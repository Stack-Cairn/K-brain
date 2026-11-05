package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/imaging"
	"github.com/Stack-Cairn/K-brain/internal/netproxy"
)

func imageTestScope() imaging.Scope { return imaging.Scope{OwnerID: "local", BackendID: "local"} }
func imageTestConnection() imaging.ImageConnection {
	return imaging.ImageConnection{ID: "ic_test", Name: "test", Enabled: true, MaxConcurrent: 2, Binding: imaging.ConnectionBinding{Kind: "independent", BaseURL: "https://example.invalid", AuthKind: "bearer"}}
}
func imageTestManager(t *testing.T, save func(*config.Config) error) (*ImageSettingsManager, *SettingsStore, string) {
	t.Helper()
	s := NewSettingsStore(config.Default(), save)
	path := filepath.Join(t.TempDir(), "secrets.json")
	m, err := NewImageSettingsManager(s, path, ImageSettingsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return m, s, path
}
func TestImageSettingsRevisionConcurrentAndSaveFailure(t *testing.T) {
	ctx := context.Background()
	scope := imageTestScope()
	var fail atomic.Bool
	var saves atomic.Int32
	m, s, _ := imageTestManager(t, func(*config.Config) error {
		saves.Add(1)
		if fail.Load() {
			return errors.New("fake-private-sentinel")
		}
		return nil
	})
	var wg sync.WaitGroup
	var success atomic.Int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.UpdateImageSettings(ctx, scope, imaging.ImageSettingsUpdate{Connections: []imaging.ImageConnection{imageTestConnection()}})
			if err == nil {
				success.Add(1)
			} else if !strings.Contains(err.Error(), imaging.CodeRevisionConflict) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatalf("committed %d writers", success.Load())
	}
	fail.Store(true)
	c := imageTestConnection()
	c.Name = "must not publish"
	_, err := m.UpdateImageSettings(ctx, scope, imaging.ImageSettingsUpdate{ExpectedRevision: 1, Connections: []imaging.ImageConnection{c}})
	if saves.Load() != 2 {
		t.Fatal("failure did not reach persistence")
	}
	if err == nil || strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("save error not sanitized: %v", err)
	}
	got, _ := m.ImageSettings(scope)
	if got.Revision != 1 || len(got.Connections) != 1 || s.Snapshot().Image.Revision != 1 {
		t.Fatal("failed save published")
	}
}
func TestImageSettingsSnapshotSaveIsolation(t *testing.T) {
	m, store, _ := imageTestManager(t, func(c *config.Config) error {
		c.Image.Connections[0].Name = "saver mutation"
		c.Image.Connections[0].Binding.CustomHeaders["X-Private"] = "saver mutation"
		return nil
	})
	connection := imageTestConnection()
	connection.Binding.CustomHeaders = map[string]string{"X-Private": "original"}
	update := imaging.ImageSettingsUpdate{Connections: []imaging.ImageConnection{connection}}
	got, err := m.UpdateImageSettings(context.Background(), imageTestScope(), update)
	if err != nil {
		t.Fatal(err)
	}
	got.Connections[0].Name = "public mutation"
	update.Connections[0].Binding.CustomHeaders["X-Private"] = "input mutation"
	snap := store.Snapshot()
	if snap.Image.Connections[0].Name != "test" || snap.Image.Connections[0].Binding.CustomHeaders["X-Private"] != "original" {
		t.Fatal("published configuration aliases caller or saver")
	}
	// Attaching a secret to a public GET must preserve hidden custom headers.
	secret, err := m.WriteImageSecret(context.Background(), imageTestScope(), imaging.ImageSecretInput{ConnectionID: connection.ID, Purpose: "apiKey", Value: "literal"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ = m.ImageSettings(imageTestScope())
	got.Connections[0].Binding.SecretRefs = map[string]string{"apiKey": secret.Ref}
	if _, err := m.UpdateImageSettings(context.Background(), imageTestScope(), imaging.ImageSettingsUpdate{ExpectedRevision: got.Revision, Connections: got.Connections}); err != nil {
		t.Fatal(err)
	}
	if store.Snapshot().Image.Connections[0].Binding.CustomHeaders["X-Private"] != "original" {
		t.Fatal("attaching secret lost private headers")
	}
}

func TestImageSettingsAcceptancePreventsDeletion(t *testing.T) {
	cfg := config.Default()
	target := imageTestTarget()
	target.Enabled = true
	cfg.Image = &imaging.ImageSettings{Connections: []imaging.ImageConnection{imageTestConnection()}, Targets: []imaging.ImageTarget{target}}
	store := NewSettingsStore(cfg, func(*config.Config) error { return nil })
	var accepted bool // Accessed only while holding the settings lock.
	m, err := NewImageSettingsManager(store, filepath.Join(t.TempDir(), "secrets.json"), ImageSettingsOptions{Dependencies: imageDependencyFunc(func(context.Context, imaging.Scope, []string, []string) (bool, error) { return accepted, nil })})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	acceptResult, deleteResult := make(chan error, 1), make(chan error, 1)
	go func() {
		acceptResult <- m.WithImageTarget(context.Background(), imageTestScope(), target.ID, func(snapshot imaging.TargetSnapshot) error {
			if snapshot.ID != target.ID {
				return errors.New("wrong target")
			}
			close(entered)
			<-release
			accepted = true
			return nil
		})
	}()
	<-entered
	go func() {
		_, err := m.UpdateImageSettings(context.Background(), imageTestScope(), imaging.ImageSettingsUpdate{})
		deleteResult <- err
	}()
	close(release)
	if err := <-acceptResult; err != nil {
		t.Fatal(err)
	}
	if err := <-deleteResult; err == nil || !strings.Contains(err.Error(), imaging.CodeConnectionInUse) {
		t.Fatalf("accepted job dependency deleted: %v", err)
	}
	for _, tc := range []struct {
		cause error
		code  string
	}{
		{&imaging.ImageError{Code: imaging.CodeIdempotencyConflict, Message: "private-sentinel", RequestID: "private-sentinel", Details: &imaging.ErrorDetails{ConnectionID: "private-sentinel"}}, imaging.CodeIdempotencyConflict},
		{errors.New(imaging.CodeInvalidInput + ": private-sentinel"), imaging.CodeInvalidInput},
		{imaging.ErrUnsupported, imaging.CodeUnsupportedOperation},
		{errors.New("private-sentinel"), imaging.CodeUnavailable},
	} {
		err := m.WithImageTarget(context.Background(), imageTestScope(), target.ID, func(imaging.TargetSnapshot) error { return tc.cause })
		var public *imaging.ImageError
		if !errors.As(err, &public) || public.Code != tc.code {
			t.Fatalf("lost acceptance error code: %v", err)
		}
		raw, _ := json.Marshal(public)
		if strings.Contains(string(raw), "private-sentinel") {
			t.Fatal("acceptance error leaked private context")
		}
	}
}

func TestImageSecretRedactionLiteralAndReload(t *testing.T) {
	ctx := context.Background()
	scope := imageTestScope()
	m, s, path := imageTestManager(t, func(*config.Config) error { return nil })
	_, err := m.UpdateImageSettings(ctx, scope, imaging.ImageSettingsUpdate{Connections: []imaging.ImageConnection{imageTestConnection()}})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "!fake-sentinel-command-do-not-execute"
	result, err := m.WriteImageSecret(ctx, scope, imaging.ImageSecretInput{ConnectionID: "ic_test", Purpose: "apiKey", Value: secret})
	if err != nil {
		t.Fatal(err)
	}
	c := imageTestConnection()
	c.Binding.SecretRefs = map[string]string{"apiKey": result.Ref}
	_, err = m.UpdateImageSettings(ctx, scope, imaging.ImageSettingsUpdate{ExpectedRevision: 1, Connections: []imaging.ImageConnection{c}})
	if err != nil {
		t.Fatal(err)
	}
	m, err = NewImageSettingsManager(s, path, ImageSettingsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	public, err := m.ImageSettings(scope)
	if err != nil || !public.Connections[0].CredentialConfigured {
		t.Fatalf("not configured: %v", err)
	}
	raw, _ := json.Marshal(public)
	if strings.Contains(string(raw), secret) {
		t.Fatal("secret leaked")
	}
	resolved, err := m.ResolveImageConnection(ctx, scope, "ic_test")
	if err != nil {
		t.Fatal(err)
	}
	literal, err := resolved.SecretResolver.ResolveSecret(ctx, scope, result.Ref)
	if err != nil || literal != secret {
		t.Fatalf("literal changed: %v", err)
	}
	if _, err := json.Marshal(resolved); err == nil {
		t.Fatal("execution data serialized")
	}
	other := imaging.Scope{OwnerID: "another", BackendID: "local"}
	if _, err = m.ImageSettings(other); err == nil {
		t.Fatal("accepted non-local owner")
	}
	if _, err = resolved.SecretResolver.ResolveSecret(ctx, other, result.Ref); err == nil {
		t.Fatal("secret crossed owner boundary")
	}
}
func imageTestTarget() imaging.ImageTarget {
	return imaging.ImageTarget{ID: "it_test", ConnectionID: "ic_test", Adapter: "openai-images", AdapterVersion: "v1", Kind: "model", RemoteID: "fake-image", Revision: 2, Capabilities: imaging.TargetCapabilities{Operations: []imaging.Operation{imaging.OperationGenerate}, MaxCount: 1, OutputFormats: []string{"png"}}, Verification: imaging.TargetVerification{ContractComplete: true, LiveVerifiedAt: time.Unix(123, 0).UTC(), ConnectionID: "ic_test", AdapterVersion: "v1", TargetRevision: 2}}
}
func TestImageSettingsTrustedEvidenceReloadAndPublicRoundTrip(t *testing.T) {
	ctx := context.Background()
	scope := imageTestScope()
	cfg := config.Default()
	c := imageTestConnection()
	c.Binding.CustomHeaders = map[string]string{"X-Private": "header-sentinel"}
	target := imageTestTarget()
	target.Enabled = true
	cfg.Image = &imaging.ImageSettings{Revision: 4, Connections: []imaging.ImageConnection{c}, Targets: []imaging.ImageTarget{target}}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := cfg.SaveFile(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSettingsStore(cfg, func(c *config.Config) error { return c.SaveFile(path) })
	m, err := NewImageSettingsManager(s, filepath.Join(t.TempDir(), "secrets.json"), ImageSettingsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	public, _ := m.ImageSettings(scope)
	if public.Targets[0].Verification.ContractComplete || len(public.Connections[0].Binding.CustomHeaders) != 0 {
		t.Fatal("private fields leaked")
	}
	got, err := m.UpdateImageSettings(ctx, scope, imaging.ImageSettingsUpdate{ExpectedRevision: 4, Connections: public.Connections, Targets: public.Targets})
	if err != nil {
		t.Fatalf("public roundtrip lost private evidence: %v", err)
	}
	if got.Revision != 5 {
		t.Fatal("wrong revision")
	}
	reloaded, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Image.Revision != 5 || !reloaded.Image.Targets[0].Verification.ContractComplete || reloaded.Image.Connections[0].Binding.CustomHeaders["X-Private"] != "header-sentinel" {
		t.Fatal("reload lost private evidence")
	}
	got.Targets[0].RemoteID = "changed"
	got.Targets[0].Verification = target.Verification
	// Enabling no longer needs evidence, but forged evidence is still never stored.
	if _, err = m.UpdateImageSettings(ctx, scope, imaging.ImageSettingsUpdate{ExpectedRevision: 5, Connections: got.Connections, Targets: got.Targets}); err != nil {
		t.Fatalf("altered target rejected: %v", err)
	}
	if m.store.cfg.Image.Targets[0].Verification.ContractComplete {
		t.Fatal("caller forged evidence for altered target")
	}
	fresh, _, _ := imageTestManager(t, func(*config.Config) error { return nil })
	if _, err = fresh.UpdateImageSettings(ctx, scope, imaging.ImageSettingsUpdate{Connections: []imaging.ImageConnection{imageTestConnection()}, Targets: []imaging.ImageTarget{target}}); err != nil {
		t.Fatalf("new enabled target rejected: %v", err)
	}
	if fresh.store.cfg.Image.Targets[0].Verification.ContractComplete {
		t.Fatal("caller supplied live evidence accepted")
	}
	oldProjection, _ := json.Marshal(projectSettings(s.Snapshot()))
	privateSnapshotJSON, _ := json.Marshal(s.Snapshot().Image)
	if strings.Contains(string(privateSnapshotJSON), "header-sentinel") || strings.Contains(string(privateSnapshotJSON), "contractComplete") {
		t.Fatal("legacy snapshot image marshaling leaked private fields")
	}
	if strings.Contains(string(oldProjection), "header-sentinel") || strings.Contains(string(oldProjection), `"image"`) {
		t.Fatal("old settings projection changed")
	}
}

type imageDependencyFunc func(context.Context, imaging.Scope, []string, []string) (bool, error)

func (f imageDependencyFunc) ImageDependenciesInUse(ctx context.Context, s imaging.Scope, c, t []string) (bool, error) {
	return f(ctx, s, c, t)
}
func TestImageSettingsDeletionDependencies(t *testing.T) {
	ctx := context.Background()
	scope := imageTestScope()
	m, s, path := imageTestManager(t, func(*config.Config) error { return nil })
	target := imageTestTarget()
	target.Verification = imaging.TargetVerification{}
	first, err := m.UpdateImageSettings(ctx, scope, imaging.ImageSettingsUpdate{Connections: []imaging.ImageConnection{imageTestConnection()}, Targets: []imaging.ImageTarget{target}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.UpdateImageSettings(ctx, scope, imaging.ImageSettingsUpdate{ExpectedRevision: first.Revision, Targets: first.Targets}); err == nil || !strings.Contains(err.Error(), imaging.CodeConnectionInUse) {
		t.Fatal("removed connection with dependent target")
	}
	if _, err = m.UpdateImageSettings(ctx, scope, imaging.ImageSettingsUpdate{ExpectedRevision: 1}); err == nil {
		t.Fatal("deletion without service checker accepted")
	}
	busy := true
	calls := 0
	m, err = NewImageSettingsManager(s, path, ImageSettingsOptions{Dependencies: imageDependencyFunc(func(_ context.Context, gotScope imaging.Scope, c, tg []string) (bool, error) {
		calls++
		if gotScope != scope || len(c) != 1 || c[0] != "ic_test" || len(tg) != 1 || tg[0] != "it_test" {
			t.Error("checker got wrong dependencies")
		}
		return busy, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.UpdateImageSettings(ctx, scope, imaging.ImageSettingsUpdate{ExpectedRevision: 1}); err == nil || !strings.Contains(err.Error(), imaging.CodeConnectionInUse) {
		t.Fatal("removed active job dependencies")
	}
	busy = false
	if _, err = m.UpdateImageSettings(ctx, scope, imaging.ImageSettingsUpdate{ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("dependency check missing")
	}
}
func TestImageSecretAtomicFailureRevocationAndOwnerBinding(t *testing.T) {
	ctx := context.Background()
	scope := imageTestScope()
	m, _, path := imageTestManager(t, func(*config.Config) error { return nil })
	c := imageTestConnection()
	other := c
	other.ID = "ic_other"
	if _, err := m.UpdateImageSettings(ctx, scope, imaging.ImageSettingsUpdate{Connections: []imaging.ImageConnection{c, other}}); err != nil {
		t.Fatal(err)
	}
	secret, err := m.WriteImageSecret(ctx, scope, imaging.ImageSecretInput{ConnectionID: c.ID, Purpose: "apiKey", Value: "old-secret-sentinel"})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatal("secret file not private")
	}
	c.Binding.SecretRefs = map[string]string{"apiKey": secret.Ref}
	other.Binding.SecretRefs = map[string]string{"apiKey": secret.Ref}
	if _, err = m.UpdateImageSettings(ctx, scope, imaging.ImageSettingsUpdate{ExpectedRevision: 1, Connections: []imaging.ImageConnection{c, other}}); err == nil {
		t.Fatal("ref crossed connection boundary")
	}
	other.Binding.SecretRefs = nil
	if _, err = m.UpdateImageSettings(ctx, scope, imaging.ImageSettingsUpdate{ExpectedRevision: 1, Connections: []imaging.ImageConnection{c, other}}); err != nil {
		t.Fatal(err)
	}
	resolved, err := m.ResolveImageConnection(ctx, scope, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Renaming a file onto a directory fails after staging, on all supported OSes.
	originalPath := m.secretPath
	m.secretPath = t.TempDir()
	if _, err = m.WriteImageSecret(ctx, scope, imaging.ImageSecretInput{ConnectionID: c.ID, Purpose: "apiKey", Value: "new-secret-sentinel"}); err == nil || strings.Contains(err.Error(), "sentinel") {
		t.Fatal("secret save failure not sanitized")
	}
	m.secretPath = originalPath
	v, err := resolved.SecretResolver.ResolveSecret(ctx, scope, secret.Ref)
	if err != nil || v != "old-secret-sentinel" {
		t.Fatal("failed secret save published")
	}
	if _, err = m.WriteImageSecret(ctx, scope, imaging.ImageSecretInput{ConnectionID: c.ID, Purpose: "apiKey"}); err != nil {
		t.Fatal(err)
	}
	if _, err = resolved.SecretResolver.ResolveSecret(ctx, scope, secret.Ref); err == nil {
		t.Fatal("previous execution resolver ignored revocation")
	}
	got, _ := m.ImageSettings(scope)
	if got.Connections[0].CredentialConfigured {
		t.Fatal("revoked secret remains configured")
	}
	for _, wrong := range []imaging.Scope{{OwnerID: "other", BackendID: "local"}, {OwnerID: "local", BackendID: "other"}, {}} {
		if _, err = m.WriteImageSecret(ctx, wrong, imaging.ImageSecretInput{ConnectionID: c.ID, Purpose: "apiKey", Value: "sentinel"}); err == nil {
			t.Fatal("scope accepted")
		}
		if _, err = m.UpdateImageSettings(ctx, wrong, imaging.ImageSettingsUpdate{}); err == nil {
			t.Fatal("scope accepted")
		}
		if _, err = m.ResolveImageConnection(ctx, wrong, c.ID); err == nil {
			t.Fatal("scope accepted")
		}
	}
}
func TestImageSecretCorruptFileErrorRedaction(t *testing.T) {
	s := NewSettingsStore(config.Default(), nil)
	for _, raw := range []string{`{"version":1,"secrets":{"secret-sentinel":false}}`, `{"version":1,"secret-sentinel":"literal"}`, `not-json-sentinel`} {
		p := filepath.Join(t.TempDir(), "secrets.json")
		if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := NewImageSettingsManager(s, p, ImageSettingsOptions{})
		if err == nil || strings.Contains(err.Error(), "sentinel") {
			t.Fatalf("unsafe parse error: %v", err)
		}
	}
}

func TestImageLegacyVerificationCallbackCannotPublishEvidence(t *testing.T) {
	ctx, scope := context.Background(), imageTestScope()
	path := filepath.Join(t.TempDir(), "config.json")
	s := NewSettingsStore(config.Default(), func(c *config.Config) error { return c.SaveFile(path) })
	calls := 0
	m, err := NewImageSettingsManager(s, filepath.Join(t.TempDir(), "secrets.json"), ImageSettingsOptions{
		VerifyTarget: func(context.Context, imaging.Scope, imaging.TargetSnapshot) (imaging.TargetVerification, error) {
			calls++
			return imageTestTarget().Verification, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	target := imageTestTarget()
	target.Verification = imaging.TargetVerification{}
	if _, err := m.UpdateImageSettings(ctx, scope, imaging.ImageSettingsUpdate{
		Connections: []imaging.ImageConnection{imageTestConnection()},
		Targets:     []imaging.ImageTarget{target},
	}); err != nil {
		t.Fatal(err)
	}
	// Legacy callbacks cannot certify a target. Production verification must
	// execute the durable verification-job lifecycle tested separately.
	for _, revision := range []uint64{1, 999} {
		_, err := m.VerifyImageTarget(ctx, scope, revision, target.ID)
		var imageErr *imaging.ImageError
		if !errors.As(err, &imageErr) || imageErr.Code != imaging.CodeUnsupportedOperation {
			t.Fatalf("legacy verification did not reject the request: %v", err)
		}
	}
	if calls != 0 {
		t.Fatalf("legacy callback executed %d times", calls)
	}
	loaded, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Image.Revision != 1 || loaded.Image.Targets[0].Verification.ContractComplete || loaded.Image.Targets[0].Enabled {
		t.Fatal("legacy callback published verification evidence or enabled the target")
	}
}
func TestImageProviderResolvedSnapshotAndRevocation(t *testing.T) {
	ctx, scope := context.Background(), imageTestScope()
	cfg := config.Default()
	cfg.Providers["image-provider"] = config.Provider{APIKey: "!fake-expression", BaseURL: "https://example.invalid/v1", UseSystemProxy: true, CustomHeaders: []config.CustomHeader{{Key: "X-Private", Value: "!fake-header"}}}
	s := NewSettingsStore(cfg, func(*config.Config) error { return nil })
	var calls int
	m, err := NewImageSettingsManager(s, filepath.Join(t.TempDir(), "secrets.json"), ImageSettingsOptions{ResolveProviderSecret: func(_ context.Context, expression string) (string, error) {
		calls++
		return "resolved-" + expression, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	c := imageTestConnection()
	c.Binding = imaging.ConnectionBinding{Kind: "provider_ref", ProviderID: "image-provider"}
	if _, err = m.UpdateImageSettings(ctx, scope, imaging.ImageSettingsUpdate{Connections: []imaging.ImageConnection{c}}); err != nil {
		t.Fatal(err)
	}
	resolved, err := m.ResolveImageConnection(ctx, scope, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.BaseURL != cfg.Providers["image-provider"].BaseURL || !resolved.UseSystemProxy || resolved.CustomHeaders["X-Private"] != "resolved-!fake-header" || calls != 2 {
		t.Fatal("provider network/secret binding lost")
	}
	// The desktop app's proxy reaches the backend only through netproxy, as it does for chat.
	got, ok := resolved.HTTPClient.Transport.(*http.Transport)
	if !ok || reflect.ValueOf(got.Proxy).Pointer() != reflect.ValueOf(netproxy.ConnectionTransport(true).Proxy).Pointer() {
		t.Fatal("image traffic bypasses the app proxy")
	}
	if got == netproxy.Transport(true) {
		t.Fatal("closing a connection's idle sockets must not touch the shared chat transport")
	}
	if _, err := json.Marshal(resolved.ResolvedConnection); err == nil {
		t.Fatal("embedded private connection serialized")
	}
	value, err := resolved.SecretResolver.ResolveSecret(ctx, scope, "provider_api_key")
	if err != nil || value != "resolved-!fake-expression" {
		t.Fatal("provider secret not resolved")
	}
	s.mu.Lock()
	p := s.cfg.Providers["image-provider"]
	p.APIKey = ""
	s.cfg.Providers["image-provider"] = p
	s.mu.Unlock()
	if _, err = resolved.SecretResolver.ResolveSecret(ctx, scope, "provider_api_key"); err == nil {
		t.Fatal("provider revocation ignored")
	}
}

func TestImageProviderReferenceAndGetDoesNotResolve(t *testing.T) {
	var calls atomic.Int32
	s := NewSettingsStore(config.Default(), func(*config.Config) error { return nil })
	m, err := NewImageSettingsManager(s, filepath.Join(t.TempDir(), "secrets.json"), ImageSettingsOptions{ResolveProviderSecret: func(context.Context, string) (string, error) {
		calls.Add(1)
		return "", errors.New("private-command-sentinel")
	}})
	if err != nil {
		t.Fatal(err)
	}
	c := imageTestConnection()
	c.Binding = imaging.ConnectionBinding{Kind: "provider_ref", ProviderID: "missing"}
	if _, err = m.UpdateImageSettings(context.Background(), imageTestScope(), imaging.ImageSettingsUpdate{Connections: []imaging.ImageConnection{c}}); err == nil {
		t.Fatal("dangling provider accepted")
	}
	s.mu.Lock()
	p := s.cfg.Providers["demo"]
	p.APIKey = "!private-command-sentinel"
	p.BaseURL = "https://example.invalid"
	s.cfg.Providers["demo"] = p
	s.mu.Unlock()
	c.Binding.ProviderID = "demo"
	if _, err = m.UpdateImageSettings(context.Background(), imageTestScope(), imaging.ImageSettingsUpdate{Connections: []imaging.ImageConnection{c}}); err != nil {
		t.Fatal(err)
	}
	got, err := m.ImageSettings(imageTestScope())
	if err != nil || !got.Connections[0].CredentialConfigured || calls.Load() != 0 {
		t.Fatal("GET resolved credential")
	}
	_, err = m.ResolveImageConnection(context.Background(), imageTestScope(), "ic_test")
	if err == nil || strings.Contains(err.Error(), "sentinel") || calls.Load() != 1 {
		t.Fatalf("resolver failure: %v, calls=%d", err, calls.Load())
	}
}
