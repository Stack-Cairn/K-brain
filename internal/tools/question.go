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
}

type AskRequest struct {
	Question string      `json:"question"`
	Options  []AskOption `json:"options"`
	Multiple bool        `json:"multiple"`
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
			if Ask == nil {
				return "", errors.New("no interactive user to ask; make a reasonable assumption and continue")
			}
			answers, ok := Ask(ctx, a)
			if !ok {
				return "", errors.New("the user dismissed the question")
			}
			return "User answered \"" + a.Question + "\": " + strings.Join(answers, ", "), nil
		},
	}
}
