// Package protocol defines the versioned JSON contract between K-brain and
// clients such as LiveAgent. Provider-specific wire shapes stay in internal/ai.
package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

const (
	Version = "kbrain.agent.v1"

	RoleSystem    = "system"
	RoleDeveloper = "developer"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"

	ContentText       = "text"
	ContentThinking   = "thinking"
	ContentImage      = "image"
	ContentFile       = "file"
	ContentToolCall   = "tool_call"
	ContentToolResult = "tool_result"

	EventRunAccepted         = "run.accepted"
	EventUserMessage         = "user.message.appended"
	EventAssistantMessage    = "assistant.message.created"
	EventTextDelta           = "assistant.text.delta"
	EventThinkingDelta       = "assistant.thinking.delta"
	EventHostedSearch        = "assistant.sources"
	EventToolCall            = "tool.call"
	EventToolResult          = "tool.result"
	EventToolStatus          = "tool.status"
	EventPermissionRequest   = "permission.requested"
	EventPermissionResult    = "permission.resolved"
	EventQuestionRequested   = "question.requested"
	EventQuestionResolved    = "question.resolved"
	EventSubagentStarted     = "subagent.started"
	EventSubagentUpdate      = "subagent.updated"
	EventSubagentCompleted   = "subagent.completed"
	EventSubagentFailed      = "subagent.failed"
	EventUsage               = "usage.updated"
	EventHistoryUpdated      = "history.updated"
	EventCompactionStarted   = "compaction.started"
	EventCompactionCompleted = "compaction.completed"
	EventRunCompleted        = "run.completed"
	EventRunFailed           = "run.failed"
	EventRunCancelled        = "run.cancelled"
	EventHookWarning         = "hook.warning"
)

type ModelRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type SessionSummary struct {
	ID           string    `json:"id"`
	Title        string    `json:"title,omitempty"`
	CWD          string    `json:"cwd,omitempty"`
	Model        ModelRef  `json:"model"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
	Shared       bool      `json:"shared"`
	Pinned       bool      `json:"pinned,omitempty"`
	Archived     bool      `json:"archived,omitempty"`
}

type Session struct {
	SessionSummary
	Revision string     `json:"revision"`
	Messages []Message  `json:"messages"`
	Tasks    []Subagent `json:"tasks,omitempty"`
	LastSeq  int64      `json:"last_seq"`
}

type SessionPage struct {
	Sessions   []SessionSummary `json:"sessions"`
	TotalCount int              `json:"total_count"`
	Next       *PageCursor      `json:"next,omitempty"`
	Version    string           `json:"version"`
}

type PageCursor struct {
	UpdatedAt time.Time `json:"updated_at"`
	ID        string    `json:"id"`
}

type Message struct {
	ID           string         `json:"id,omitempty"`
	Role         string         `json:"role"`
	Content      []ContentBlock `json:"content,omitempty"`
	ToolCalls    []ToolCall     `json:"tool_calls,omitempty"`
	ToolCallID   string         `json:"tool_call_id,omitempty"`
	Name         string         `json:"name,omitempty"`
	Model        string         `json:"model,omitempty"`
	Provider     string         `json:"provider,omitempty"`
	Usage        *Usage         `json:"usage,omitempty"`
	HostedSearch []HostedSearch `json:"hosted_search,omitempty"`
	StopReason   string         `json:"stop_reason,omitempty"`
	CreatedAt    *time.Time     `json:"created_at,omitempty"`
}

type ContentBlock struct {
	Type       string      `json:"type"`
	Text       string      `json:"text,omitempty"`
	ImageURL   string      `json:"image_url,omitempty"`
	FileURL    string      `json:"file_url,omitempty"`
	Filename   string      `json:"filename,omitempty"`
	MimeType   string      `json:"mime_type,omitempty"`
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
}

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolResult struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Output    string `json:"output"`
	Failed    bool   `json:"failed,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
}

type Usage struct {
	InputTokens      int `json:"input_tokens,omitempty"`
	OutputTokens     int `json:"output_tokens,omitempty"`
	CachedTokens     int `json:"cached_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

type Event struct {
	Version        string          `json:"version"`
	Seq            int64           `json:"seq"`
	ID             string          `json:"id,omitempty"`
	ConversationID string          `json:"conversation_id"`
	RunID          string          `json:"run_id"`
	ParentRunID    string          `json:"parent_run_id,omitempty"`
	Type           string          `json:"type"`
	CreatedAt      time.Time       `json:"created_at"`
	Payload        json.RawMessage `json:"payload,omitempty"`
}

type TextDelta struct {
	Text string `json:"text"`
}

type HostedSearch struct {
	Type     string               `json:"type"`
	ID       string               `json:"id"`
	Provider string               `json:"provider,omitempty"`
	Status   string               `json:"status"`
	Queries  []string             `json:"queries"`
	Sources  []HostedSearchSource `json:"sources"`
	Error    string               `json:"error,omitempty"`
}

type HostedSearchSource struct {
	URL        string `json:"url"`
	Title      string `json:"title,omitempty"`
	SourceType string `json:"sourceType,omitempty"`
}

func fromAIHostedSearch(items []ai.HostedSearch) []HostedSearch {
	if len(items) == 0 {
		return nil
	}
	out := make([]HostedSearch, len(items))
	for i, item := range items {
		out[i] = HostedSearch{Type: item.Type, ID: item.ID, Provider: item.Provider, Status: item.Status, Queries: item.Queries, Error: item.Error}
		out[i].Sources = make([]HostedSearchSource, len(item.Sources))
		for j, source := range item.Sources {
			out[i].Sources[j] = HostedSearchSource{URL: source.URL, Title: source.Title, SourceType: source.SourceType}
		}
	}
	return out
}

type ToolCallEvent struct {
	ToolCall ToolCall `json:"tool_call"`
}

type ToolResultEvent struct {
	ToolResult ToolResult `json:"tool_result"`
}

type ToolStatus struct {
	ToolCallID string `json:"tool_call_id,omitempty"`
	Tool       string `json:"tool"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	Compaction bool   `json:"compaction,omitempty"`
}

type PermissionOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

type PermissionRequest struct {
	PermissionID string             `json:"permission_id"`
	Tool         string             `json:"tool"`
	Command      string             `json:"command,omitempty"`
	Rule         string             `json:"rule,omitempty"`
	Description  string             `json:"description,omitempty"`
	Options      []PermissionOption `json:"options,omitempty"`
}

type PermissionDecision struct {
	PermissionID string `json:"permission_id"`
	Decision     string `json:"decision"`
	Reason       string `json:"reason,omitempty"`
}

type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}

type Question struct {
	ID       string           `json:"id"`
	Header   string           `json:"header,omitempty"`
	Prompt   string           `json:"prompt"`
	Options  []QuestionOption `json:"options"`
	Multiple bool             `json:"multiple,omitempty"`
}

type QuestionRequest struct {
	QuestionID string     `json:"question_id"`
	ToolCallID string     `json:"tool_call_id"`
	RunID      string     `json:"run_id"`
	DeadlineAt int64      `json:"deadline_at"`
	Questions  []Question `json:"questions"`
}

type QuestionAnswer struct {
	Prompt        string `json:"prompt,omitempty"`
	QuestionID    string `json:"question_id"`
	SelectedLabel string `json:"selected_label"`
	Custom        bool   `json:"custom,omitempty"`
}

type QuestionAnswerRequest struct {
	ConversationID string           `json:"conversation_id"`
	RunID          string           `json:"run_id"`
	QuestionID     string           `json:"question_id"`
	Answers        []QuestionAnswer `json:"answers"`
}

type QuestionResolution struct {
	QuestionID string           `json:"question_id"`
	ToolCallID string           `json:"tool_call_id"`
	RunID      string           `json:"run_id"`
	Kind       string           `json:"kind"`
	Questions  []Question       `json:"questions"`
	Answers    []QuestionAnswer `json:"answers"`
	Text       string           `json:"text"`
	TimedOut   bool             `json:"timed_out,omitempty"`
	Cancelled  bool             `json:"cancelled,omitempty"`
}

type Subagent struct {
	ID          string     `json:"id"`
	ParentID    string     `json:"parent_id,omitempty"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	Model       ModelRef   `json:"model"`
	Attempt     int        `json:"attempt,omitempty"`
	Report      string     `json:"report,omitempty"`
	Error       string     `json:"error,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
}

type SubagentEvent struct {
	Subagent Subagent `json:"subagent"`
}

type RunTerminal struct {
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
	Usage   *Usage `json:"usage,omitempty"`
}

type CreateSessionRequest struct {
	CWD      string    `json:"cwd,omitempty"`
	Model    ModelRef  `json:"model"`
	Title    string    `json:"title,omitempty"`
	Messages []Message `json:"messages,omitempty"`
}

type RunOptions struct {
	Reasoning       string          `json:"reasoning,omitempty"`
	Mode            string          `json:"mode,omitempty"`
	Search          string          `json:"search,omitempty"`
	ApprovalPolicy  string          `json:"approval_policy,omitempty"`
	WorkspaceRoots  []WorkspaceRoot `json:"workspace_roots,omitempty"`
	Tools           *ToolSelection  `json:"tools,omitempty"`
	PlanModeEnabled bool            `json:"plan_mode_enabled,omitempty"`
	MCPServerIDs    []string        `json:"mcp_server_ids,omitempty"`
}
type WorkspaceRoot struct {
	Path   string `json:"path"`
	Access string `json:"access"`
}

type ToolSelection struct {
	Policies map[string]string `json:"policies,omitempty"`
	Enabled  []string          `json:"enabled,omitempty"`
	Disabled []string          `json:"disabled,omitempty"`
}

type PromptRequest struct {
	ResumeMessageID string         `json:"resume_message_id,omitempty"`
	TurnID          string         `json:"turn_id,omitempty"`
	ConversationID  string         `json:"conversation_id"`
	ClientRequestID string         `json:"client_request_id"`
	Prompt          string         `json:"prompt,omitempty"`
	Content         []ContentBlock `json:"content,omitempty"`
	Model           *ModelRef      `json:"model,omitempty"`
	Options         *RunOptions    `json:"options,omitempty"`
	HookPolicy      string         `json:"hook_policy,omitempty"`
	HookScopeID     string         `json:"hook_scope_id,omitempty"`
	StopRequested   bool           `json:"stop_requested,omitempty"`
}

type CompactRequest struct {
	ConversationID   string `json:"conversation_id,omitempty"`
	ClientRequestID  string `json:"client_request_id"`
	ExpectedRevision string `json:"expected_revision"`
}

type CompactAccepted struct {
	Version        string `json:"version"`
	ConversationID string `json:"conversation_id"`
	RunID          string `json:"run_id"`
	AcceptedSeq    int64  `json:"accepted_seq"`
	Status         string `json:"status"`
	Revision       string `json:"revision,omitempty"`
}

// TextGenerateRequest is the stateless, no-tools auxiliary generation contract.
// Messages use the same canonical model as session history but only text blocks
// and system/developer/user/assistant roles are accepted by the backend.
type TextGenerateRequest struct {
	Model    ModelRef  `json:"model"`
	Messages []Message `json:"messages"`
	Output   string    `json:"output,omitempty"`
}

type TextGenerateResponse struct {
	Version string   `json:"version"`
	Text    string   `json:"text"`
	Model   ModelRef `json:"model"`
	Usage   *Usage   `json:"usage,omitempty"`
}

type RunAccepted struct {
	Version        string `json:"version"`
	ConversationID string `json:"conversation_id"`
	RunID          string `json:"run_id"`
	AcceptedSeq    int64  `json:"accepted_seq"`
}

type ResumeRequest struct {
	ConversationID string `json:"conversation_id"`
	AfterSeq       int64  `json:"after_seq,omitempty"`
}

type UpdateSessionRequest struct {
	Model           *ModelRef `json:"model,omitempty"`
	Title           *string   `json:"title,omitempty"`
	Pinned          *bool     `json:"pinned,omitempty"`
	Archived        *bool     `json:"archived,omitempty"`
	Shared          *bool     `json:"shared,omitempty"`
	ShareRedactTool *bool     `json:"share_redact_tool,omitempty"`
	// CWD moves the session to another workspace. It must be an absolute path to an
	// existing directory; tools in later turns run there.
	CWD *string `json:"cwd,omitempty"`
}

type HistoryMessageRef struct {
	SegmentIndex int    `json:"segment_index,omitempty"`
	MessageIndex int    `json:"message_index,omitempty"`
	SegmentID    string `json:"segment_id,omitempty"`
	MessageID    string `json:"message_id,omitempty"`
	Role         string `json:"role,omitempty"`
	ContentHash  string `json:"content_hash,omitempty"`
}

type EditSessionRequest struct {
	ExpectedRevision string            `json:"expected_revision"`
	MessageRef       HistoryMessageRef `json:"message_ref"`
	Replacement      Message           `json:"replacement"`
}

type BranchSessionRequest struct {
	ExpectedRevision string            `json:"expected_revision,omitempty"`
	MessageRef       HistoryMessageRef `json:"message_ref"`
	Title            string            `json:"title,omitempty"`
}

type ShareStatus struct {
	ConversationID    string    `json:"conversation_id"`
	Enabled           bool      `json:"enabled"`
	Token             string    `json:"token,omitempty"`
	CreatedAt         time.Time `json:"created_at,omitempty"`
	UpdatedAt         time.Time `json:"updated_at,omitempty"`
	RedactToolContent bool      `json:"redact_tool_content"`
}

type ShareUpdateRequest struct {
	Enabled           bool  `json:"enabled"`
	RedactToolContent *bool `json:"redact_tool_content,omitempty"`
}

type CancelRequest struct {
	ConversationID string `json:"conversation_id"`
	RunID          string `json:"run_id"`
}

type PermissionDecisionRequest struct {
	ConversationID string             `json:"conversation_id"`
	RunID          string             `json:"run_id"`
	Decision       PermissionDecision `json:"decision"`
}

func NewEvent(seq int64, conversationID, runID, typ string, payload any) (Event, error) {
	if seq < 1 {
		return Event{}, fmt.Errorf("event sequence must be positive")
	}
	if strings.TrimSpace(conversationID) == "" || strings.TrimSpace(runID) == "" {
		return Event{}, fmt.Errorf("event conversation_id and run_id are required")
	}
	var raw json.RawMessage
	if payload != nil {
		var err error
		raw, err = json.Marshal(payload)
		if err != nil {
			return Event{}, fmt.Errorf("marshal event payload: %w", err)
		}
	}
	return Event{Version: Version, Seq: seq, ConversationID: conversationID, RunID: runID, Type: typ, CreatedAt: time.Now().UTC(), Payload: raw}, nil
}

func (e Event) Validate() error {
	if e.Version != Version {
		return fmt.Errorf("unsupported protocol version %q", e.Version)
	}
	if e.Seq < 1 {
		return fmt.Errorf("event sequence must be positive")
	}
	if strings.TrimSpace(e.ConversationID) == "" || strings.TrimSpace(e.RunID) == "" {
		return fmt.Errorf("event conversation_id and run_id are required")
	}
	if strings.TrimSpace(e.Type) == "" {
		return fmt.Errorf("event type is required")
	}
	return nil
}

// FromAIMessage converts the provider-neutral message into the canonical
// protocol shape. Use FromAIMessageValidated when malformed provider data must
// be rejected instead of returned for inspection.
// FromAIMessage converts a provider message into the canonical protocol shape.
func FromAIMessage(m ai.Message) Message {
	model, provider := m.Model, ""
	if left, right, ok := strings.Cut(m.Model, " @ "); ok {
		model, provider = left, right
	}
	out := Message{ID: m.ID, Role: m.Role, ToolCallID: m.ToolCallID, Name: m.Name, Model: model, Provider: provider, Usage: FromAIUsage(m.Usage), HostedSearch: fromAIHostedSearch(m.HostedSearch), StopReason: string(m.StopReason), CreatedAt: m.SentAt}
	if m.Content != "" {
		out.Content = append(out.Content, ContentBlock{Type: ContentText, Text: m.Content})
	}
	for _, part := range m.Parts {
		switch part.Type {
		case "text":
			out.Content = append(out.Content, ContentBlock{Type: ContentText, Text: part.Text})
		case "image_url":
			url := ""
			if part.ImageURL != nil {
				url = part.ImageURL.URL
			}
			out.Content = append(out.Content, ContentBlock{Type: ContentImage, ImageURL: url, MimeType: part.MimeType})
		case ai.AttachmentFile:
			url := ""
			var filename string
			if part.FileURL != nil {
				url, filename = part.FileURL.URL, part.FileURL.Filename
			}
			out.Content = append(out.Content, ContentBlock{Type: ContentFile, FileURL: url, Filename: filename, MimeType: part.MimeType})
		default:
			out.Content = append(out.Content, ContentBlock{Type: part.Type, Text: part.Text})
		}
	}
	for _, tc := range m.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: json.RawMessage(tc.Function.Arguments)})
	}
	return out
}

func FromAIMessageValidated(m ai.Message) (Message, error) {
	out := FromAIMessage(m)
	if err := out.Validate(); err != nil {
		return Message{}, err
	}
	return out, nil
}

func (b ContentBlock) Validate() error {
	switch b.Type {
	case ContentText, ContentThinking:
		if b.ImageURL != "" || b.FileURL != "" || b.Filename != "" || b.MimeType != "" {
			return fmt.Errorf("%s content block has image fields", b.Type)
		}
	case ContentImage:
		if strings.TrimSpace(b.ImageURL) == "" {
			return errors.New("image content block requires image_url")
		}
		if b.Text != "" || b.FileURL != "" || b.Filename != "" {
			return errors.New("image content block has non-image fields")
		}
		if err := validateAttachmentURL(b.ImageURL, "image"); err != nil {
			return err
		}
	case ContentFile:
		if strings.TrimSpace(b.FileURL) == "" {
			return errors.New("file content block requires file_url")
		}
		if b.Text != "" || b.ImageURL != "" {
			return errors.New("file content block has non-file fields")
		}
		if strings.TrimSpace(b.MimeType) == "" {
			return errors.New("file content block requires mime_type")
		}
		if err := validateAttachmentURL(b.FileURL, "file"); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported canonical content block %q", b.Type)
	}
	return nil
}

func (m Message) Validate() error {
	switch m.Role {
	case RoleSystem, RoleDeveloper, RoleUser, RoleAssistant, RoleTool:
	default:
		return fmt.Errorf("unsupported message role %q", m.Role)
	}
	if m.Role == RoleTool {
		if strings.TrimSpace(m.ToolCallID) == "" {
			return errors.New("tool message requires tool_call_id")
		}
		if len(m.ToolCalls) > 0 {
			return errors.New("tool message cannot contain tool_calls")
		}
	} else {
		if m.ToolCallID != "" {
			return fmt.Errorf("%s message cannot contain tool_call_id", m.Role)
		}
		if len(m.ToolCalls) > 0 && m.Role != RoleAssistant {
			return fmt.Errorf("%s message cannot contain tool_calls", m.Role)
		}
	}
	seen := make(map[string]struct{}, len(m.ToolCalls))
	for _, tc := range m.ToolCalls {
		if strings.TrimSpace(tc.ID) == "" {
			return errors.New("tool call requires id")
		}
		if strings.TrimSpace(tc.Name) == "" {
			return fmt.Errorf("tool call %q requires name", tc.ID)
		}
		if _, ok := seen[tc.ID]; ok {
			return fmt.Errorf("duplicate tool call id %q", tc.ID)
		}
		seen[tc.ID] = struct{}{}
		if !isJSONObject(tc.Arguments) {
			return fmt.Errorf("tool call %q requires JSON object arguments", tc.ID)
		}
	}
	for i, b := range m.Content {
		if err := b.Validate(); err != nil {
			return fmt.Errorf("content[%d]: %w", i, err)
		}
	}
	return nil
}
func isJSONObject(raw json.RawMessage) bool {
	if len(raw) == 0 || !json.Valid(raw) {
		return false
	}
	var obj map[string]json.RawMessage
	return json.Unmarshal(raw, &obj) == nil && obj != nil
}

func (m Message) ToAIMessage() (ai.Message, error) {
	if err := m.Validate(); err != nil {
		return ai.Message{}, err
	}
	model := m.Model
	if m.Provider != "" {
		model += " @ " + m.Provider
	}
	out := ai.Message{ID: m.ID, Role: m.Role, ToolCallID: m.ToolCallID, Name: m.Name, Model: model, SentAt: m.CreatedAt}
	var parts []ai.ContentPart
	seenImage := false
	for _, b := range m.Content {
		switch b.Type {
		case ContentText:
			if !seenImage {
				out.Content += b.Text
			} else {
				parts = append(parts, ai.ContentPart{Type: "text", Text: b.Text})
			}
		case ContentThinking:
		case ContentImage:
			seenImage = true
			parts = append(parts, ai.ContentPart{Type: "image_url", MimeType: b.MimeType, ImageURL: &struct {
				URL string `json:"url"`
			}{URL: b.ImageURL}})
		case ContentFile:
			seenImage = true
			parts = append(parts, ai.ContentPart{Type: ai.AttachmentFile, MimeType: b.MimeType, FileURL: &struct {
				URL      string `json:"url"`
				Filename string `json:"filename,omitempty"`
			}{URL: b.FileURL, Filename: b.Filename}})
		}
	}
	out.Parts = parts
	for _, item := range m.HostedSearch {
		search := ai.HostedSearch{Type: item.Type, ID: item.ID, Provider: item.Provider, Status: item.Status, Queries: append([]string(nil), item.Queries...), Error: item.Error}
		for _, source := range item.Sources {
			search.Sources = append(search.Sources, ai.SearchSource{URL: source.URL, Title: source.Title, SourceType: source.SourceType})
		}
		out.HostedSearch = append(out.HostedSearch, search)
	}
	out.StopReason = ai.StopReason(m.StopReason)
	if m.Usage != nil {
		out.Usage = &ai.Usage{PromptTokens: m.Usage.InputTokens, CompletionTokens: m.Usage.OutputTokens, PromptCacheHitTokens: m.Usage.CachedTokens, PromptCacheWriteTokens: m.Usage.CacheWriteTokens}
	}
	for _, tc := range m.ToolCalls {
		c := ai.ToolCall{ID: tc.ID, Type: "function"}
		c.Function.Name, c.Function.Arguments = tc.Name, string(tc.Arguments)
		out.ToolCalls = append(out.ToolCalls, c)
	}
	return out, nil
}
func FromAIUsage(u *ai.Usage) *Usage {
	if u == nil {
		return nil
	}
	return &Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens, CachedTokens: u.Cached(), CacheWriteTokens: u.CacheWrite()}
}
