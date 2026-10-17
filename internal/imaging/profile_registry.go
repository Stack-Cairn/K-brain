package imaging

import (
	"context"
	"strings"
)

// Target IDs are derived from (profile, model); see ProfileTargetID. The
// workbench never learns adapter names, so the registry derives them from the platform.

// ProfileRegistry adapts the profile document to the engine's Registry, so the
// scheduler, adapters and job service stay unchanged.
type ProfileRegistry struct {
	Store   ProfileSource
	Secrets ProfileCredentials
}

func (r ProfileRegistry) snapshotFor(scope Scope, targetID string) (TargetSnapshot, error) {
	if err := checkProfileScope(scope); err != nil {
		return TargetSnapshot{}, err
	}
	set := r.Store.Current()
	profile, model, ok := FindProfileTarget(set, targetID)
	if !ok {
		return TargetSnapshot{}, &ImageError{Code: CodeNotFound}
	}
	if !profile.Enabled {
		return TargetSnapshot{}, &ImageError{Code: CodeForbidden}
	}
	adapter, capabilities, parameters, err := describeProfile(profile, model)
	if err != nil {
		return TargetSnapshot{}, err
	}
	return TargetSnapshot{
		ID:           targetID,
		Name:         profile.Name + " · " + model,
		ConnectionID: profile.ID,
		Adapter:      adapter,
		// Adapters reject a mismatched version, and the profile layer is the only
		// producer now, so the current version is always correct.
		AdapterVersion: currentAdapterVersion(adapter),
		Kind:           "model",
		RemoteID:       remoteTarget(model, adapter),
		Revision:       set.Revision,
		Capabilities:   capabilities,
		Parameters:     parameters,
	}, nil
}

func (r ProfileRegistry) ResolveTarget(ctx context.Context, scope Scope, targetID string) (TargetSnapshot, error) {
	_ = ctx
	return r.snapshotFor(scope, targetID)
}

func (r ProfileRegistry) WithImageTarget(ctx context.Context, scope Scope, targetID string, accept func(TargetSnapshot) error) error {
	_ = ctx
	snapshot, err := r.snapshotFor(scope, targetID)
	if err != nil {
		return err
	}
	return accept(snapshot)
}

// FindProfileTarget maps an engine target id back to its profile and model.
// Every (profile, model) pair hashes to one stable it_ id, so a lookup is a scan
// of the small profile document rather than a parse of user-controlled text.
func FindProfileTarget(set ImageProfileSet, targetID string) (ImageProfile, string, bool) {
	for _, p := range set.Profiles {
		for _, m := range p.Models {
			if ProfileTargetID(p.ID, m) == targetID {
				return p, m, true
			}
		}
	}
	return ImageProfile{}, "", false
}

func checkProfileScope(scope Scope) error {
	if scope.OwnerID != "local" || scope.BackendID != "local" {
		return &ImageError{Code: CodeForbidden}
	}
	return nil
}

// describeProfile maps a platform onto the adapter the engine executes.
func describeProfile(p ImageProfile, model string) (string, TargetCapabilities, []ParameterSpec, error) {
	platform, ok := FindPlatform(p.Platform)
	if !ok {
		// A profile can outlive a platform removal only if the file was edited by
		// hand; report it instead of guessing an adapter.
		return "", TargetCapabilities{}, nil, &ImageError{Code: CodeUnsupportedOperation}
	}
	caps := TargetCapabilities{
		Operations:    []Operation{OperationGenerate, OperationReference, OperationEdit},
		MaxInputs:     16,
		MaxCount:      platform.MaxOutputImages,
		OutputFormats: []string{"png"},
	}
	if platform.SupportsOutputFormat && p.Platform != PlatformRollDek {
		caps.OutputFormats = []string{"png", "jpeg", "webp"}
	}
	switch p.Platform {
	case PlatformOpenAI:
		caps.MaxInputs = 16
		caps.Operations = append(caps.Operations, OperationMask)
	case PlatformRollDek:
		// The relay forwards /images/edits like OpenAI; masks are not offered.
		caps.Operations = []Operation{OperationGenerate, OperationReference}
		caps.MaxInputs = 16
	case PlatformVolcengine:
		caps.MaxInputs = 10
	case PlatformGoogle:
		caps.MaxInputs = 14
	}
	if caps.MaxCount < 1 {
		caps.MaxCount = 1
	}
	return adapterForProfile(p), caps, profileParameters(platform), nil
}

func adapterForProfile(p ImageProfile) string {
	if p.Kind == "custom" {
		return CustomAdapterID(p.CustomProviderID)
	}
	switch p.Platform {
	case PlatformOpenAI:
		// A relay is recognised by its host, matching the previous behaviour that
		// the RollDek profile could only be reached through the relay addresses.
		return OpenAIAdapterID
	case PlatformRollDek:
		return RollDekAdapterID
	case PlatformVolcengine:
		return VolcengineAdapterID
	case PlatformGoogle:
		return GoogleAdapterID
	}
	return ""
}

// remoteTarget is what the adapter sends as the model: Seedream also accepts
// endpoint IDs, which are passed through unchanged.
func remoteTarget(model, adapter string) string {
	return strings.TrimSpace(model)
}

// currentAdapterVersion reports the version the built adapter expects. It lives
// here rather than in the adapters package to keep this file free of imports.
func currentAdapterVersion(adapter string) string {
	return AdapterContractVersion
}

// AdapterContractVersion must keep matching every built adapter's Version. The
// value is asserted by the cmd/kn test so a bump cannot slip through.
var AdapterContractVersion = "1"

// Adapter IDs the profile layer produces. They match the adapters package; the
// imaging package cannot import it, so cmd/kn asserts they stay equal.
const (
	OpenAIAdapterID     = "openai-images"
	RollDekAdapterID    = "rolldek-images"
	VolcengineAdapterID = "volcengine-ark-image"
	GoogleAdapterID     = "google-generate-content-image"
)

// CustomAdapterID names the adapter used for custom providers.
func CustomAdapterID(id string) string {
	if strings.TrimSpace(id) == "" {
		return "custom-provider"
	}
	return "custom:" + id
}

// ImageProfileSetRevision exposes the revision for callers building the public view.
func (r ProfileRegistry) Revision() uint64 { return r.Store.Current().Revision }

// DescribeProfileTarget exposes the adapter and capabilities of one profile
// model, so the profile view can tell the workbench what to grey out.
func DescribeProfileTarget(p ImageProfile, model string) (string, TargetCapabilities, error) {
	adapter, caps, _, err := describeProfile(p, model)
	return adapter, caps, err
}

// TargetParameters exposes the parameter specs the workbench greys out against.
func TargetParameters(p ImageProfile, model string) []ParameterSpec {
	_, _, params, err := describeProfile(p, model)
	if err != nil {
		return nil
	}
	return params
}
