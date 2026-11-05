package backend

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func imageHTTPFixture(t *testing.T) *Server {
	t.Helper()
	store, err := imaging.OpenStore(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	assets, err := imaging.NewAssetStore(store, filepath.Join(t.TempDir(), "assets"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = assets.Close() })
	settings, err := NewImageSettingsManager(NewSettingsStore(config.Default(), func(*config.Config) error { return nil }), filepath.Join(t.TempDir(), "secrets.json"), ImageSettingsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return &Server{token: "test-image-token", image: &ImageModule{Settings: settings, Store: store, Assets: assets, Jobs: &imageHTTPJobsFake{store: store}, AllowLocalImageScope: true}}
}
func imageHTTPRequest(s *Server, method, path string, body io.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, body)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Authorization", "Bearer test-image-token")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func imageAssertError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var v struct {
		Version string                                    `json:"version"`
		Error   struct{ Code, Message, RequestID string } `json:"error"`
	}
	if json.Unmarshal(w.Body.Bytes(), &v) != nil || w.Code != status || v.Version != imaging.WireVersion || v.Error.Code != code || v.Error.RequestID == "" {
		t.Fatalf("bad error %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Request-ID") != v.Error.RequestID {
		t.Fatal("request ID mismatch")
	}
}
func TestImageHTTPAuthScopeCapability(t *testing.T) {
	for _, tc := range []struct {
		name                string
		token, auth, remote string
		module, local       bool
		status              int
		code                string
	}{
		{"unauthorized", "test-image-token", "wrong", "127.0.0.1:1", true, true, 401, imaging.CodeAuthRequired},
		{"nil", "test-image-token", "Bearer test-image-token", "127.0.0.1:1", false, true, 503, imaging.CodeUnavailable},
		{"opt in required", "test-image-token", "Bearer test-image-token", "127.0.0.1:1", true, false, 403, imaging.CodeForbidden},
		{"no token", "", "", "127.0.0.1:1", true, true, 403, imaging.CodeForbidden},
		{"remote", "test-image-token", "Bearer test-image-token", "192.0.2.1:1", true, true, 403, imaging.CodeForbidden},
		{"bare token", "test-image-token", "test-image-token", "127.0.0.1:1", true, true, 403, imaging.CodeForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{token: tc.token}
			if tc.module {
				s.image = &ImageModule{AllowLocalImageScope: tc.local}
			}
			r := httptest.NewRequest("GET", "/v1/image/targets", nil)
			r.RemoteAddr = tc.remote
			r.Header.Set("Authorization", tc.auth)
			r.Header.Set("X-Owner-ID", "local")
			r.Header.Set("X-Forwarded-For", "127.0.0.1")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			imageAssertError(t, w, tc.status, tc.code)
		})
	}
	for _, enabled := range []bool{false, true} {
		s := &Server{token: "test-image-token"}
		if enabled {
			s.image = &ImageModule{}
		}
		w := imageHTTPRequest(s, "GET", "/v1/health", nil)
		var v struct {
			Capabilities struct {
				Image struct {
					Supported bool
					Version   string
				}
			}
		}
		if json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Capabilities.Image.Supported != enabled || v.Capabilities.Image.Version != imaging.WireVersion {
			t.Fatal(w.Body.String())
		}
	}
}
func TestImageHTTPStrictJSONAndErrors(t *testing.T) {
	s := imageHTTPFixture(t)
	for _, body := range []string{`null`, `{"title":"x"} {}`, `{"title":"x","ownerId":"other"}`, `{"title":"x","version":"wrong"}`, `{"title":`} {
		w := imageHTTPRequest(s, "POST", "/v1/image/projects", strings.NewReader(body))
		imageAssertError(t, w, 422, imaging.CodeInvalidInput)
	}
	w := imageHTTPRequest(s, "POST", "/v1/image/projects", strings.NewReader(`{"title":"`+strings.Repeat("x", maxBodyBytes)+`"}`))
	imageAssertError(t, w, 413, imaging.CodeTooLarge)
	w = imageHTTPRequest(s, "PUT", "/v1/image/projects", nil)
	imageAssertError(t, w, 405, "IMAGE_METHOD_NOT_ALLOWED")
	if w.Header().Get("Allow") == "" {
		t.Fatal("missing Allow")
	}
	w = imageHTTPRequest(s, "GET", "/v1/image/projects?limit=101", nil)
	imageAssertError(t, w, 422, imaging.CodeInvalidInput)
	r := imageRequest(httptest.NewRequest("GET", "/v1/image/jobs", nil))
	w = httptest.NewRecorder()
	imageHTTPError(w, r, errors.New("PRIVATE_SENTINEL"))
	if bytes.Contains(w.Body.Bytes(), []byte("PRIVATE_SENTINEL")) {
		t.Fatal("raw reason leaked")
	}
	w = imageHTTPRequest(s, "OPTIONS", "/v1/image/jobs", nil)
	if w.Code != 204 || !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "Last-Event-ID") || !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "Idempotency-Key") || !strings.Contains(w.Header().Get("Access-Control-Allow-Methods"), "HEAD") {
		t.Fatal("image CORS missing")
	}
}
func TestImageHTTPErrorKeepsProviderOutcomeCodes(t *testing.T) {
	for _, tc := range []struct {
		code   string
		status int
	}{
		{imaging.CodeResultUnknown, 502},
		{imaging.CodeNoImageOutput, 502},
		{imaging.CodeOutputSaveFailed, 502},
		{"IMAGE_SOMETHING_NEW", 503},
	} {
		r := imageRequest(httptest.NewRequest("GET", "/v1/image/jobs", nil))
		w := httptest.NewRecorder()
		imageHTTPError(w, r, &imaging.ImageError{Code: tc.code, Reason: "no response from https://u.example/p?k=1: request timed out"})
		want := tc.code
		if tc.code == "IMAGE_SOMETHING_NEW" {
			want = imaging.CodeUnavailable
		}
		var body struct {
			Error struct {
				Code   string `json:"code"`
				Reason string `json:"reason"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != tc.status || body.Error.Code != want {
			t.Fatalf("%s: got %d %s, want %d %s", tc.code, w.Code, body.Error.Code, tc.status, want)
		}
		if body.Error.Reason == "" || strings.Contains(body.Error.Reason, "u.example") {
			t.Fatalf("%s: reason = %q", tc.code, body.Error.Reason)
		}
	}
	r := imageRequest(httptest.NewRequest("GET", "/v1/image/jobs", nil))
	w := httptest.NewRecorder()
	imageHTTPError(w, r, errors.New("PRIVATE_SENTINEL"))
	if bytes.Contains(w.Body.Bytes(), []byte(`"reason"`)) {
		t.Fatal("untyped errors must not produce a reason")
	}
}
func TestImageHTTPDelegationCannotFallBackToLocal(t *testing.T) {
	s := imageHTTPFixture(t)
	for _, header := range []string{"X-Kbrain-Image-Delegation", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host"} {
		r := httptest.NewRequest("GET", "/v1/image/projects", nil)
		r.RemoteAddr = "127.0.0.1:1"
		r.Header.Set("Authorization", "Bearer test-image-token")
		r.Header.Set(header, "untrusted")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		imageAssertError(t, w, 403, imaging.CodeForbidden)
	}
	r := httptest.NewRequest("GET", "/v1/image/projects", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("Authorization", "Bearer test-image-token")
	r.Header.Set("X-Owner-ID", "other")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}

func TestImageHTTPScopeResolverConfigurationGrant(t *testing.T) {
	s := imageHTTPFixture(t)
	calls := 0
	s.image.ResolveScope = func(r *http.Request) (ImageAuthorization, error) {
		calls++
		return ImageAuthorization{Scope: imaging.Scope{OwnerID: "delegated-user", BackendID: "delegated-backend"}}, nil
	}
	for _, route := range []string{"settings", "secrets"} {
		w := imageHTTPRequest(s, "GET", "/v1/image/"+route, nil)
		imageAssertError(t, w, 403, imaging.CodeForbidden)
	}
	w := imageHTTPRequest(s, "POST", "/v1/image/projects", strings.NewReader(`{"title":"delegated"}`))
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var v struct{ Project imaging.ImageProject }
	if json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatal(w.Body.String())
	}
	if _, err := s.image.Store.ReadProject(imageTestScope(), v.Project.ID); err == nil {
		t.Fatal("delegated project became local")
	}
	before := calls
	r := httptest.NewRequest("GET", "/v1/image/projects", nil)
	r.Header.Set("Authorization", "Bearer wrong")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	imageAssertError(t, w, 401, imaging.CodeAuthRequired)
	if calls != before {
		t.Fatal("resolver ran before token authentication")
	}
	s.image.ResolveScope = func(*http.Request) (ImageAuthorization, error) {
		return ImageAuthorization{}, errors.New("private delegation cause")
	}
	w = imageHTTPRequest(s, "GET", "/v1/image/projects", nil)
	imageAssertError(t, w, 503, imaging.CodeUnavailable)
	if strings.Contains(w.Body.String(), "private delegation cause") {
		t.Fatal("delegation cause leaked")
	}
}

func TestImageHTTPSettings(t *testing.T) {
	s := imageHTTPFixture(t)
	for _, body := range []string{`{"connections":[],"targets":[]}`, `{"expectedRevision":null}`, `{"expectedRevision":9007199254740992}`} {
		w := imageHTTPRequest(s, "PUT", "/v1/image/settings", strings.NewReader(body))
		imageAssertError(t, w, 422, imaging.CodeInvalidInput)
	}
	w := imageHTTPRequest(s, "PUT", "/v1/image/settings", strings.NewReader(`{"expectedRevision":0,"connections":[],"targets":[]}`))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = imageHTTPRequest(s, "PUT", "/v1/image/settings", strings.NewReader(`{"expectedRevision":0,"connections":[],"targets":[]}`))
	imageAssertError(t, w, 409, imaging.CodeRevisionConflict)
	for _, path := range []string{"settings", "targets"} {
		w = imageHTTPRequest(s, "GET", "/v1/image/"+path, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), imaging.WireVersion) {
			t.Fatal(w.Body.String())
		}
	}
	w = imageHTTPRequest(s, "GET", "/v1/image/secrets", nil)
	imageAssertError(t, w, 405, "IMAGE_METHOD_NOT_ALLOWED")
}
