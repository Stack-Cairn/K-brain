package elicit

import (
	"context"
	"encoding/json"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// Actions an elicitation ends with, as the MCP server receives them.
const (
	Accept  = "accept"
	Decline = "decline"
	Cancel  = "cancel"
)

// Reasons say how an action came about. They are K-brain's own: the MCP
// server sees only the action.
const (
	// ReasonUser: the user chose the action.
	ReasonUser = "user"
	// ReasonTimeout: nobody answered before the deadline (cancel).
	ReasonTimeout = "timeout"
	// ReasonCancelled: the run stopped, or the server withdrew the request
	// (cancel).
	ReasonCancelled = "cancelled"
	// ReasonNoClient: no front end that can show a form is attached
	// (decline).
	ReasonNoClient = "no_client"
	// ReasonUnattended: the run is unattended, so no one can answer
	// (decline).
	ReasonUnattended = "unattended"
	// ReasonLimit: the tool call has already made MaxPerCall elicitations
	// (decline).
	ReasonLimit = "limit"
)

// MaxPerCall caps the elicitations one tool call may make; past it the
// server is declined without asking. It keeps a looping server from holding
// the user in a stream of forms.
const MaxPerCall = 8

// Request is an elicitation a front end should show: Server is the
// configured MCP server name, the identity the user must see.
type Request struct {
	Server  string
	Tool    string
	Message string
	Fields  []ai.ElicitField
	// Schema is the requestedSchema as the server sent it, for clients that
	// render JSON Schema themselves.
	Schema json.RawMessage
}

// Response is how an elicitation ended. Content, for Accept, has been
// validated against the request's fields by the front end.
type Response struct {
	Action  string
	Content map[string]any
	Reason  string
	// ID is the front end's id for the request (the /v1 elicitation id),
	// recorded in history.
	ID string
}

// Func asks the user. It blocks until the user answers or ctx ends; it must
// always return one of the three actions.
type Func func(context.Context, Request) Response

// Default is the front end used when the context carries none. The TUI sets
// it; nil means no front end.
var Default Func

type hookKey struct{}

// With returns ctx carrying fn as the front end for elicitations made during
// tool calls under ctx.
func With(ctx context.Context, fn Func) context.Context {
	return context.WithValue(ctx, hookKey{}, fn)
}

// Handler returns the front end for ctx: the one With set, else Default,
// else nil.
func Handler(ctx context.Context) Func {
	if fn, ok := ctx.Value(hookKey{}).(Func); ok && fn != nil {
		return fn
	}
	return Default
}
