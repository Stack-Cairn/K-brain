package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// AskRequest is one question (Question, Options, Multiple) or, when
// Questions is set, several asked together. An AskFunc answers with one
// AskAnswer per question, in order: a single one for the first form.
type AskRequest struct {
	Question  string        `json:"question,omitempty"`
	Options   []AskOption   `json:"options,omitempty"`
	Multiple  bool          `json:"multiple,omitempty"`
	Questions []AskQuestion `json:"questions,omitempty"`
}

// AskAnswer is the user's answer to one question: the options they chose
// (Labels, each one of the question's labels, in option order) and their own
// words (Custom, typed instead of or, on a multi-select question, beside the
// chosen options).
type AskAnswer struct {
	Labels []string
	Custom string
}

// Text is the answer as the model sees it: chosen options by their labels,
// the user's own words marked as typed rather than listed.
func (a AskAnswer) Text() string {
	text := strings.Join(a.Labels, ", ")
	if custom := strings.TrimSpace(a.Custom); custom != "" {
		typed := custom + " (user-typed answer via Other, not a listed option)"
		if text == "" {
			return typed
		}
		text += "; plus " + typed
	}
	return text
}

// record is the answer as question tool history keeps it (the /v1 answer shape).
func (a AskAnswer) record(id, prompt string) ai.QuestionAnswer {
	r := ai.QuestionAnswer{Prompt: prompt, QuestionID: id, SelectedLabel: strings.Join(a.Labels, ", ")}
	if len(a.Labels) > 0 {
		r.SelectedLabels = append([]string(nil), a.Labels...)
	}
	if custom := strings.TrimSpace(a.Custom); custom != "" {
		r.SelectedLabel, r.Custom = custom, true
	}
	return r
}

type askContextKey struct{}
type toolCallIDKey struct{}
type AskFunc func(context.Context, AskRequest) ([]AskAnswer, bool)

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

var Ask func(ctx context.Context, req AskRequest) (answers []AskAnswer, ok bool)

// questionSchema is the question tool's input: one question (question,
// options, multiple) or, with questions, up to four asked together. Both
// forms stay accepted; questions wins when both are given.
const questionSchema = `{"type":"object","properties":{` +
	`"question":{"type":"string","description":"The complete question (single-question form)"},` +
	`"options":` + askOptionsSchema + `,` +
	`"multiple":{"type":"boolean","description":"Allow selecting more than one option"},` +
	`"questions":{"type":"array","minItems":1,"maxItems":4,"description":"Several related questions asked together instead of question/options; each is answered on its own tab","items":{"type":"object","properties":{` +
	`"header":{"type":"string","description":"Short tab label, 1-3 words"},` +
	`"question":{"type":"string","description":"The complete question"},` +
	`"options":` + askOptionsSchema + `,` +
	`"multiple":{"type":"boolean","description":"Allow selecting more than one option"}},"required":["question","options"]}}}}`

const askOptionsSchema = `{"type":"array","minItems":2,"maxItems":6,"items":{"type":"object","properties":{"label":{"type":"string","description":"Display text, 1-5 words"},"description":{"type":"string","description":"One line explaining the choice"}},"required":["label"]}}`

// MaxAskQuestions bounds the questions of one request, as /v1 clients do.
const MaxAskQuestions = 4

// askInput is the question tool's arguments. Questions accept "question" (the
// tool's own spelling) or "prompt" (AskQuestion's) for the text.
type askInput struct {
	Question  string      `json:"question"`
	Options   []AskOption `json:"options"`
	Multiple  bool        `json:"multiple"`
	Questions []struct {
		ID       string      `json:"id"`
		Header   string      `json:"header"`
		Question string      `json:"question"`
		Prompt   string      `json:"prompt"`
		Options  []AskOption `json:"options"`
		Multiple bool        `json:"multiple"`
	} `json:"questions"`
}

// ParseAskRequest reads the question tool's arguments. The single-question
// form passes through as before; the questions form is checked against what
// every client can show: 1-4 questions, each with a prompt and 2-6 distinct
// labels, ids unique (q1, q2, ... when omitted).
func ParseAskRequest(args json.RawMessage) (AskRequest, error) {
	var in askInput
	if err := json.Unmarshal(args, &in); err != nil {
		return AskRequest{}, err
	}
	if len(in.Questions) == 0 {
		return AskRequest{Question: in.Question, Options: in.Options, Multiple: in.Multiple}, nil
	}
	if len(in.Questions) > MaxAskQuestions {
		return AskRequest{}, fmt.Errorf("ask at most %d questions at once", MaxAskQuestions)
	}
	req := AskRequest{Questions: make([]AskQuestion, len(in.Questions))}
	ids := map[string]bool{}
	for i, q := range in.Questions {
		prompt := strings.TrimSpace(q.Question)
		if prompt == "" {
			prompt = strings.TrimSpace(q.Prompt)
		}
		id := strings.TrimSpace(q.ID)
		if id == "" {
			id = fmt.Sprintf("q%d", i+1)
		}
		if prompt == "" {
			return AskRequest{}, fmt.Errorf("question %d has no text", i+1)
		}
		if ids[id] {
			return AskRequest{}, fmt.Errorf("question id %q is used twice", id)
		}
		ids[id] = true
		if len(q.Options) < 2 || len(q.Options) > 6 {
			return AskRequest{}, fmt.Errorf("question %d needs 2-6 options", i+1)
		}
		labels := map[string]bool{}
		opts := make([]AskOption, len(q.Options))
		for j, o := range q.Options {
			o.Label, o.Description = strings.TrimSpace(o.Label), strings.TrimSpace(o.Description)
			if o.Label == "" || labels[o.Label] {
				return AskRequest{}, fmt.Errorf("question %d: option labels must be non-empty and distinct", i+1)
			}
			labels[o.Label] = true
			opts[j] = o
		}
		req.Questions[i] = AskQuestion{ID: id, Header: strings.TrimSpace(q.Header), Prompt: prompt, Options: opts, Multiple: q.Multiple}
	}
	return req, nil
}

// AskAnswerText is the question tool's result for answers to req, one per
// question in order (one for the single-question form).
func AskAnswerText(req AskRequest, answers []AskAnswer) string {
	if len(req.Questions) == 0 {
		answer := ""
		if len(answers) > 0 {
			answer = answers[0].Text()
		}
		return "User answered \"" + req.Question + "\": " + answer
	}
	var b strings.Builder
	b.WriteString("The user answered:")
	for i, q := range req.Questions {
		answer := "(no answer)"
		if i < len(answers) {
			if text := answers[i].Text(); strings.TrimSpace(text) != "" {
				answer = text
			}
		}
		fmt.Fprintf(&b, "\n%d. %s\n   → %s", i+1, q.Prompt, answer)
	}
	return b.String()
}

// askRecords is answers to req as question tool history keeps them, with the
// question ids /v1 clients see (q1 for the single-question form).
func askRecords(req AskRequest, answers []AskAnswer) []ai.QuestionAnswer {
	qs := req.Questions
	if len(qs) == 0 {
		qs = []AskQuestion{{ID: "q1", Prompt: req.Question}}
	}
	out := make([]ai.QuestionAnswer, 0, len(answers))
	for i, a := range answers {
		if i < len(qs) {
			out = append(out, a.record(qs[i].ID, qs[i].Prompt))
		}
	}
	return out
}

func QuestionTool() Tool {
	return Tool{
		Def: ai.NewTool("question",
			"Ask the user to choose between options when a decision is genuinely theirs: ambiguous instructions, a preference, or a fork in implementation. The user sees a selectable list; a \"type your own answer\" row is always added, so never include an \"Other\" option. If you recommend an option, put it first and end its label with \"(Recommended)\". To ask up to 4 related questions at once, pass questions (each with a short header) instead of question/options. Answers come back as the chosen labels.",
			questionSchema),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			a, err := ParseAskRequest(args)
			if err != nil {
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
			ReportQuestionAnswers(ctx, askRecords(a, answers))
			return AskAnswerText(a, answers), nil
		},
	}
}
