package ai

import (
	"encoding/json"
	"strings"
	"time"
)

type Message struct {
	Role       string        `json:"role"`
	Content    string        `json:"content"`
	Parts      []ContentPart `json:"-"`
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`

	Name string `json:"name,omitempty"`

	Authored bool `json:"authored,omitempty"`

	SentAt *time.Time `json:"sent_at,omitempty"`

	Usage *Usage `json:"usage,omitempty"`

	Model string `json:"model,omitempty"`

	StopReason    StopReason `json:"stop_reason,omitempty"`
	RawStopReason string     `json:"raw_stop_reason,omitempty"`

	RewoundFrom string `json:"rewound_from,omitempty"`
}

type ContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
	W int `json:"w,omitempty"`
	H int `json:"h,omitempty"`
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
	Role          string     `json:"role"`
	Content       any        `json:"content"`
	ToolCalls     []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID    string     `json:"tool_call_id,omitempty"`
	Name          string     `json:"name,omitempty"`
	Authored      bool       `json:"authored,omitempty"`
	SentAt        *time.Time `json:"sent_at,omitempty"`
	Usage         *Usage     `json:"usage,omitempty"`
	Model         string     `json:"model,omitempty"`
	StopReason    StopReason `json:"stop_reason,omitempty"`
	RawStopReason string     `json:"raw_stop_reason,omitempty"`
	RewoundFrom   string     `json:"rewound_from,omitempty"`
}

func (m Message) MarshalJSON() ([]byte, error) {
	w := messageWire{
		Role: m.Role, Content: m.Content, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID,
		Name: m.Name, Authored: m.Authored, SentAt: m.SentAt, Usage: m.Usage,
		Model: m.Model, RewoundFrom: m.RewoundFrom,
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
	*m = Message{}
	m.Role, m.ToolCalls, m.ToolCallID, m.Name = raw.Role, raw.ToolCalls, raw.ToolCallID, raw.Name
	m.Authored, m.SentAt, m.Usage, m.Model, m.RewoundFrom = raw.Authored, raw.SentAt, raw.Usage, raw.Model, raw.RewoundFrom
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
