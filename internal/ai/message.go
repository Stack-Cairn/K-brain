package ai

import (
	"encoding/json"
	"strings"
	"time"
)

type Message struct {
	ID      string        `json:"id,omitempty"`
	Role    string        `json:"role"`
	Content string        `json:"content"`
	Parts   []ContentPart `json:"-"`
	// Reasoning is display-only; signed provider replay state is separate.
	Reasoning    string          `json:"reasoning,omitempty"`
	Replay       *ProviderReplay `json:"-"`
	ToolCalls    []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID   string          `json:"tool_call_id,omitempty"`
	HostedSearch []HostedSearch  `json:"-"`

	Name string `json:"name,omitempty"`

	Authored bool `json:"authored,omitempty"`
	// Origin marks a user-role message the user did not write; see OriginBackground.
	Origin string `json:"origin,omitempty"`

	SentAt *time.Time `json:"sent_at,omitempty"`

	// PromptContext is a persisted request-only snapshot, not user-authored text.
	PromptContext   string           `json:"prompt_context,omitempty"`
	PromptSnapshots []PromptSnapshot `json:"prompt_snapshots,omitempty"`

	Usage *Usage `json:"usage,omitempty"`

	Model string `json:"model,omitempty"`

	StopReason    StopReason `json:"stop_reason,omitempty"`
	RawStopReason string     `json:"raw_stop_reason,omitempty"`

	RewoundFrom string `json:"rewound_from,omitempty"`
}

// OriginBackground marks a notice the runtime delivered on its own as a user-role
// message: a wait result, a managed process exit, a background subagent or workflow
// report, a goal continuation round, the new-context notice or the output-limit
// continuation reminder. Clients show it as a notice rather than as something the
// user typed.
const OriginBackground = "background"

// PromptSnapshot records the source and rendered value of backend-owned context.
type PromptSnapshot struct {
	Source string `json:"source"`
	Text   string `json:"text"`
}

// ProviderReplay is backend-owned state, scoped to the originating endpoint and model.
type ProviderReplay struct {
	API      string            `json:"api"`
	Endpoint string            `json:"endpoint"`
	Model    string            `json:"model"`
	Blocks   []json.RawMessage `json:"blocks"`
}

type ContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
	FileURL *struct {
		URL      string `json:"url"`
		Filename string `json:"filename,omitempty"`
	} `json:"file_url,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	W        int    `json:"w,omitempty"`
	H        int    `json:"h,omitempty"`
}

func (m Message) TextContent() string {
	var b strings.Builder
	b.WriteString(m.Content)
	for _, p := range m.Parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func (m Message) ContentParts() []ContentPart {
	parts := make([]ContentPart, 0, len(m.Parts)+1)
	if m.Content != "" {
		parts = append(parts, ContentPart{Type: "text", Text: m.Content})
	}
	return append(parts, m.Parts...)
}

type messageWire struct {
	ID              string           `json:"id,omitempty"`
	Role            string           `json:"role"`
	Content         any              `json:"content"`
	Reasoning       string           `json:"reasoning,omitempty"`
	Replay          *ProviderReplay  `json:"provider_replay,omitempty"`
	ToolCalls       []ToolCall       `json:"tool_calls,omitempty"`
	ToolCallID      string           `json:"tool_call_id,omitempty"`
	HostedSearch    []HostedSearch   `json:"hosted_search,omitempty"`
	Name            string           `json:"name,omitempty"`
	Authored        bool             `json:"authored,omitempty"`
	Origin          string           `json:"origin,omitempty"`
	SentAt          *time.Time       `json:"sent_at,omitempty"`
	PromptContext   string           `json:"prompt_context,omitempty"`
	PromptSnapshots []PromptSnapshot `json:"prompt_snapshots,omitempty"`
	Usage           *Usage           `json:"usage,omitempty"`
	Model           string           `json:"model,omitempty"`
	StopReason      StopReason       `json:"stop_reason,omitempty"`
	RawStopReason   string           `json:"raw_stop_reason,omitempty"`
	RewoundFrom     string           `json:"rewound_from,omitempty"`
}

func (m Message) MarshalJSON() ([]byte, error) {
	w := messageWire{
		ID: m.ID, Role: m.Role, Content: m.Content, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID,
		Reasoning: m.Reasoning,
		Replay:    m.Replay,
		Name:      m.Name, Authored: m.Authored, Origin: m.Origin, SentAt: m.SentAt, Usage: m.Usage,
		PromptContext:   m.PromptContext,
		PromptSnapshots: m.PromptSnapshots,
		HostedSearch:    m.HostedSearch,
		Model:           m.Model, RewoundFrom: m.RewoundFrom,
		StopReason: m.StopReason, RawStopReason: m.RawStopReason,
	}
	if len(m.Parts) > 0 {
		w.Content = m.ContentParts()
	}
	return json.Marshal(w)
}

func (m *Message) UnmarshalJSON(data []byte) error {
	var raw struct {
		messageWire
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*m = Message{ID: raw.ID, Reasoning: raw.Reasoning, Replay: raw.Replay}
	m.Role, m.ToolCalls, m.ToolCallID, m.Name = raw.Role, raw.ToolCalls, raw.ToolCallID, raw.Name
	m.Authored, m.Origin, m.SentAt, m.Usage, m.Model, m.RewoundFrom = raw.Authored, raw.Origin, raw.SentAt, raw.Usage, raw.Model, raw.RewoundFrom
	m.PromptContext = raw.PromptContext
	m.PromptSnapshots = raw.PromptSnapshots
	m.StopReason, m.RawStopReason = raw.StopReason, raw.RawStopReason
	if len(raw.Content) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw.Content, &s); err == nil {
		m.Content = s
		return nil
	}
	var parts []ContentPart
	if err := json.Unmarshal(raw.Content, &parts); err != nil {
		return err
	}
	if len(parts) > 0 && parts[0].Type == "text" && parts[0].Text != "" {
		m.Content = parts[0].Text
		parts = parts[1:]
	}
	m.Parts = parts
	return nil
}
