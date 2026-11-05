package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

type AskOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	Recommended bool   `json:"recommended,omitempty"`
}

type AskQuestion struct {
	ID       string      `json:"id,omitempty"`
	Header   string      `json:"header,omitempty"`
	Prompt   string      `json:"prompt"`
	Options  []AskOption `json:"options"`
	Multiple bool        `json:"multiple,omitempty"`
}

type AskRequest struct {
	Question  string        `json:"question,omitempty"`
	Options   []AskOption   `json:"options,omitempty"`
	Multiple  bool          `json:"multiple,omitempty"`
	Questions []AskQuestion `json:"questions,omitempty"`
}

type askContextKey struct{}
type toolCallIDKey struct{}
type AskFunc func(context.Context, AskRequest) ([]string, bool)

func WithAsk(ctx context.Context, ask AskFunc) context.Context {
	return context.WithValue(ctx, askContextKey{}, ask)
}

func WithToolCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallIDKey{}, id)
}

func ToolCallID(ctx context.Context) string {
	id, _ := ctx.Value(toolCallIDKey{}).(string)
	return id
}

var Ask func(ctx context.Context, req AskRequest) (answers []string, ok bool)

func QuestionTool() Tool {
	return Tool{
		Def: ai.NewTool("question",
			"Ask the user to choose between options when a decision is genuinely theirs: ambiguous instructions, a preference, or a fork in implementation. The user sees a selectable list; a \"type your own answer\" row is always added, so never include an \"Other\" option. If you recommend an option, put it first and end its label with \"(Recommended)\". Answers come back as the chosen labels.",
			`{"type":"object","properties":{"question":{"type":"string","description":"The complete question"},"options":{"type":"array","minItems":2,"maxItems":6,"items":{"type":"object","properties":{"label":{"type":"string","description":"Display text, 1-5 words"},"description":{"type":"string","description":"One line explaining the choice"}},"required":["label"]}},"multiple":{"type":"boolean","description":"Allow selecting more than one option"}},"required":["question","options"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a AskRequest
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			// Gates never prompt for a question; deny rules still refuse it.
			if err := Authorize(ctx, "question", string(args)); err != nil {
				return "", err
			}
			ask, scoped := ctx.Value(askContextKey{}).(AskFunc)
			if !scoped {
				ask = Ask
			}
			if ask == nil {
				return "", errors.New("no interactive user to ask; make a reasonable assumption and continue")
			}
			answers, ok := ask(ctx, a)
			if !ok {
				return "", errors.New("the user dismissed the question")
			}
			return "User answered \"" + a.Question + "\": " + strings.Join(answers, ", "), nil
		},
	}
}
