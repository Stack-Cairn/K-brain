package backend

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

const healthTestToken = "test-image-token"

func healthJSON(t *testing.T, s *Server) map[string]any {
	t.Helper()
	w := imageHTTPRequest(s, http.MethodGet, "/v1/health", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("health status = %d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	dec := json.NewDecoder(strings.NewReader(w.Body.String()))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func jsonInt(t *testing.T, v any, field string) int64 {
	t.Helper()
	n, ok := v.(json.Number)
	if !ok {
		t.Fatalf("%s = %#v, want JSON integer", field, v)
	}
	i, err := n.Int64()
	if err != nil {
		t.Fatalf("%s = %s, want integer: %v", field, n, err)
	}
	return i
}

// K01: the legacy v1 marker stays and the structured fields agree with it.
func TestHealthStructuredContract(t *testing.T) {
	h := healthJSON(t, &Server{token: healthTestToken})
	if h["version"] != "kbrain.agent.v1" || h["status"] != "ok" {
		t.Fatalf("legacy fields changed: %#v", h)
	}
	p, ok := h["protocol"].(map[string]any)
	if !ok || p["name"] != "kbrain.agent" || jsonInt(t, p["major"], "protocol.major") != 1 {
		t.Fatalf("protocol = %#v", h["protocol"])
	}
	if jsonInt(t, p["minor"], "protocol.minor") < 1 {
		t.Fatalf("protocol.minor = %v", p["minor"])
	}
	if jsonInt(t, h["startupContract"], "startupContract") != 1 {
		t.Fatalf("startupContract = %v", h["startupContract"])
	}
	if jsonInt(t, h["capabilityRevision"], "capabilityRevision") < 1 {
		t.Fatalf("capabilityRevision = %v", h["capabilityRevision"])
	}
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if id, _ := h["instanceId"].(string); !uuid.MatchString(id) {
		t.Fatalf("instanceId = %#v", h["instanceId"])
	}
	build, ok := h["build"].(map[string]any)
	if !ok || build["version"] != "dev" || build["channel"] != "dev" {
		t.Fatalf("zero-value server build = %#v", h["build"])
	}
	if _, present := build["releaseSequence"]; present {
		t.Fatal("releaseSequence must stay absent until signed release metadata exists")
	}
	for _, unimplemented := range []string{"instance_precondition", "backend_drain"} {
		if _, present := h["capabilities"].(map[string]any)[unimplemented]; present {
			t.Fatalf("%s advertised before it is implemented", unimplemented)
		}
	}
	if _, present := h["storage"]; present {
		t.Fatal("storage block advertised before storage markers exist")
	}
}

func TestHealthInstanceIDIsPerProcessAndStable(t *testing.T) {
	a := &Server{token: healthTestToken}
	b := &Server{token: healthTestToken}
	first := healthJSON(t, a)["instanceId"]
	if again := healthJSON(t, a)["instanceId"]; again != first {
		t.Fatalf("instanceId changed within one server: %v -> %v", first, again)
	}
	if other := healthJSON(t, b)["instanceId"]; other == first {
		t.Fatalf("two servers share instanceId %v", first)
	}
}

func TestHealthReportsInjectedBuild(t *testing.T) {
	cases := []struct {
		version, revision, wantVersion, wantChannel string
	}{
		{"v0.200.0-rc.1", "abc123", "v0.200.0-rc.1", "prerelease"},
		{"v1.0.0", "def456", "v1.0.0", "stable"},
		{"", "", "dev", "dev"},
		{" dev ", "", "dev", "dev"},
	}
	for _, tc := range cases {
		s := &Server{token: healthTestToken, build: protocol.NewBuild(tc.version, tc.revision)}
		build := healthJSON(t, s)["build"].(map[string]any)
		if build["version"] != tc.wantVersion || build["channel"] != tc.wantChannel {
			t.Fatalf("NewBuild(%q) health build = %#v", tc.version, build)
		}
		if got, _ := build["revision"].(string); got != tc.revision {
			t.Fatalf("revision = %q, want %q", got, tc.revision)
		}
	}
}

type registryEntry struct {
	Name           string          `json:"name"`
	Type           string          `json:"type"`
	Compare        string          `json:"compare"`
	Value          json.RawMessage `json:"value"`
	FirstRelease   string          `json:"firstRelease"`
	EvidenceCommit string          `json:"evidenceCommit"`
	Missing        string          `json:"missing"`
}

func loadCapabilityRegistry(t *testing.T) []registryEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "protocol", "capabilities.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema       int             `json:"schema"`
		Kind         string          `json:"kind"`
		Repository   string          `json:"repository"`
		Protocol     json.RawMessage `json:"protocol"`
		Notes        string          `json:"notes"`
		Capabilities []registryEntry `json:"capabilities"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != 1 || doc.Kind != "capability-registry" {
		t.Fatalf("registry header = %d/%q", doc.Schema, doc.Kind)
	}
	return doc.Capabilities
}

// objectFieldMatches checks one field of an "object" capability: the type names
// boolean, string and string[] accept any value of that type; anything else is a
// frozen literal.
func objectFieldMatches(got, want any) bool {
	switch want {
	case "boolean":
		_, ok := got.(bool)
		return ok
	case "string":
		_, ok := got.(string)
		return ok
	case "string[]":
		list, ok := got.([]any)
		if !ok {
			return false
		}
		for _, item := range list {
			if _, ok := item.(string); !ok {
				return false
			}
		}
		return true
	}
	return got == want
}

// K01/A05: every advertised capability is registered and satisfies its
// registered comparison; frozen integers never move off their value.
func TestHealthCapabilitiesMatchRegistry(t *testing.T) {
	caps := healthJSON(t, &Server{token: healthTestToken, image: &ImageModule{}})["capabilities"].(map[string]any)
	registry := loadCapabilityRegistry(t)
	seen := map[string]bool{}
	for _, entry := range registry {
		if entry.FirstRelease == "" || entry.Missing == "" {
			t.Fatalf("%s lacks firstRelease or missing behavior", entry.Name)
		}
		seen[entry.Name] = true
		got, ok := caps[entry.Name]
		if !ok {
			t.Fatalf("registered capability %s missing from health", entry.Name)
		}
		switch entry.Compare {
		case "eq", "gte":
			var want int64
			if err := json.Unmarshal(entry.Value, &want); err != nil {
				t.Fatalf("%s value: %v", entry.Name, err)
			}
			have := jsonInt(t, got, entry.Name)
			if (entry.Compare == "eq" && have != want) || (entry.Compare == "gte" && have < want) {
				t.Fatalf("%s = %d violates %s %d", entry.Name, have, entry.Compare, want)
			}
		case "members":
			var want []string
			if err := json.Unmarshal(entry.Value, &want); err != nil {
				t.Fatalf("%s value: %v", entry.Name, err)
			}
			list, ok := got.([]any)
			if !ok {
				t.Fatalf("%s = %#v, want list", entry.Name, got)
			}
			var have []string
			for _, item := range list {
				s, ok := item.(string)
				if !ok {
					t.Fatalf("%s contains non-string %#v", entry.Name, item)
				}
				have = append(have, s)
			}
			for _, mode := range want {
				if !slices.Contains(have, mode) {
					t.Fatalf("%s dropped registered member %q", entry.Name, mode)
				}
			}
		case "object":
			obj, ok := got.(map[string]any)
			if !ok {
				t.Fatalf("%s = %#v, want object", entry.Name, got)
			}
			var shape map[string]any
			if err := json.Unmarshal(entry.Value, &shape); err != nil {
				t.Fatalf("%s value: %v", entry.Name, err)
			}
			for field, want := range shape {
				if !objectFieldMatches(obj[field], want) {
					t.Fatalf("%s.%s = %#v, want %#v", entry.Name, field, obj[field], want)
				}
			}
		default:
			t.Fatalf("%s has unknown compare %q", entry.Name, entry.Compare)
		}
	}
	for name := range caps {
		if !seen[name] {
			t.Fatalf("health advertises unregistered capability %s", name)
		}
	}
}

// Legacy consumers copied from LiveAgent upstream/v2-kbrian@336a6e75. They must
// keep decoding the new health body without any code change.
func TestHealthLegacyConsumersStillAccept(t *testing.T) {
	w := imageHTTPRequest(&Server{token: healthTestToken, image: &ImageModule{}}, http.MethodGet, "/v1/health", nil)
	body := w.Body.String()

	// crates/agent-gateway/internal/kbrain/backend_queue_client.go EnableBackendQueue
	var gatewayQueue struct {
		Capabilities struct {
			SessionQueue int `json:"session_queue"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal([]byte(body), &gatewayQueue); err != nil || gatewayQueue.Capabilities.SessionQueue != 1 {
		t.Fatalf("gateway queue consumer: %+v err=%v", gatewayQueue, err)
	}

	// crates/agent-gui/src-tauri/src/services/kbrain_backend.rs and
	// crates/agent-gui/src/lib/kbrain/bootstrap.ts: status ok and exact v1 string.
	var desktop struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(body), &desktop); err != nil || desktop.Status != "ok" || desktop.Version != protocol.Version {
		t.Fatalf("desktop handshake consumer: %+v err=%v", desktop, err)
	}

	// crates/agent-gui/src/lib/kbrain/sessionSync.ts: session_changes >= 2.
	var sync struct {
		Capabilities struct {
			SessionChanges int      `json:"session_changes"`
			PermissionMode []string `json:"permission_modes"`
			Image          struct {
				Supported bool   `json:"supported"`
				Version   string `json:"version"`
			} `json:"image"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal([]byte(body), &sync); err != nil || sync.Capabilities.SessionChanges < 2 || !sync.Capabilities.Image.Supported || !slices.Contains(sync.Capabilities.PermissionMode, "auto_review") {
		t.Fatalf("session sync / settings consumer: %+v err=%v", sync, err)
	}
}

// K02: request decoding still rejects fields a capability did not introduce.
func TestRequestsRejectUnknownFields(t *testing.T) {
	w := imageHTTPRequest(&Server{token: healthTestToken}, http.MethodPost, "/v1/sessions", strings.NewReader(`{"instance_hint":"x"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d body=%s", w.Code, w.Body.String())
	}
}
