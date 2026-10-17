package imaging

// Platform metadata drives the settings form: which fields apply, which are
// hidden, and which defaults to prefill. It is static and carries no secrets.
type Platform struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // builtin | custom
	// DefaultBaseURL prefills the address field; the user may override it.
	DefaultBaseURL string `json:"defaultBaseUrl"`
	// BaseURLs lists additional recognized addresses when the platform is
	// host-restricted (for example a relay with fixed endpoints).
	BaseURLs      []string `json:"baseUrls,omitempty"`
	DefaultModels []string `json:"defaultModels"`
	// SupportsModelListing enables the "fetch models" button.
	SupportsModelListing bool `json:"supportsModelListing"`
	// SupportsAPIMode shows the Images/Responses selector.
	SupportsAPIMode   bool `json:"supportsApiMode"`
	SupportsStreaming bool `json:"supportsStreaming"`
	// APIModes lists the selectable values when SupportsAPIMode is true.
	APIModes   []string `json:"apiModes,omitempty"`
	SecretKind string   `json:"secretKind"` // apiKey | akSk
	// APIKeyPlaceholder is shown in the key field.
	APIKeyPlaceholder string `json:"apiKeyPlaceholder,omitempty"`
	// MaxOutputImages bounds the count field.
	MaxOutputImages int `json:"maxOutputImages"`
	// Capabilities the parameter bar greys out.
	SupportsQuality               bool `json:"supportsQuality"`
	SupportsModeration            bool `json:"supportsModeration"`
	SupportsOutputFormat          bool `json:"supportsOutputFormat"`
	SupportsCompression           bool `json:"supportsCompression"`
	SupportsTransparentBackground bool `json:"supportsTransparentBackground"`
	// Hints are short Chinese notes shown under each field.
	Hints map[string]string `json:"hints,omitempty"`
}

// PlatformIDs are the built-in platforms this build executes.
const (
	PlatformOpenAI     = "openai"
	PlatformVolcengine = "volcengine"
	PlatformGoogle     = "google"
	PlatformRollDek    = "rolldek"
)

// Platforms returns the built-in catalog. Custom providers are appended by the
// caller from the stored definitions.
func Platforms() []Platform {
	return []Platform{
		{
			ID: PlatformOpenAI, Name: "OpenAI 兼容", Kind: "builtin",
			DefaultBaseURL:       "https://api.openai.com/v1",
			DefaultModels:        []string{"gpt-image-2.5-sunburst", "gpt-image-2"},
			SupportsModelListing: true, SupportsAPIMode: true, APIModes: []string{"images", "responses"},
			SupportsStreaming: true, SecretKind: "apiKey", APIKeyPlaceholder: "sk-...",
			MaxOutputImages: 10, SupportsQuality: true, SupportsModeration: true,
			SupportsOutputFormat: true, SupportsCompression: true, SupportsTransparentBackground: true,
			Hints: map[string]string{
				"baseUrl": "末尾带 / 时直接拼接接口路径，不再补 /v1。支持中转地址。",
				"model":   "多个模型用逗号分隔；可点“拉取模型”从服务商获取候选。",
			},
		},
		{
			ID: PlatformVolcengine, Name: "火山方舟", Kind: "builtin",
			DefaultBaseURL: "https://ark.cn-beijing.volces.com/api/v3",
			DefaultModels:  []string{"doubao-seedream-5-0-pro-260628"},
			SecretKind:     "apiKey", APIKeyPlaceholder: "ARK_API_KEY",
			MaxOutputImages: 1, SupportsQuality: false, SupportsModeration: false,
			SupportsOutputFormat: true, SupportsCompression: false, SupportsTransparentBackground: false,
			Hints: map[string]string{
				"model":   "可填模型 ID，也可填推理接入点 ep-*。",
				"quality": "火山方舟不支持质量参数，此处固定为默认值。",
			},
		},
		{
			ID: PlatformGoogle, Name: "Google Gemini", Kind: "builtin",
			DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta",
			DefaultModels:  []string{"gemini-nano-banana-2.1"},
			SecretKind:     "apiKey", APIKeyPlaceholder: "AIza...",
			MaxOutputImages: 1, SupportsQuality: false, SupportsModeration: false,
			SupportsOutputFormat: false, SupportsCompression: false, SupportsTransparentBackground: false,
			Hints: map[string]string{
				"model": "Gemini 图像模型；比例与分辨率通过 generationConfig 传递。",
			},
		},
		{
			ID: PlatformRollDek, Name: "RollDek", Kind: "builtin",
			DefaultBaseURL:       "https://api-cn.rolldek.com/v1",
			BaseURLs:             []string{"https://api-cn.rolldek.com/v1", "https://rolldek.com/v1"},
			DefaultModels:        []string{"gpt-image-2.5", "gpt-image-2"},
			SupportsModelListing: true, SecretKind: "apiKey", APIKeyPlaceholder: "sk-...",
			MaxOutputImages: 4, SupportsQuality: false, SupportsModeration: false,
			SupportsOutputFormat: true, SupportsCompression: false, SupportsTransparentBackground: false,
			Hints: map[string]string{
				"baseUrl": "仅支持 API key 认证的两个官方地址。",
				"quality": "当前已验证的 RollDek 请求不带质量参数。",
			},
		},
	}
}

func FindPlatform(id string) (Platform, bool) {
	for _, p := range Platforms() {
		if p.ID == id {
			return p, true
		}
	}
	return Platform{}, false
}
