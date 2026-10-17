package imaging

import (
	"sort"
	"testing"
)

// Every key NativeParameters can emit must be declared by profileParameters, or
// ValidateRequest rejects the job as "unknown parameter".
func TestNativeParametersAreDeclared(t *testing.T) {
	comp := 80
	full := GenerationParams{Size: "1536x1024", Quality: "high", OutputFormat: "webp", OutputCompression: &comp, Moderation: "low", N: 1, TransparentOutput: true}
	for _, platform := range Platforms() {
		applied, _ := NormalizeParams(platform, full)
		native := NativeParameters(platform.ID, applied)
		declared := map[string]bool{}
		for _, spec := range profileParameters(platform) {
			declared[spec.Key] = true
		}
		var keys []string
		for k := range native {
			keys = append(keys, k)
			if !declared[k] {
				t.Errorf("%s emits undeclared parameter %q", platform.ID, k)
			}
		}
		sort.Strings(keys)
		t.Logf("%s -> %v", platform.ID, keys)
	}
}

func TestProfileTargetIDIsEngineValid(t *testing.T) {
	for _, model := range []string{"gpt-image-2", "models/gemini x", "doubao-seedream-5-0-pro-260628", string(make([]byte, 400))} {
		id := ProfileTargetID("ip_0123456789abcdef", model)
		if !validID(id, "it_") {
			t.Fatalf("invalid target id %q for %q", id, model)
		}
		if id != ProfileTargetID("ip_0123456789abcdef", model) {
			t.Fatal("target id not stable")
		}
	}
	if ProfileTargetID("ip_a", "m") == ProfileTargetID("ip_b", "m") {
		t.Fatal("profiles collide")
	}
}

func TestFindProfileTarget(t *testing.T) {
	set := ImageProfileSet{Profiles: []ImageProfile{{ID: "ip_0123456789abcdef", Models: []string{"a", "b"}}}}
	p, model, ok := FindProfileTarget(set, ProfileTargetID("ip_0123456789abcdef", "b"))
	if !ok || p.ID != "ip_0123456789abcdef" || model != "b" {
		t.Fatalf("lookup failed: %v %q %v", p.ID, model, ok)
	}
	if _, _, ok := FindProfileTarget(set, ProfileTargetID("ip_0123456789abcdef", "c")); ok {
		t.Fatal("unknown model resolved")
	}
}

type staticProfiles ImageProfileSet

func (s staticProfiles) Current() ImageProfileSet { return ImageProfileSet(s) }

type noCredentials struct{}

func (noCredentials) Configured(string) bool              { return true }
func (noCredentials) ResolverFor(string) SecretResolver   { return nil }
func (noCredentials) HeadersFor(string) map[string]string { return nil }

func TestProfileRegistrySnapshotValidates(t *testing.T) {
	for _, platform := range Platforms() {
		id := "ip_" + (platform.ID + "0000000000000000")[:16]
		set := ImageProfileSet{Profiles: []ImageProfile{{ID: id, Name: platform.Name, Kind: "builtin", Platform: platform.ID, BaseURL: platform.DefaultBaseURL, Enabled: true, Models: platform.DefaultModels}}}
		reg := ProfileRegistry{Store: staticProfiles(set), Secrets: noCredentials{}}
		target := ProfileTargetID(id, platform.DefaultModels[0])
		snap, err := reg.ResolveTarget(nil, Scope{OwnerID: "local", BackendID: "local"}, target)
		if err != nil {
			t.Fatalf("%s: %v", platform.ID, err)
		}
		applied, _ := NormalizeParams(platform, GenerationParams{Size: "1024x1024", Quality: "high", OutputFormat: "png", N: 1})
		req := CreateJobRequest{ProjectID: "ip_project", TargetID: target, Operation: OperationGenerate, Prompt: "cat", Count: 1, Parameters: NativeParameters(platform.ID, applied)}
		if err := ValidateRequest(req, snap); err != nil {
			t.Fatalf("%s: request rejected: %v (params %v)", platform.ID, err, req.Parameters)
		}
	}
}
