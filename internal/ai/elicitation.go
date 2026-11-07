package ai

// ElicitField is one input of an MCP elicitation form, flattened from the
// server's requestedSchema (see internal/elicit). The /v1 protocol uses this
// shape in elicitation.requested and in history.
type ElicitField struct {
	// Name is the property name: the key of the field in the answer content.
	Name string `json:"name"`
	// Type is "string", "number", "integer", "boolean" or "array". A string
	// field with Options is a single choice; an array field is always a
	// multiple choice of Options.
	Type        string `json:"type"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
	// Default is the value to prefill: a string, number, boolean or, for an
	// array field, a list of option values.
	Default any `json:"default,omitempty"`
	// Format is "email", "uri", "date" or "date-time" (string fields only).
	Format    string `json:"format,omitempty"`
	MinLength *int   `json:"min_length,omitempty"`
	MaxLength *int   `json:"max_length,omitempty"`
	// Pattern is a regular expression the whole value must contain a match
	// for, as in JSON Schema (unanchored; Go RE2 syntax).
	Pattern string   `json:"pattern,omitempty"`
	Minimum *float64 `json:"minimum,omitempty"`
	Maximum *float64 `json:"maximum,omitempty"`
	// Options are the choices of a single (string) or multiple (array) choice
	// field, in the server's order.
	Options  []ElicitOption `json:"options,omitempty"`
	MinItems *int           `json:"min_items,omitempty"`
	MaxItems *int           `json:"max_items,omitempty"`
	// Sensitive marks a free-text field whose name or title reads like a
	// secret (password, token, API key...). Clients mask its input and warn;
	// K-brain leaves its value out of the journal and history.
	Sensitive bool `json:"sensitive,omitempty"`
}

// ElicitOption is one choice of an elicitation field. Value goes into the
// answer; Label is what the user sees (Value when the server gave no title).
type ElicitOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Elicitation records one elicitation an MCP server made during a tool call
// and how it ended, for the call's history. Display only: the model never
// sees it.
type Elicitation struct {
	// ElicitationID is the /v1 elicitation id; empty when the request never
	// reached a /v1 client (TUI, or declined without asking).
	ElicitationID string        `json:"elicitation_id,omitempty"`
	Server        string        `json:"server"`
	Message       string        `json:"message"`
	Fields        []ElicitField `json:"fields"`
	// Action is "accept", "decline" or "cancel": what the MCP server received.
	Action string `json:"action"`
	// Reason says how the action came about: "user", "timeout", "cancelled",
	// "no_client", "unattended" or "limit".
	Reason string `json:"reason"`
	// Content is the accepted answer without the values of Redacted fields.
	Content  map[string]any `json:"content,omitempty"`
	Redacted []string       `json:"redacted,omitempty"`
}
