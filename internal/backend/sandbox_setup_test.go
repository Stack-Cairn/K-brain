package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/sandbox"
)

func TestSandboxSetupEndpoint(t *testing.T) {
	const token = "backend-test-token"
	server := &Server{token: token}
	var result error
	calls := 0
	old := sandboxSetup
	sandboxSetup = func(context.Context) (sandbox.SetupStatus, error) {
		calls++
		return sandbox.SetupStatus{Platform: "windows", Available: true, Installed: result == nil}, result
	}
	t.Cleanup(func() { sandboxSetup = old })

	do := func(method, auth string) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, "/v1/sandbox/setup", nil)
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec, body
	}

	if rec, _ := do(http.MethodPost, ""); rec.Code != http.StatusUnauthorized || calls != 0 {
		t.Fatalf("setup without the token = %d (calls %d)", rec.Code, calls)
	}
	if rec, _ := do(http.MethodPost, "wrong"); rec.Code != http.StatusUnauthorized || calls != 0 {
		t.Fatalf("setup with a wrong token = %d (calls %d)", rec.Code, calls)
	}
	if rec, _ := do(http.MethodGet, token); rec.Code != http.StatusMethodNotAllowed || calls != 0 {
		t.Fatalf("GET setup = %d (calls %d)", rec.Code, calls)
	}

	before := healthRevision(t, server, token)
	result = nil
	rec, body := do(http.MethodPost, token)
	if rec.Code != http.StatusOK || body["status"].(map[string]any)["installed"] != true || body["run_sandbox"] == nil || body["version"] == nil {
		t.Fatalf("setup = %d %v", rec.Code, body)
	}
	after := healthRevision(t, server, token)
	if after <= before {
		t.Fatalf("capabilityRevision %v -> %v after a setup", before, after)
	}

	for _, tc := range []struct {
		err  error
		code int
		want string
	}{
		{sandbox.ErrSetupDeclined, http.StatusForbidden, "declined"},
		{errors.Join(errors.New("wrap"), sandbox.ErrSetupUnsupported), http.StatusNotImplemented, "unsupported"},
		{errors.New("NetUserAdd failed"), http.StatusInternalServerError, "failed"},
	} {
		result = tc.err
		rec, body := do(http.MethodPost, token)
		if rec.Code != tc.code || body["code"] != tc.want || body["error"] == "" {
			t.Fatalf("setup with %v = %d %v, want %d %s", tc.err, rec.Code, body, tc.code, tc.want)
		}
	}

	// One setup at a time.
	sandboxSetupMu.Lock()
	rec, body = do(http.MethodPost, token)
	sandboxSetupMu.Unlock()
	if rec.Code != http.StatusConflict || body["code"] != "busy" {
		t.Fatalf("concurrent setup = %d %v", rec.Code, body)
	}
}

func healthRevision(t *testing.T, server *Server, token string) float64 {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	var health map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	caps := health["capabilities"].(map[string]any)
	if caps["sandbox_setup"] != float64(1) {
		t.Fatalf("capabilities.sandbox_setup = %v", caps["sandbox_setup"])
	}
	rs := caps["run_sandbox"].(map[string]any)
	for _, key := range []string{"setup_available", "setup_hint"} {
		if _, ok := rs[key]; !ok {
			t.Fatalf("run_sandbox lacks %s: %v", key, rs)
		}
	}
	return health["capabilityRevision"].(float64)
}
