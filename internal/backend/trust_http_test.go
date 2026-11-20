package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/resources"
)

func newTrustTestServer(t *testing.T) func(method, path, body string, status int) []byte {
	t.Helper()
	isolateUserDir(t)
	t.Setenv("K_BRAIN_HOME", "")
	root, err := config.Dir()
	if err != nil {
		t.Fatal(err)
	}
	store, err := resources.OpenPrompts(root)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{prompts: store, token: "trust-token"}
	return func(method, path, body string, status int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer trust-token")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
}

func decodeTrustStatus(t *testing.T, data []byte) trustStatus {
	t.Helper()
	var out trustStatus
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTrustRoutesRoundTripAndGateProjectPrompts(t *testing.T) {
	request := newTrustTestServer(t)
	workdir := t.TempDir()
	// Reach the project through a symlink so the reply must canonicalize it.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(workdir, link); err != nil {
		t.Fatal(err)
	}
	canonical, err := resources.CanonicalWorkdir(workdir)
	if err != nil {
		t.Fatal(err)
	}
	writePromptTemplate(t, filepath.Join(config.ProjectDir(canonical), "prompts"), "ship", "---\ndescription: Ship it\n---\nShip $1")

	query := "?workdir=" + url.QueryEscape(link)
	projectVisible := func() (inPrompts, inCommands bool) {
		t.Helper()
		var prompts struct {
			Files []map[string]any `json:"files"`
		}
		if err := json.Unmarshal(request(http.MethodGet, "/v1/prompts"+query, "", 200), &prompts); err != nil {
			t.Fatal(err)
		}
		for _, file := range prompts.Files {
			if file["name"] == "ship" {
				inPrompts = true
			}
		}
		var commands protocol.SlashCommandList
		if err := json.Unmarshal(request(http.MethodGet, "/v1/commands"+query, "", 200), &commands); err != nil {
			t.Fatal(err)
		}
		for _, command := range commands.Commands {
			if command.Name == "ship" && command.Source == "project" {
				inCommands = true
			}
		}
		return inPrompts, inCommands
	}

	status := decodeTrustStatus(t, request(http.MethodGet, "/v1/trust"+query, "", 200))
	if status.Trusted || status.Workdir != canonical || status.Source != "" || status.Version != protocol.Version {
		t.Fatalf("initial status = %+v (canonical %s)", status, canonical)
	}
	if p, c := projectVisible(); p || c {
		t.Fatalf("untrusted project templates leaked: prompts=%v commands=%v", p, c)
	}

	body, _ := json.Marshal(map[string]string{"workdir": link})
	status = decodeTrustStatus(t, request(http.MethodPost, "/v1/trust", string(body), 200))
	if !status.Trusted || status.Workdir != canonical || status.Source != canonical {
		t.Fatalf("after trust = %+v", status)
	}
	if !config.Trusted(canonical) {
		t.Fatal("trust was not persisted")
	}
	if status := decodeTrustStatus(t, request(http.MethodGet, "/v1/trust"+query, "", 200)); !status.Trusted {
		t.Fatalf("status after trust = %+v", status)
	}
	if p, c := projectVisible(); !p || !c {
		t.Fatalf("trusted project templates missing without restart: prompts=%v commands=%v", p, c)
	}

	status = decodeTrustStatus(t, request(http.MethodDelete, "/v1/trust"+query, "", 200))
	if status.Trusted || status.Workdir != canonical {
		t.Fatalf("after revoke = %+v", status)
	}
	if p, c := projectVisible(); p || c {
		t.Fatalf("revoked project templates still visible: prompts=%v commands=%v", p, c)
	}

	request(http.MethodPost, "/v1/trust", string(body), 200)
	revoke, _ := json.Marshal(map[string]any{"workdir": canonical, "trusted": false})
	if status := decodeTrustStatus(t, request(http.MethodPost, "/v1/trust", string(revoke), 200)); status.Trusted {
		t.Fatalf("POST trusted:false = %+v", status)
	}
	if config.Trusted(canonical) {
		t.Fatal("POST trusted:false did not revoke")
	}
}

func TestTrustRoutesRejectBadPaths(t *testing.T) {
	request := newTrustTestServer(t)
	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	for _, workdir := range []string{"", "relative/dir", ".", missing, file} {
		query := "/v1/trust?workdir=" + url.QueryEscape(workdir)
		request(http.MethodGet, query, "", http.StatusBadRequest)
		request(http.MethodDelete, query, "", http.StatusBadRequest)
		body, _ := json.Marshal(map[string]string{"workdir": workdir})
		request(http.MethodPost, "/v1/trust", string(body), http.StatusBadRequest)
	}
	request(http.MethodPost, "/v1/trust", `{"workdir":"/","extra":1}`, http.StatusBadRequest)
	request(http.MethodPut, "/v1/trust", `{}`, http.StatusMethodNotAllowed)
	if _, err := os.Stat(filepath.Join(missing)); !os.IsNotExist(err) {
		t.Fatal("a rejected path must not be created")
	}
	path, err := config.TrustedPath()
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) != "" {
		t.Fatalf("rejected requests wrote trust records:\n%s", data)
	}
}

func TestTrustRoutesRequireBackendToken(t *testing.T) {
	isolateUserDir(t)
	server := &Server{token: "trust-token"}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		r := httptest.NewRequest(method, "/v1/trust?workdir="+url.QueryEscape(t.TempDir()), strings.NewReader(`{"workdir":"/"}`))
		r.Header.Set("Authorization", "Bearer wrong")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s without token: %d", method, w.Code)
		}
	}
}
