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
	EventRunProgress         = "run.progress"
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
	EventClientToolRequested = "client_tool.requested"
	EventClientToolResolved  = "client_tool.resolved"
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
	EventGoalUpdated         = "goal.updated"
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
	// ForkedFrom names the session this one was branched from; ForkSeq is the last copied message.
	ForkedFrom string `json:"forked_from,omitempty"`
	ForkSeq    int    `json:"fork_seq,omitempty"`
}

type Session struct {
	SessionSummary
	Revision     string     `json:"revision"`
	SyncEpoch    string     `json:"sync_epoch,omitempty"`
	SyncRevision string     `json:"sync_revision,omitempty"`
	Messages     []Message  `json:"messages"`
	Tasks        []Subagent `json:"tasks,omitempty"`
	LastSeq      int64      `json:"last_seq"`
}

type SessionPage struct {
	Sessions   []SessionSummary `json:"sessions"`
	TotalCount int              `json:"total_count"`
	Next       *PageCursor      `json:"next,omitempty"`
	Version    string           `json:"version"`
	Epoch      string           `json:"epoch,omitempty"`
	Revision   string           `json:"revision,omitempty"`
}

type SessionChange struct {
	ID       string          `json:"id"`
	Revision string          `json:"revision"`
	Kind     string          `json:"kind"`
	Listed   bool            `json:"listed"`
	Session  *SessionSummary `json:"session,omitempty"`
}

type SessionChanges struct {
	Epoch        string          `json:"epoch"`
	HeadRevision string          `json:"head_revision"`
	MinSince     string          `json:"min_since"`
	NextSince    *string         `json:"next_since"`
	Reset        bool            `json:"reset"`
	ResetReason  string          `json:"reset_reason,omitempty"`
	More         bool            `json:"more"`
	Changes      []SessionChange `json:"changes"`
}

type SessionWorkdir struct {
	CWD       string    `json:"cwd"`
	Count     int       `json:"count"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SessionSnapshotInfo struct {
	Epoch            string           `json:"epoch"`
	Revision         string           `json:"revision"`
	SnapshotID       string           `json:"snapshot_id"`
	ExpiresInSeconds int              `json:"expires_in_seconds"`
	TotalCount       int              `json:"total_count"`
	Workdirs         []SessionWorkdir `json:"workdirs"`
}

type SessionSnapshotPage struct {
	Epoch      string           `json:"epoch"`
	Revision   string           `json:"revision"`
	SnapshotID string           `json:"snapshot_id"`
	TotalCount int              `json:"total_count"`
	Sessions   []SessionSummary `json:"sessions"`
	NextOffset *int             `json:"next_offset"`
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
	// Origin is "background" on a user-role notice the runtime delivered on its
	// own (a wait result, a process exit, a background task report, a goal
	// continuation round, the new-context notice, the output-limit continuation
	// reminder); it is absent on messages the user wrote.
	Origin string `json:"origin,omitempty"`
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
	// ToolRunInfo is filled in history once the call has run; absent in
	// tool.call events and in messages a client sends.
	ToolRunInfo
}

// ToolRunInfo describes how a tool call ran, for display. Every field is
// optional and absent when unknown; the model never sees them.
type ToolRunInfo struct {
	// ExitCode is the exit status of the command a shell tool (bash, Bash,
	// ManagedProcess, ProcessWait, ProcessStop) ran or observed, 0 included;
	// absent for other tools and for a command that timed out, was cancelled,
	// was killed or is still running.
	ExitCode *int `json:"exit_code,omitempty"`
	// TimedOut marks a command killed when its timeout passed.
	TimedOut bool `json:"timed_out,omitempty"`
	// DurationMs is the tool's own wall-clock time in milliseconds, rounded
	// up, without time spent waiting for permission.
	DurationMs int64 `json:"duration_ms,omitempty"`
	// SandboxDenial explains a shell command the OS sandbox most likely
	// stopped. Only in tool.result events; history keeps the same explanation
	// in the output text alone.
	SandboxDenial *SandboxDenial `json:"sandbox_denial,omitempty"`
	// QuestionAnswers is what the user answered a question or AskUserQuestion
	// call, one entry per question, as question.resolved reports it.
	QuestionAnswers []QuestionAnswer `json:"question_answers,omitempty"`
}

// SandboxDenial is a hint recognized from a failed command's output. Nothing
// is granted from it: a client may offer the user to add SuggestedRoot as a
// writable workspace root for a later run.
type SandboxDenial struct {
	// Kind is "write" (outside the writable roots), "protected" (git
	// configuration, hooks or agent settings inside the workspace), "masked"
	// (a hidden credential directory) or "network" (network access is off).
	Kind string `json:"kind"`
	// Path is the refused path, absolute and with symlinks resolved; absent for
	// "network".
	Path string `json:"path,omitempty"`
	// SuggestedRoot is set only for "write" when the command itself named the
	// path, and never for a directory holding the home directory, a credential
	// directory or a system top-level directory.
	SuggestedRoot string `json:"suggested_root,omitempty"`
}

type ToolResult struct {
	Content   []ContentBlock `json:"content,omitempty"`
	ID        string         `json:"id"`
	Name      string         `json:"name,omitempty"`
	Output    string         `json:"output"`
	Failed    bool           `json:"failed,omitempty"`
	Cancelled bool           `json:"cancelled,omitempty"`
	ToolRunInfo
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

// EventPermissionAutoResolved says a gated call ran without asking because a
// remembered rule or the read-only allowance covered it (capability
// permission_rules).
const EventPermissionAutoResolved = "permission.auto_resolved"

type PermissionOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

type PermissionRequest struct {
	PermissionID string `json:"permission_id"`
	Tool         string `json:"tool"`
	Command      string `json:"command,omitempty"`
	// Shell runs Command, when the tool runs one.
	Shell       string             `json:"shell,omitempty"`
	Rule        string             `json:"rule,omitempty"`
	Description string             `json:"description,omitempty"`
	Options     []PermissionOption `json:"options,omitempty"`
	// Suggestion is what allow_always would remember; absent when the action can
	// only be allowed once (capability permission_rules).
	Suggestion *PermissionSuggestion `json:"suggestion,omitempty"`
	// Scope "session" means approving also grants this kind of action for the
	// rest of the session, so clients can say so before the user decides.
	Scope string `json:"scope,omitempty"`
	// ToolCallID is the model tool call awaiting this decision, when known, so
	// clients can show the prompt beside that call.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Preview is the change a file tool (write, edit, Write, Edit) will make;
	// absent for other tools and when there is nothing to show.
	Preview *PermissionPreview `json:"preview,omitempty"`
}

// PermissionPreview shows a pending file change while the user decides.
type PermissionPreview struct {
	Path string `json:"path"`
	// Diff lists the changed lines as "<line> <mark> <text>", mark one of ' ',
	// '-', '+': the format of the diff block in edit and write results. It holds
	// at most 200 rows and 32 KiB; empty when the change is too large to preview.
	Diff string `json:"diff,omitempty"`
	// NewFile marks a write that creates the file.
	NewFile bool `json:"new_file,omitempty"`
	// Truncated marks a Diff cut to those limits or left out.
	Truncated bool `json:"truncated,omitempty"`
}

type PermissionDecision struct {
	PermissionID string `json:"permission_id"`
	Decision     string `json:"decision"`
	Reason       string `json:"reason,omitempty"`
	// Scope picks where allow_always stores the suggested rules: session, project
	// or user. Empty keeps the pre-rules meaning, this session only.
	Scope string `json:"scope,omitempty"`
}

// PermissionSuggestion lists the rules allow_always stores, all of them together,
// and the scope a client should preselect.
type PermissionSuggestion struct {
	Rules        []string `json:"rules"`
	DefaultScope string   `json:"default_scope"`
}

// PermissionAutoResolved is the payload of permission.auto_resolved.
type PermissionAutoResolved struct {
	ToolCallID string `json:"tool_call_id,omitempty"`
	Tool       string `json:"tool"`
	Command    string `json:"command,omitempty"`
	// Source is "rule" or "read_only".
	Source string `json:"source"`
	Rule   string `json:"rule,omitempty"`
	Scope  string `json:"scope,omitempty"`
}

// PermissionRule is one remembered rule as the rules API returns it.
type PermissionRule struct {
	ID            string `json:"id"`
	Rule          string `json:"rule"`
	Effect        string `json:"effect"`
	Scope         string `json:"scope"`
	CreatedAt     string `json:"created_at,omitempty"`
	SourceSession string `json:"source_session,omitempty"`
}

// PermissionRulesResponse answers GET /v1/permissions/rules.
type PermissionRulesResponse struct {
	ProjectRoot string           `json:"project_root,omitempty"`
	Rules       []PermissionRule `json:"rules"`
}

// PermissionRuleCreate is the body of POST /v1/permissions/rules. Workspace names
// the project for project scope; SessionID the session for session scope.
type PermissionRuleCreate struct {
	Rule      string `json:"rule"`
	Effect    string `json:"effect"`
	Scope     string `json:"scope"`
	Workspace string `json:"workspace,omitempty"`
	SessionID string `json:"session_id,omitempty"`
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

// QuestionAnswer is one answer in a question answer request, a
// question.resolved event and a question tool call's history. A request sends
// selected_label (an option's label, or with custom the user's own words) or,
// where capabilities.question_selected_labels is 1, selected_labels (the chosen
// options; with custom, selected_label adds the user's own words).
type QuestionAnswer = ai.QuestionAnswer

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
	ID          string   `json:"id"`
	ParentID    string   `json:"parent_id,omitempty"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	Model       ModelRef `json:"model"`
	Attempt     int      `json:"attempt,omitempty"`
	Report      string   `json:"report,omitempty"`
	Error       string   `json:"error,omitempty"`
	// ParentToolCallID is the id of the parent's `subagent` tool call that
	// started this subagent, so a client can show both as one item.
	ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
	// Background is true for subagents started with background=true.
	Background bool `json:"background,omitempty"`
	// Usage, ToolCount and DurationMS cover the subagent's own requests and
	// tool calls; DurationMS is set once it has ended.
	Usage      *Usage     `json:"usage,omitempty"`
	ToolCount  int        `json:"tool_count,omitempty"`
	DurationMS int64      `json:"duration_ms,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
}

type SubagentEvent struct {
	Subagent Subagent `json:"subagent"`
}

// Child events record a subagent's own transcript on the parent session's
// event stream. Every payload carries task_id (the Subagent id). Text and
// thinking arrive in coalesced chunks; clients that do not know these types
// ignore them.
const (
	EventChildText      = "trajectory.child.text"
	EventChildThinking  = "trajectory.child.thinking"
	EventChildSteer     = "trajectory.child.steer"
	EventChildToolStart = "trajectory.child.tool_start"
	EventChildToolEnd   = "trajectory.child.tool_end"
)

// ChildTextEvent is the payload of trajectory.child.text, .thinking and .steer.
type ChildTextEvent struct {
	TaskID string `json:"task_id"`
	Text   string `json:"text"`
}

// SubagentList answers GET /v1/sessions/{id}/subagents.
type SubagentList struct {
	Subagents []Subagent `json:"subagents"`
	LastSeq   int64      `json:"last_seq"`
}

// SubagentTranscript answers GET /v1/sessions/{id}/subagents/{task}: the
// subagent's transcript up to LastSeq. A client continues it live with the
// child events after LastSeq.
type SubagentTranscript struct {
	Subagent Subagent  `json:"subagent"`
	Messages []Message `json:"messages"`
	LastSeq  int64     `json:"last_seq"`
	// Live is true while the subagent is still running in this backend.
	Live bool `json:"live"`
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
	Reasoning      string `json:"reasoning,omitempty"`
	Mode           string `json:"mode,omitempty"`
	Search         string `json:"search,omitempty"`
	ApprovalPolicy string `json:"approval_policy,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
	// ReviewScope "per_run" (capability ai_review_per_run) lets the permission mode alone decide
	// AI review: only auto_review reviews, with the session's model when no reviewer is set.
	// Without it the global aiApproval mode keeps applying as before.
	ReviewScope     string          `json:"review_scope,omitempty"`
	WorkspaceRoots  []WorkspaceRoot `json:"workspace_roots,omitempty"`
	Tools           *ToolSelection  `json:"tools,omitempty"`
	PlanModeEnabled bool            `json:"plan_mode_enabled,omitempty"`
	MCPServerIDs    []string        `json:"mcp_server_ids,omitempty"`
	// ClientTools are tools the connected client executes itself (for example the LiveAgent
	// desktop Browser). They are offered to the model for this run only; a call is published
	// as client_tool.requested and completes when the client posts the result.
	ClientTools []ClientTool `json:"client_tools,omitempty"`
	// Sandbox (capability run_sandbox) confines the run's shell commands, hooks
	// and terminals with the platform sandbox. It can only tighten a sandbox the
	// server is configured with. The run is refused when the machine cannot
	// enforce it; it never falls back to running unconfined.
	Sandbox *RunSandbox `json:"sandbox,omitempty"`
}

// RunSandbox is a run's request for OS-level confinement. Mode "workspace" lets
// commands read the machine (credential and LiveAgent data directories masked)
// and write only the workspace roots and temporary directories. Network false
// blocks network access.
type RunSandbox struct {
	Mode    string `json:"mode,omitempty"`
	Network bool   `json:"network"`
}

type ClientTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ClientToolRequest struct {
	CallID     string          `json:"call_id"`
	ToolCallID string          `json:"tool_call_id"`
	RunID      string          `json:"run_id"`
	Tool       string          `json:"tool"`
	Arguments  json.RawMessage `json:"arguments"`
	DeadlineAt int64           `json:"deadline_at"`
}

type ClientToolImage struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
}

type ClientToolResultRequest struct {
	ConversationID string            `json:"conversation_id"`
	RunID          string            `json:"run_id"`
	Text           string            `json:"text"`
	Images         []ClientToolImage `json:"images,omitempty"`
	IsError        bool              `json:"is_error,omitempty"`
}

type ClientToolResolution struct {
	CallID     string `json:"call_id"`
	ToolCallID string `json:"tool_call_id"`
	RunID      string `json:"run_id"`
	Tool       string `json:"tool"`
	Text       string `json:"text"`
	ImageCount int    `json:"image_count,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	Cancelled  bool   `json:"cancelled,omitempty"`
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
	// PromptCommand marks Prompt as "/name args" naming a prompt command. The backend
	// expands it for the model while history keeps the text the user typed.
	PromptCommand bool `json:"prompt_command,omitempty"`
	// Snapshot is request-only text resolved at acceptance; it is never part of the wire.
	Snapshot string `json:"-"`
}

// GoalRequest manages the session goal. Set and resume start a run with Run's request
// identity, model and options; later rounds reuse that run's options. Pause stops the
// running round and keeps the goal; update replaces the goal text without starting a
// run; clear drops the goal.
type GoalRequest struct {
	Action string         `json:"action"`
	Goal   string         `json:"goal,omitempty"`
	Run    *PromptRequest `json:"run,omitempty"`
}

// GoalState is the session goal snapshot returned by the goal route and carried by
// goal.updated events. Status is none, active, continuing, met, paused or cleared.
// A continuing event names the request id of the round the backend starts next.
// Reason explains a pause: user, restored, max_rounds, busy, start_failed, cancelled,
// failed, or awaiting_input when a round asked the user for input; the user's next
// plain run then resumes the goal as its round.
type GoalState struct {
	Version             string `json:"version"`
	ConversationID      string `json:"conversation_id"`
	Goal                string `json:"goal"`
	Status              string `json:"status"`
	Round               int    `json:"round"`
	MaxRounds           int    `json:"max_rounds"`
	Reason              string `json:"reason,omitempty"`
	NextClientRequestID string `json:"next_client_request_id,omitempty"`
	NextPrompt          string `json:"next_prompt,omitempty"`
	// StartedAt is when the current goal was set or last resumed (RFC 3339); the
	// desktop goal bar shows the elapsed time from it.
	StartedAt string       `json:"started_at,omitempty"`
	Run       *RunAccepted `json:"run,omitempty"`
}

// SlashCommand is one entry of the desktop slash command catalog. Kind "action" is
// handled by the client by name; kind "prompt" is sent as a run with prompt_command.
type SlashCommand struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	ArgumentHint string `json:"argument_hint,omitempty"`
	Kind         string `json:"kind"`
	Source       string `json:"source"`
}

type SlashCommandList struct {
	Version  string         `json:"version"`
	Commands []SlashCommand `json:"commands"`
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
	out := Message{ID: m.ID, Role: m.Role, ToolCallID: m.ToolCallID, Name: m.Name, Model: model, Provider: provider, Usage: FromAIUsage(m.Usage), HostedSearch: fromAIHostedSearch(m.HostedSearch), StopReason: string(m.StopReason), CreatedAt: m.SentAt, Origin: m.Origin}
	if m.Reasoning != "" {
		out.Content = append(out.Content, ContentBlock{Type: ContentThinking, Text: m.Reasoning})
	}
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
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: json.RawMessage(tc.Function.Arguments), ToolRunInfo: ToolRunInfo{ExitCode: tc.ExitStatus, TimedOut: tc.TimedOut, DurationMs: tc.DurationMs, QuestionAnswers: tc.QuestionAnswers}})
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
	if m.Role == RoleUser && m.Origin == ai.OriginBackground {
		out.Origin = m.Origin
	}
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
			out.Reasoning += b.Text
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
