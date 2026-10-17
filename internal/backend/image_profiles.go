package backend

import (
	"net/http"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

// ImageProfilesManager exposes the image services derived from the chat
// provider configuration. It is read-only: base URLs, keys and models are
// edited in provider settings, so there is no second place to keep in sync.
type ImageProfilesManager struct {
	Source imaging.ProfileSource
}

func NewProviderImageProfiles(source ProviderImageSource) *ImageProfilesManager {
	return &ImageProfilesManager{Source: source}
}

// publicProfile is the wire shape: never a secret value.
type publicProfile struct {
	imaging.ImageProfile
	Targets []publicProfileTarget `json:"targets"`
}

type publicProfileTarget struct {
	ID           string                     `json:"id"`
	Model        string                     `json:"model"`
	Adapter      string                     `json:"adapter"`
	Capabilities imaging.TargetCapabilities `json:"capabilities"`
	// Parameters declares the native keys this model accepts; the workbench
	// greys out the unified options that are missing here.
	Parameters []imaging.ParameterSpec `json:"parameters"`
	Ready      bool                    `json:"ready"`
}

func (m *ImageProfilesManager) List() map[string]any {
	set := m.Source.Current()
	profiles := make([]publicProfile, 0, len(set.Profiles))
	for _, p := range set.Profiles {
		p.Secret = nil
		targets := make([]publicProfileTarget, 0, len(p.Models))
		for _, model := range p.Models {
			adapter, caps, err := imaging.DescribeProfileTarget(p, model)
			params := imaging.TargetParameters(p, model)
			if params == nil {
				params = []imaging.ParameterSpec{}
			}
			if caps.Operations == nil {
				caps.Operations = []imaging.Operation{}
			}
			if caps.OutputFormats == nil {
				caps.OutputFormats = []string{}
			}
			targets = append(targets, publicProfileTarget{
				ID: imaging.ProfileTargetID(p.ID, model), Model: model, Adapter: adapter,
				Capabilities: caps, Parameters: params, Ready: err == nil && p.Enabled && p.APIKeyConfigured,
			})
		}
		profiles = append(profiles, publicProfile{ImageProfile: p, Targets: targets})
	}
	return map[string]any{"revision": set.Revision, "profiles": profiles}
}

func (m *ImageProfilesManager) routes(w http.ResponseWriter, r *http.Request, _ imaging.Scope, parts []string) {
	if len(parts) != 1 {
		imageHTTPError(w, r, imageError(imaging.CodeNotFound))
		return
	}
	if !imageMethod(w, r, http.MethodGet) {
		return
	}
	imageJSON(w, r, 200, m.List())
}

func (m *ImageProfilesManager) Platforms() []imaging.Platform { return imaging.Platforms() }