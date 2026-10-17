package imaging

import "time"

// ImageProfile is the single configuration entity: one platform, one endpoint,
// one credential and the model IDs the user typed. It replaces the previous
// connection/target split and carries no verification state.
type ImageProfile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // builtin | custom
	Platform string `json:"platform"`
	// CustomProviderID references a CustomProviderDefinition when Kind is custom.
	CustomProviderID string `json:"customProviderId,omitempty"`
	BaseURL          string `json:"baseUrl"`
	APIMode          string `json:"apiMode,omitempty"` // images | responses (OpenAI-compatible only)
	Enabled          bool   `json:"enabled"`
	// Models is the user's editable list; order is preserved and shown as-is.
	Models []string `json:"models"`
	// SelectedModel is the per-profile default the workbench opens with.
	SelectedModel string `json:"selectedModel,omitempty"`
	// APIKeyConfigured reports a stored secret. The key itself never leaves the backend.
	APIKeyConfigured bool `json:"apiKeyConfigured"`
	APIProxy         bool `json:"apiProxy,omitempty"`
	StreamImages     bool `json:"streamImages,omitempty"`
	// StreamPartialImages is 0-3, matching partial_images.
	StreamPartialImages       int    `json:"streamPartialImages,omitempty"`
	TransparentBackgroundMode string `json:"transparentBackgroundMethod,omitempty"` // api | local
	TimeoutSeconds            int    `json:"timeoutSeconds,omitempty"`
	// MaxConcurrent bounds parallel submissions for this profile.
	MaxConcurrent int `json:"maxConcurrent,omitempty"`
	// LastError records the last classified failure so the card can explain it.
	LastError *ProfileError `json:"lastError,omitempty"`

	// ProviderID names the chat provider this service is derived from.
	ProviderID string `json:"providerId,omitempty"`

	// Secret is accepted on write and never serialized back.
	Secret *ProfileSecret `json:"-"`
}

// ProfileSecret holds write-only credentials. Values are stored by reference and
// never returned by any read path.
type ProfileSecret struct {
	APIKey        string `json:"apiKey,omitempty"`
	AccessKey     string `json:"accessKey,omitempty"`
	SecretKey     string `json:"secretKey,omitempty"`
	SecurityToken string `json:"securityToken,omitempty"`
}

// ProfileError is a sanitized last-failure note; it never contains credentials,
// request bodies or upstream message bodies.
type ProfileError struct {
	Code      string    `json:"code"`
	Message   string    `json:"message"`
	At        time.Time `json:"at"`
	RequestID string    `json:"requestId,omitempty"`
}

// ImageProfileInput creates a profile; the secret is written with the config.
type ImageProfileInput struct {
	Name                      string         `json:"name"`
	Kind                      string         `json:"kind"`
	Platform                  string         `json:"platform"`
	CustomProviderID          string         `json:"customProviderId,omitempty"`
	BaseURL                   string         `json:"baseUrl"`
	APIMode                   string         `json:"apiMode,omitempty"`
	Enabled                   *bool          `json:"enabled,omitempty"`
	Models                    []string       `json:"models"`
	SelectedModel             string         `json:"selectedModel,omitempty"`
	APIProxy                  bool           `json:"apiProxy,omitempty"`
	StreamImages              bool           `json:"streamImages,omitempty"`
	StreamPartialImages       int            `json:"streamPartialImages,omitempty"`
	TransparentBackgroundMode string         `json:"transparentBackgroundMethod,omitempty"`
	TimeoutSeconds            int            `json:"timeoutSeconds,omitempty"`
	MaxConcurrent             int            `json:"maxConcurrent,omitempty"`
	Secret                    *ProfileSecret `json:"secret,omitempty"`
}

// ImageProfilePatch updates one profile. Absent fields keep their value.
type ImageProfilePatch struct {
	Name                      *string  `json:"name,omitempty"`
	BaseURL                   *string  `json:"baseUrl,omitempty"`
	APIMode                   *string  `json:"apiMode,omitempty"`
	Enabled                   *bool    `json:"enabled,omitempty"`
	Models                    []string `json:"models,omitempty"`
	SelectedModel             *string  `json:"selectedModel,omitempty"`
	APIProxy                  *bool    `json:"apiProxy,omitempty"`
	StreamImages              *bool    `json:"streamImages,omitempty"`
	StreamPartialImages       *int     `json:"streamPartialImages,omitempty"`
	TransparentBackgroundMode *string  `json:"transparentBackgroundMethod,omitempty"`
	TimeoutSeconds            *int     `json:"timeoutSeconds,omitempty"`
	MaxConcurrent             *int     `json:"maxConcurrent,omitempty"`
	// Secret replaces the stored credential when present.
	Secret *ProfileSecret `json:"secret,omitempty"`
	// ClearSecret removes it (for example when switching to a platform without a key).
	ClearSecret bool `json:"clearSecret,omitempty"`
}

// ImageProfileSet is the derived list of image services.
type ImageProfileSet struct {
	SchemaVersion int            `json:"schemaVersion"`
	Revision      uint64         `json:"revision"`
	Profiles      []ImageProfile `json:"profiles"`
}

// ProfileSnapshot is what the execution layer resolves: no secret values, only
// references, plus the frozen model string.
type ProfileSnapshot struct {
	ID                        string
	Name                      string
	Kind                      string
	Platform                  string
	CustomProviderID          string
	BaseURL                   string
	APIMode                   string
	Model                     string
	MaxConcurrent             int
	StreamImages              bool
	StreamPartialImages       int
	TransparentBackgroundMode string
	TimeoutSeconds            int
}

func (p ImageProfile) public() ImageProfile {
	out := p
	out.Secret = nil
	return out
}

// ProfileSchemaVersion versions the profile wire shape.
const ProfileSchemaVersion = 2
