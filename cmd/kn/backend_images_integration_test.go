package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/backend"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/imaging"
	"github.com/Stack-Cairn/K-brain/internal/imaging/adapters/direct"
)

func TestImageRuntimeNativeAdapterToDurableAsset(t *testing.T) {
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/images/generations" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer fake-image-key" || r.Header.Get("X-Image-Test") != "inherited" {
			t.Error("image request did not use the provider credential and headers")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(pngBytes.Bytes())}}})
	}))
	defer upstream.Close()
	cfg := config.Default()
	cfg.Providers["image-test"] = config.Provider{Name: "Test", API: "openai-completions", BaseURL: upstream.URL + "/v1", APIKey: "fake-image-key", ActiveModels: []string{"chat-model", "gpt-image-2"}, CustomHeaders: []config.CustomHeader{{Key: "X-Image-Test", Value: "inherited"}}}
	// This evidence belongs only to the synthetic fixture, never a production target.
	cfg.Image = &imaging.ImageSettings{Revision: 1, Connections: []imaging.ImageConnection{{ID: "ic_test", Name: "test", Vendor: "openai", Enabled: true, MaxConcurrent: 2, Binding: imaging.ConnectionBinding{Kind: "provider_ref", ProviderID: "image-test"}}}, Targets: []imaging.ImageTarget{{ID: "it_test", Name: "test", ConnectionID: "ic_test", Adapter: direct.OpenAIID, AdapterVersion: direct.Version, Kind: "model", RemoteID: "gpt-image-2", Revision: 1, Enabled: true, Capabilities: imaging.TargetCapabilities{Operations: []imaging.Operation{imaging.OperationGenerate}, MaxCount: 1, OutputFormats: []string{"png"}}, Verification: imaging.TargetVerification{ContractComplete: true, LiveVerifiedAt: time.Now(), ConnectionID: "ic_test", AdapterVersion: direct.Version, TargetRevision: 1}}}}
	runtime, err := openImageRuntime(context.Background(), t.TempDir(), backend.NewSettingsStore(cfg, func(*config.Config) error { return nil }), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := runtime.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	scope := imaging.Scope{OwnerID: "local", BackendID: "local"}
	project, err := runtime.store.CreateProject(context.Background(), scope, "native fixture", "")
	if err != nil {
		t.Fatal(err)
	}
	// The chat provider is the image service; only its image models are offered.
	profiles := runtime.module.Profiles.Source.Current().Profiles
	if len(profiles) != 1 || profiles[0].Platform != imaging.PlatformOpenAI || len(profiles[0].Models) != 1 || profiles[0].Models[0] != "gpt-image-2" {
		t.Fatalf("provider not exposed as an image service: %+v", profiles)
	}
	target := imaging.ProfileTargetID(profiles[0].ID, "gpt-image-2")
	request := imaging.CreateJobRequest{ProjectID: project.ID, TargetID: target, Operation: imaging.OperationGenerate, Prompt: "synthetic test", Count: 1}
	job, replay, err := runtime.module.Jobs.CreateJob(context.Background(), scope, "fixture-key", request)
	if err != nil || replay {
		t.Fatalf("accept: replay=%v err=%v", replay, err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for !imaging.IsTerminal(job.Status) {
		select {
		case <-deadline.C:
			t.Fatal("synthetic adapter did not finish")
		case <-tick.C:
			job, err = runtime.module.Jobs.GetJob(context.Background(), scope, job.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if job.Status != imaging.JobSucceeded || len(job.AssetIDs) != 1 || job.Usage != nil {
		t.Fatalf("unexpected durable outcome: status=%s assets=%d", job.Status, len(job.AssetIDs))
	}
	r, asset, err := runtime.assets.OpenAsset(context.Background(), scope, job.AssetIDs[0], "original")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(r)
	r.Close()
	if err != nil || !bytes.Equal(raw, pngBytes.Bytes()) || asset.JobID != job.ID {
		t.Fatal("saved original or ancestry differs")
	}
	_, replay, err = runtime.module.Jobs.CreateJob(context.Background(), scope, "fixture-key", request)
	if err != nil || !replay || calls.Load() != 1 {
		t.Fatal("durable replay resubmitted upstream")
	}
}

func TestImageRuntimeGoogleProviderAuthentication(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["google-test"] = config.Provider{API: "google-generative-ai", BaseURL: "https://example.invalid/v1beta", APIKey: "fake-image-key"}
	cfg.Image = &imaging.ImageSettings{Connections: []imaging.ImageConnection{{ID: "ic_google", Name: "google", Enabled: true, MaxConcurrent: 1, Binding: imaging.ConnectionBinding{Kind: "provider_ref", ProviderID: "google-test"}}}}
	runtime, err := openImageRuntime(context.Background(), t.TempDir(), backend.NewSettingsStore(cfg, func(*config.Config) error { return nil }), true)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	scope := imaging.Scope{OwnerID: "local", BackendID: "local"}
	resolved, err := runtime.module.Settings.ResolveImageConnection(context.Background(), scope, "ic_google")
	if err != nil {
		t.Fatal(err)
	}
	defer resolved.HTTPClient.CloseIdleConnections()
	if resolved.AuthKind != "api_key_header" {
		t.Fatal("Google provider lost its native authentication scheme")
	}
	if _, err := resolveImageAdapter(context.Background(), runtime.module.Settings, scope, imaging.TargetSnapshot{ConnectionID: "ic_google", Adapter: direct.OpenAIID, AdapterVersion: direct.Version}); err == nil {
		t.Fatal("incompatible authentication accepted")
	}
}
