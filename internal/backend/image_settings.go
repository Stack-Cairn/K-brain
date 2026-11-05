package backend

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/imaging"
	"github.com/Stack-Cairn/K-brain/internal/netproxy"
)

// ImageDependencyChecker is called under SettingsStore.mu. The job service must
// serialize job acceptance with this check and must not re-enter SettingsStore.
// Include queued, running, saving, cancelling and unresolved accepted jobs.
type ImageDependencyChecker interface {
	ImageDependenciesInUse(context.Context, imaging.Scope, []string, []string) (bool, error)
}
type ImageSettingsOptions struct {
	Dependencies ImageDependencyChecker
	// Only trusted provider configuration is passed to this resolver. HTTP secret
	// input is always stored literally and never reaches this function.
	ResolveProviderSecret func(context.Context, string) (string, error)
	// Deprecated: ignored. Only durable successful verification jobs may issue evidence.
	VerifyTarget func(context.Context, imaging.Scope, imaging.TargetSnapshot) (imaging.TargetVerification, error)
}

// ImageSettingsManager shares the settings store's one configuration lock.
// Construct one manager per store, with an explicit private secret file path.
// Only {OwnerID:"local", BackendID:"local"} is supported until trusted owner
// delegation and per-owner settings storage are implemented.
type ImageSettingsManager struct {
	store                 *SettingsStore
	secretPath            string
	secrets               map[string]imageSecretRecord
	dependencies          ImageDependencyChecker
	resolveProviderSecret func(context.Context, string) (string, error)
	verifications         map[string]imageVerificationRecord
}
type imageSecretRecord struct {
	Generation   uint64 `json:"generation,omitempty"`
	OwnerID      string `json:"ownerId"`
	BackendID    string `json:"backendId"`
	ConnectionID string `json:"connectionId"`
	Purpose      string `json:"purpose"`
	Value        string `json:"value"`
}
type imageSecretFile struct {
	Verifications map[string]imageVerificationRecord `json:"verifications,omitempty"`
	Version       int                                `json:"version"`
	Secrets       map[string]imageSecretRecord       `json:"secrets"`
}

var imageSecretPurpose = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
var imageSecretRef = regexp.MustCompile(`^is_[a-f0-9]{32}$`)

func imageError(code string) error { return &imaging.ImageError{Code: code} }
func checkImageScope(scope imaging.Scope) error {
	if scope.OwnerID != "local" || scope.BackendID != "local" {
		return imageError(imaging.CodeForbidden)
	}
	return nil
}
func NewImageSettingsManager(store *SettingsStore, secretPath string, opts ImageSettingsOptions) (*ImageSettingsManager, error) {
	if store == nil || secretPath == "" || !filepath.IsAbs(secretPath) {
		return nil, imageError(imaging.CodeInvalidInput)
	}
	m := &ImageSettingsManager{store: store, secretPath: filepath.Clean(secretPath), secrets: map[string]imageSecretRecord{}, verifications: map[string]imageVerificationRecord{}, dependencies: opts.Dependencies, resolveProviderSecret: opts.ResolveProviderSecret}
	if m.resolveProviderSecret == nil {
		m.resolveProviderSecret = config.ResolveSecretContext
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	m.pruneEmptyProviderConnectionsLocked()
	raw, err := os.ReadFile(m.secretPath)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, imageError(imaging.CodeUnavailable)
	}
	var disk imageSecretFile
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&disk) != nil || d.Decode(new(any)) != io.EOF || disk.Version != 1 {
		return nil, imageError(imaging.CodeUnavailable)
	}
	for id, record := range disk.Verifications {
		record.Scope = imaging.Scope{OwnerID: record.OwnerID, BackendID: record.BackendID}
		if id != record.ID || !strings.HasPrefix(id, "iv_") || checkImageScope(record.Scope) != nil || record.Input.ExpectedRevision == nil || record.Input.TargetRevision == nil {
			return nil, imageError(imaging.CodeUnavailable)
		}
		m.verifications[id] = record
	}
	for ref, r := range disk.Secrets {
		if !imageSecretRef.MatchString(ref) || checkImageScope(imaging.Scope{OwnerID: r.OwnerID, BackendID: r.BackendID}) != nil || !imageSecretPurpose.MatchString(r.Purpose) || r.ConnectionID == "" {
			return nil, imageError(imaging.CodeUnavailable)
		}
		m.secrets[ref] = r
	}
	return m, nil
}
func (m *ImageSettingsManager) currentLocked() imaging.ImageSettings {
	if m.store.cfg.Image == nil {
		return imaging.ImageSettings{Connections: []imaging.ImageConnection{}, Targets: []imaging.ImageTarget{}}
	}
	s := imaging.CloneSettings(*m.store.cfg.Image)
	for i := range s.Targets {
		s.Targets[i].VerificationState = nil
	}
	return s
}
func (m *ImageSettingsManager) publicLocked(scope imaging.Scope) imaging.ImageSettings {
	s := m.currentLocked()
	for i := range s.Connections {
		c := &s.Connections[i]
		c.CredentialConfigured = false
		if c.Binding.Kind == "provider_ref" {
			p, ok := m.store.cfg.Providers[c.Binding.ProviderID]
			c.CredentialConfigured = ok && p.APIKey != ""
		} else if len(c.Binding.SecretRefs) > 0 {
			c.CredentialConfigured = true
			for purpose, ref := range c.Binding.SecretRefs {
				r, ok := m.secrets[ref]
				if !ok || !secretOwned(r, scope, c.ID, purpose) || r.Value == "" {
					c.CredentialConfigured = false
				}
			}
		}
	}
	return imaging.PublicSettings(s)
}
func (m *ImageSettingsManager) ImageSettings(scope imaging.Scope) (imaging.ImageSettings, error) {
	if err := checkImageScope(scope); err != nil {
		return imaging.ImageSettings{}, err
	}
	m.store.mu.RLock()
	defer m.store.mu.RUnlock()
	return m.publicLocked(scope), nil
}
func (m *ImageSettingsManager) resolveTargetLocked(id string) (imaging.TargetSnapshot, error) {
	derived, _ := m.providerImageTargetsLocked()
	for _, target := range derived {
		if target.ID == id {
			if !target.Enabled {
				return imaging.TargetSnapshot{}, imageError(imaging.CodeInvalidInput)
			}
			return imaging.SnapshotTarget(target), nil
		}
	}
	s := m.currentLocked()
	if err := imaging.ValidateSettingsWithProviders(s, func(id string) bool { _, ok := m.store.cfg.Providers[id]; return ok }); err != nil {
		return imaging.TargetSnapshot{}, imageError(imaging.CodeInvalidInput)
	}
	for _, target := range s.Targets {
		if target.ID == id {
			if !target.Enabled {
				return imaging.TargetSnapshot{}, imageError(imaging.CodeInvalidInput)
			}
			return imaging.SnapshotTarget(target), nil
		}
	}
	return imaging.TargetSnapshot{}, imageError(imaging.CodeNotFound)
}

// ResolveTarget implements imaging.Registry for trusted local execution.
func (m *ImageSettingsManager) ResolveTarget(ctx context.Context, scope imaging.Scope, id string) (imaging.TargetSnapshot, error) {
	if err := checkImageScope(scope); err != nil {
		return imaging.TargetSnapshot{}, err
	}
	if ctx.Err() != nil {
		return imaging.TargetSnapshot{}, imageError(imaging.CodeUnavailable)
	}
	m.store.mu.RLock()
	defer m.store.mu.RUnlock()
	return m.resolveTargetLocked(id)
}

// WithImageTarget serializes durable job acceptance with configuration deletion.
// The service callback must persist its dependency before returning and must not
// call back into this manager or SettingsStore. Both this callback and the
// dependency checker acquire service locks AFTER the settings lock.
func (m *ImageSettingsManager) WithImageTarget(ctx context.Context, scope imaging.Scope, id string, accept func(imaging.TargetSnapshot) error) error {
	if err := checkImageScope(scope); err != nil {
		return err
	}
	if ctx.Err() != nil || accept == nil {
		return imageError(imaging.CodeInvalidInput)
	}
	m.store.mu.RLock()
	defer m.store.mu.RUnlock()
	target, err := m.resolveTargetLocked(id)
	if err != nil {
		return err
	}
	if err = accept(target); err != nil {
		// Preserve service conflict/not-found/input semantics without forwarding
		// arbitrary causes, messages, request IDs or diagnostic fields to HTTP.
		var imageErr *imaging.ImageError
		if errors.As(err, &imageErr) {
			return imaging.PublicError(&imaging.ImageError{Code: imageErr.Code})
		}
		if errors.Is(err, imaging.ErrUnsupported) {
			return imageError(imaging.CodeUnsupportedOperation)
		}
		if strings.HasPrefix(err.Error(), imaging.CodeInvalidInput+":") {
			return imageError(imaging.CodeInvalidInput)
		}
		return imageError(imaging.CodeUnavailable)
	}
	return nil
}

func (m *ImageSettingsManager) UpdateImageSettings(ctx context.Context, scope imaging.Scope, update imaging.ImageSettingsUpdate) (imaging.ImageSettings, error) {
	for _, target := range update.Targets {
		if target.Source != "" || target.ProviderID != "" || target.ModelID != "" || strings.HasPrefix(target.ID, "it_provider_") {
			return imaging.ImageSettings{}, imageError(imaging.CodeInvalidInput)
		}
	}
	// Derived connections are resolved after legacy ones; a legacy ID in their
	// namespace would shadow a provider binding with a different endpoint/key.
	for _, connection := range update.Connections {
		if strings.HasPrefix(connection.ID, "ic_provider_") {
			return imaging.ImageSettings{}, imageError(imaging.CodeInvalidInput)
		}
	}
	if err := checkImageScope(scope); err != nil {
		return imaging.ImageSettings{}, err
	}
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	if ctx.Err() != nil {
		return imaging.ImageSettings{}, imageError(imaging.CodeUnavailable)
	}
	old := m.currentLocked()
	if update.ExpectedRevision != old.Revision {
		return imaging.ImageSettings{}, imageError(imaging.CodeRevisionConflict)
	}
	if old.Revision >= imaging.MaxSafeInteger {
		return imaging.ImageSettings{}, imageError(imaging.CodeInvalidInput)
	}
	next := imaging.CloneSettings(imaging.ImageSettings{Revision: old.Revision + 1, Connections: update.Connections, Targets: update.Targets})
	oldConnections := map[string]imaging.ImageConnection{}
	newConnections := map[string]imaging.ImageConnection{}
	oldTargets := map[string]imaging.ImageTarget{}
	newTargets := map[string]bool{}
	for _, c := range old.Connections {
		oldConnections[c.ID] = c
	}
	for _, t := range old.Targets {
		oldTargets[t.ID] = t
	}
	for i := range next.Connections {
		c := &next.Connections[i]
		c.CredentialConfigured = false
		// Public reads omit headers. Preserve them only for the same binding.
		if prev, ok := oldConnections[c.ID]; ok && c.Binding.CustomHeaders == nil && sameImageBinding(c.Binding, prev.Binding) {
			c.Binding.CustomHeaders = maps.Clone(prev.Binding.CustomHeaders)
		}
		for purpose, ref := range c.Binding.SecretRefs {
			r, ok := m.secrets[ref]
			if !ok || !secretOwned(r, scope, c.ID, purpose) {
				return imaging.ImageSettings{}, imageError(imaging.CodeInvalidInput)
			}
		}
		newConnections[c.ID] = *c
	}
	for i := range next.Targets {
		t := &next.Targets[i]
		t.Verification = imaging.TargetVerification{}
		t.VerificationState = nil
		// Discard caller-supplied evidence, including trusted-looking Go values.
		// Only an unchanged private execution definition may retain disk evidence.
		if prev, ok := oldTargets[t.ID]; ok && sameImageTarget(*t, prev) && sameImageExecutionBinding(oldConnections[t.ConnectionID].Binding, newConnections[t.ConnectionID].Binding) {
			t.Verification = prev.Verification
		}
		newTargets[t.ID] = true
	}
	var removedConnections, removedTargets []string
	for id := range oldConnections {
		if _, ok := newConnections[id]; !ok {
			removedConnections = append(removedConnections, id)
		}
	}
	for id := range oldTargets {
		if !newTargets[id] {
			removedTargets = append(removedTargets, id)
		}
	}
	for _, t := range next.Targets {
		if _, ok := newConnections[t.ConnectionID]; !ok {
			if _, existed := oldConnections[t.ConnectionID]; existed {
				return imaging.ImageSettings{}, imageError(imaging.CodeConnectionInUse)
			}
		}
	}
	if len(removedConnections) > 0 || len(removedTargets) > 0 {
		// No service has certified absence of executing dependencies: fail closed.
		if m.dependencies == nil {
			return imaging.ImageSettings{}, imageError(imaging.CodeUnavailable)
		}
		busy, err := m.dependencies.ImageDependenciesInUse(ctx, scope, removedConnections, removedTargets)
		if err != nil {
			return imaging.ImageSettings{}, imageError(imaging.CodeUnavailable)
		}
		if busy {
			return imaging.ImageSettings{}, imageError(imaging.CodeConnectionInUse)
		}
	}
	return m.saveImageSettingsLocked(scope, next)
}

// saveImageSettingsLocked publishes only after the real SettingsStore saver
// succeeds. Callers hold the settings write lock for revision check and save.
func (m *ImageSettingsManager) saveImageSettingsLocked(scope imaging.Scope, next imaging.ImageSettings) (imaging.ImageSettings, error) {
	if err := imaging.ValidateSettingsWithProviders(next, func(id string) bool { _, ok := m.store.cfg.Providers[id]; return ok }); err != nil {
		return imaging.ImageSettings{}, imageError(imaging.CodeInvalidInput)
	}
	candidate := m.store.cfg.Snapshot()
	candidate.Image = &next
	if m.store.save == nil {
		return imaging.ImageSettings{}, imageError(imaging.CodeUnavailable)
	}
	// A saver receives its own snapshot, so it cannot mutate the published value.
	if err := m.store.save(candidate.Snapshot()); err != nil {
		return imaging.ImageSettings{}, imageError(imaging.CodeUnavailable)
	}
	m.store.cfg = candidate
	m.store.revision++
	return m.publicLocked(scope), nil
}

// VerifyImageTarget is retained as a fail-closed compatibility shim. Synchronous
// callbacks cannot certify durable outputs or preserve submission idempotency.
// Use ImageVerificationManager.Start/Get with explicit consent instead.
func (m *ImageSettingsManager) VerifyImageTarget(ctx context.Context, scope imaging.Scope, expectedRevision uint64, targetID string) (imaging.ImageSettings, error) {
	if err := checkImageScope(scope); err != nil {
		return imaging.ImageSettings{}, err
	}
	return imaging.ImageSettings{}, imageError(imaging.CodeUnsupportedOperation)
}

func sameImageBinding(a, b imaging.ConnectionBinding) bool {
	a.SecretRefs = nil
	b.SecretRefs = nil
	a.CustomHeaders = nil
	b.CustomHeaders = nil
	return reflect.DeepEqual(a, b)
}
func sameImageExecutionBinding(a, b imaging.ConnectionBinding) bool {
	return sameImageBinding(a, b) && maps.Equal(a.SecretRefs, b.SecretRefs) && maps.Equal(a.CustomHeaders, b.CustomHeaders)
}
func sameImageTarget(a, b imaging.ImageTarget) bool {
	a.Name = ""
	b.Name = ""
	a.Enabled = false
	b.Enabled = false
	a.DisabledReason = ""
	b.DisabledReason = ""
	a.VerificationState = nil
	b.VerificationState = nil
	a.Verification = imaging.TargetVerification{}
	b.Verification = imaging.TargetVerification{}
	return reflect.DeepEqual(a, b)
}
func secretOwned(r imageSecretRecord, s imaging.Scope, connectionID, purpose string) bool {
	return r.OwnerID == s.OwnerID && r.BackendID == s.BackendID && r.ConnectionID == connectionID && r.Purpose == purpose
}

// WriteImageSecret stores a literal and returns its opaque reference. The caller
// attaches that reference with UpdateImageSettings; no config save spans two
// files. Rewriting a purpose rotates its value; an empty value revokes it.
func (m *ImageSettingsManager) WriteImageSecret(ctx context.Context, scope imaging.Scope, input imaging.ImageSecretInput) (imaging.ImageSecretResult, error) {
	if err := checkImageScope(scope); err != nil {
		return imaging.ImageSecretResult{}, err
	}
	if !imageSecretPurpose.MatchString(input.Purpose) || len(input.Value) > 1024*1024 {
		return imaging.ImageSecretResult{}, imageError(imaging.CodeInvalidInput)
	}
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	if ctx.Err() != nil {
		return imaging.ImageSecretResult{}, imageError(imaging.CodeUnavailable)
	}
	found := false
	for _, c := range m.currentLocked().Connections {
		if c.ID == input.ConnectionID {
			found = c.Binding.Kind == "independent"
			break
		}
	}
	if !found {
		return imaging.ImageSecretResult{}, imageError(imaging.CodeNotFound)
	}
	ref := ""
	for id, r := range m.secrets {
		if secretOwned(r, scope, input.ConnectionID, input.Purpose) {
			ref = id
			break
		}
	}
	if ref == "" {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return imaging.ImageSecretResult{}, imageError(imaging.CodeUnavailable)
		}
		ref = "is_" + hex.EncodeToString(random[:])
	}
	next := maps.Clone(m.secrets)
	next[ref] = imageSecretRecord{Generation: m.secrets[ref].Generation + 1, OwnerID: scope.OwnerID, BackendID: scope.BackendID, ConnectionID: input.ConnectionID, Purpose: input.Purpose, Value: input.Value}
	if err := saveImageSecrets(m.secretPath, imageSecretFile{Version: 1, Secrets: next, Verifications: m.verifications}); err != nil {
		return imaging.ImageSecretResult{}, imageError(imaging.CodeUnavailable)
	}
	m.secrets = next
	// In-flight trusted verification cannot certify credentials that changed
	// while the verifier was running. This also invalidates old settings ETags.
	m.store.revision++
	return imaging.ImageSecretResult{Ref: ref, Configured: input.Value != ""}, nil
}
func saveImageSecrets(path string, disk imageSecretFile) error {
	raw, err := json.Marshal(disk)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".image-secrets-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// ResolvedImageConnection adds the execution endpoint to imaging's private
// injected client/resolver contract. Neither this object nor its embedded
// connection can be serialized into a public response.
type ResolvedImageConnection struct {
	imaging.ResolvedConnection
	BaseURL        string            `json:"-"`
	AuthKind       string            `json:"-"`
	SecretRefs     map[string]string `json:"-"`
	CustomHeaders  map[string]string `json:"-"`
	UseSystemProxy bool              `json:"-"`
}

func (ResolvedImageConnection) MarshalJSON() ([]byte, error) {
	return nil, imageError(imaging.CodeForbidden)
}
func (m *ImageSettingsManager) ResolveImageConnection(ctx context.Context, scope imaging.Scope, id string) (ResolvedImageConnection, error) {
	if err := checkImageScope(scope); err != nil {
		return ResolvedImageConnection{}, err
	}
	if ctx.Err() != nil {
		return ResolvedImageConnection{}, imageError(imaging.CodeUnavailable)
	}
	m.store.mu.RLock()
	var connection imaging.ImageConnection
	found := false
	for _, c := range m.currentLocked().Connections {
		if c.ID == id {
			connection = c
			found = true
			break
		}
	}
	derivedTargets, derivedConnections := m.providerImageTargetsLocked()
	derivedAdapter := ""
	if !found {
		for _, c := range derivedConnections {
			if c.ID == id {
				connection, found = c, true
				break
			}
		}
		for _, target := range derivedTargets {
			if target.ConnectionID == id && target.Enabled {
				derivedAdapter = target.Adapter
				break
			}
		}
	}
	provider := m.store.cfg.Providers[connection.Binding.ProviderID]
	_, providerExists := m.store.cfg.Providers[connection.Binding.ProviderID]
	m.store.mu.RUnlock()
	if !found {
		return ResolvedImageConnection{}, imageError(imaging.CodeNotFound)
	}
	if !connection.Enabled {
		return ResolvedImageConnection{}, imageError(imaging.CodeInvalidInput)
	}
	b := connection.Binding
	var resolver imaging.SecretResolver
	if b.Kind == "provider_ref" {
		if !providerExists {
			return ResolvedImageConnection{}, imageError(imaging.CodeInvalidInput)
		}
		value, err := m.resolveProviderSecret(ctx, provider.APIKey)
		if err != nil || value == "" {
			return ResolvedImageConnection{}, imageError(imaging.CodeAuthRequired)
		}
		b = imaging.ConnectionBinding{Kind: "independent", BaseURL: provider.BaseURL, AuthKind: "bearer", UseSystemProxy: provider.UseSystemProxy, SecretRefs: map[string]string{"apiKey": "provider_api_key"}, CustomHeaders: map[string]string{}}
		if provider.API == "google-generative-ai" {
			b.AuthKind = "api_key_header"
		}
		if derivedAdapter != "" {
			base, ok := providerImageBaseURL(provider, derivedAdapter)
			if !ok {
				return ResolvedImageConnection{}, imageError(imaging.CodeInvalidInput)
			}
			b.BaseURL = base
			b.AuthKind = "bearer"
			if derivedAdapter == "google-generate-content-image" {
				b.AuthKind = "api_key_header"
			}
		}
		for _, h := range provider.CustomHeaders {
			v, err := m.resolveProviderSecret(ctx, h.Value)
			if err != nil {
				return ResolvedImageConnection{}, imageError(imaging.CodeAuthRequired)
			}
			if strings.ContainsAny(v, "\r\n") {
				return ResolvedImageConnection{}, imageError(imaging.CodeInvalidInput)
			}
			b.CustomHeaders[h.Key] = v
		}
		resolver = &imageProviderSecretResolver{manager: m, scope: scope, providerID: connection.Binding.ProviderID, expression: provider.APIKey, value: value, provider: provider}
	} else if b.Kind == "independent" {
		resolver = &imageOwnedSecretResolver{manager: m, scope: scope, connectionID: id, refs: maps.Clone(b.SecretRefs)}
		if len(b.SecretRefs) == 0 {
			return ResolvedImageConnection{}, imageError(imaging.CodeAuthRequired)
		}
		for _, ref := range b.SecretRefs {
			if _, err := resolver.ResolveSecret(ctx, scope, ref); err != nil {
				return ResolvedImageConnection{}, err
			}
		}
	} else {
		return ResolvedImageConnection{}, imageError(imaging.CodeInvalidInput)
	}
	u, err := url.Parse(b.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ResolvedImageConnection{}, imageError(imaging.CodeInvalidInput)
	}
	// netproxy carries the app proxy LiveAgent passes at spawn. A cloned default transport only
	// reads HTTPS_PROXY, so a desktop app's image traffic would silently connect directly.
	client := &http.Client{Transport: netproxy.ConnectionTransport(b.UseSystemProxy), Timeout: 180 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return ResolvedImageConnection{ResolvedConnection: imaging.ResolvedConnection{HTTPClient: client, SecretResolver: resolver, NetworkProfile: imaging.NetworkProfile{AllowedHosts: []string{u.Hostname()}}}, BaseURL: b.BaseURL, AuthKind: b.AuthKind, SecretRefs: maps.Clone(b.SecretRefs), CustomHeaders: maps.Clone(b.CustomHeaders), UseSystemProxy: b.UseSystemProxy}, nil
}

type imageOwnedSecretResolver struct {
	manager      *ImageSettingsManager
	scope        imaging.Scope
	connectionID string
	refs         map[string]string
}

func (r *imageOwnedSecretResolver) ResolveSecret(ctx context.Context, scope imaging.Scope, ref string) (string, error) {
	if scope != r.scope || checkImageScope(scope) != nil {
		return "", imageError(imaging.CodeForbidden)
	}
	if ctx.Err() != nil {
		return "", imageError(imaging.CodeUnavailable)
	}
	r.manager.store.mu.RLock()
	defer r.manager.store.mu.RUnlock()
	record, ok := r.manager.secrets[ref]
	if ok && record.Value != "" {
		for purpose, ownedRef := range r.refs {
			if ref == ownedRef && secretOwned(record, scope, r.connectionID, purpose) {
				return record.Value, nil
			}
		}
	}
	return "", imageError(imaging.CodeAuthRequired)
}

type imageProviderSecretResolver struct {
	manager    *ImageSettingsManager
	providerID string
	expression string
	scope      imaging.Scope
	value      string
	provider   config.Provider
	target     *imaging.TargetSnapshot
}

func (r *imageProviderSecretResolver) ResolveSecret(ctx context.Context, scope imaging.Scope, ref string) (string, error) {
	if scope != r.scope || checkImageScope(scope) != nil {
		return "", imageError(imaging.CodeForbidden)
	}
	if ctx.Err() != nil {
		return "", imageError(imaging.CodeUnavailable)
	}
	if r.target != nil {
		if err := r.manager.CheckProviderImageTarget(ctx, scope, *r.target); err != nil {
			return "", err
		}
	}
	r.manager.store.mu.RLock()
	provider, exists := r.manager.store.cfg.Providers[r.providerID]
	r.manager.store.mu.RUnlock()
	if !exists || provider.APIKey == "" || provider.APIKey != r.expression || !reflect.DeepEqual(providerImageBinding(provider), providerImageBinding(r.provider)) {
		return "", imageError(imaging.CodeAuthRequired)
	}
	if ref != "provider_api_key" || r.value == "" {
		return "", imageError(imaging.CodeAuthRequired)
	}
	return r.value, nil
}

// pruneEmptyProviderConnectionsLocked drops provider_ref connections that no
// target uses. Provider models now reach Image Studio directly, so such a
// connection has no remaining purpose. Connections with targets are kept.
// Best effort: a failed save leaves the old settings and retries next start.
func (m *ImageSettingsManager) pruneEmptyProviderConnectionsLocked() {
	image := m.store.cfg.Image
	if image == nil {
		return
	}
	used := map[string]bool{}
	for _, t := range image.Targets {
		used[t.ConnectionID] = true
	}
	next := imaging.CloneSettings(*image)
	next.Connections = slices.DeleteFunc(next.Connections, func(c imaging.ImageConnection) bool {
		return c.Binding.Kind == "provider_ref" && !used[c.ID]
	})
	if len(next.Connections) == len(image.Connections) || m.store.save == nil {
		return
	}
	next.Revision++
	candidate := m.store.cfg.Snapshot()
	candidate.Image = &next
	if m.store.save(candidate.Snapshot()) != nil {
		return
	}
	m.store.cfg = candidate
	m.store.revision++
}
