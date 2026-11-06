package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func runQuestion(t *testing.T, args string, ask AskFunc) (string, error) {
	t.Helper()
	ctx := WithAsk(context.Background(), ask)
	return QuestionTool().Run(ctx, json.RawMessage(args))
}

func TestQuestionToolSchemaKeepsSingleFormAndAddsQuestions(t *testing.T) {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal([]byte(questionSchema), &schema); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	for _, p := range []string{"question", "options", "multiple", "questions"} {
		if _, ok := schema.Properties[p]; !ok {
			t.Errorf("schema lost property %q", p)
		}
	}
	if len(schema.Required) != 0 {
		t.Errorf("required = %v; questions must stay optional and question must not be forced", schema.Required)
	}
}

func TestQuestionToolSingleFormUnchanged(t *testing.T) {
	var got AskRequest
	out, err := runQuestion(t, `{"question":"Which DB?","options":[{"label":"Postgres"},{"label":"SQLite"}],"multiple":true}`,
		func(_ context.Context, req AskRequest) ([]AskAnswer, bool) {
			got = req
			return []AskAnswer{{Labels: []string{"Postgres", "SQLite"}}}, true
		})
	if err != nil {
		t.Fatal(err)
	}
	if got.Question != "Which DB?" || len(got.Options) != 2 || !got.Multiple || got.Questions != nil {
		t.Errorf("request = %+v", got)
	}
	if out != `User answered "Which DB?": Postgres, SQLite` {
		t.Errorf("result = %q", out)
	}
}

func TestQuestionToolAsksSeveralQuestions(t *testing.T) {
	var got AskRequest
	args := `{"questions":[
		{"header":"Format","question":"Which output format?","options":[{"label":"JSON"},{"label":"YAML"}]},
		{"id":"tests","prompt":"Which tests to run?","multiple":true,"options":[{"label":" unit "},{"label":"e2e","description":" slow "}]}]}`
	out, err := runQuestion(t, args, func(_ context.Context, req AskRequest) ([]AskAnswer, bool) {
		got = req
		return []AskAnswer{{Labels: []string{"JSON"}}, {Labels: []string{"unit", "e2e"}}}, true
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Questions) != 2 {
		t.Fatalf("questions = %+v", got.Questions)
	}
	q1, q2 := got.Questions[0], got.Questions[1]
	if q1.ID != "q1" || q1.Header != "Format" || q1.Prompt != "Which output format?" || q1.Multiple {
		t.Errorf("first question = %+v", q1)
	}
	if q2.ID != "tests" || q2.Prompt != "Which tests to run?" || !q2.Multiple || q2.Options[0].Label != "unit" || q2.Options[1].Description != "slow" {
		t.Errorf("second question = %+v", q2)
	}
	for _, want := range []string{"1. Which output format?\n   → JSON", "2. Which tests to run?\n   → unit, e2e"} {
		if !strings.Contains(out, want) {
			t.Errorf("result %q missing %q", out, want)
		}
	}
}

func TestQuestionToolRejectsBadQuestions(t *testing.T) {
	never := func(context.Context, AskRequest) ([]AskAnswer, bool) {
		t.Fatal("asked an invalid request")
		return nil, false
	}
	opts := `[{"label":"a"},{"label":"b"}]`
	for _, args := range []string{
		`{"questions":[{"question":"","options":` + opts + `}]}`,
		`{"questions":[{"question":"one option","options":[{"label":"a"}]}]}`,
		`{"questions":[{"question":"dup","options":[{"label":"a"},{"label":"a"}]}]}`,
		`{"questions":[{"id":"x","question":"a","options":` + opts + `},{"id":"x","question":"b","options":` + opts + `}]}`,
		`{"questions":[` + strings.Repeat(`{"question":"q","options":`+opts+`},`, 4) + `{"question":"q","options":` + opts + `}]}`,
	} {
		if _, err := runQuestion(t, args, never); err == nil {
			t.Errorf("accepted %s", args)
		}
	}
}

// Chosen options read as their labels; the user's own words are marked as
// typed, alone or beside chosen options.
func TestAskAnswerText(t *testing.T) {
	for _, tc := range []struct {
		answer AskAnswer
		want   string
	}{
		{AskAnswer{Labels: []string{"Fast"}}, "Fast"},
		{AskAnswer{Labels: []string{"Docs", "Search, filter"}}, "Docs, Search, filter"},
		{AskAnswer{Custom: " my words "}, "my words (user-typed answer via Other, not a listed option)"},
		{AskAnswer{Labels: []string{"Docs"}, Custom: "and a changelog"}, "Docs; plus and a changelog (user-typed answer via Other, not a listed option)"},
	} {
		if got := tc.answer.Text(); got != tc.want {
			t.Errorf("%+v.Text() = %q, want %q", tc.answer, got, tc.want)
		}
	}
	req := AskRequest{Questions: []AskQuestion{{ID: "q1", Prompt: "Which?"}, {ID: "extras", Prompt: "Add what?"}}}
	got := AskAnswerText(req, []AskAnswer{{Custom: "neither"}, {Labels: []string{"Docs", "CI"}, Custom: "a badge"}})
	want := "The user answered:\n1. Which?\n   → neither (user-typed answer via Other, not a listed option)\n2. Add what?\n   → Docs, CI; plus a badge (user-typed answer via Other, not a listed option)"
	if got != want {
		t.Errorf("AskAnswerText = %q, want %q", got, want)
	}
}

// The question tool records the answers for its history in the /v1 answer
// shape, with the ids /v1 clients see.
func TestQuestionToolRecordsAnswers(t *testing.T) {
	args := `{"questions":[{"question":"Which?","options":[{"label":"a"},{"label":"b"}]},{"id":"extras","question":"Add what?","multiple":true,"options":[{"label":"Docs"},{"label":"Search, filter"},{"label":"CI"}]}]}`
	ctx := WithAsk(context.Background(), func(context.Context, AskRequest) ([]AskAnswer, bool) {
		return []AskAnswer{{Labels: []string{"b"}}, {Labels: []string{"Docs", "Search, filter"}, Custom: "a badge"}}, true
	})
	result := ExecuteResult(ctx, []Tool{QuestionTool()}, "question", json.RawMessage(args), false)
	if result.Failed {
		t.Fatal(result.Text)
	}
	got, _ := json.Marshal(result.QuestionAnswers)
	want := `[{"prompt":"Which?","question_id":"q1","selected_label":"b","selected_labels":["b"]},{"prompt":"Add what?","question_id":"extras","selected_label":"a badge","selected_labels":["Docs","Search, filter"],"custom":true}]`
	if string(got) != want {
		t.Errorf("answers = %s\nwant      %s", got, want)
	}

	single := WithAsk(context.Background(), func(context.Context, AskRequest) ([]AskAnswer, bool) {
		return []AskAnswer{{Custom: "neither"}}, true
	})
	result = ExecuteResult(single, []Tool{QuestionTool()}, "question", json.RawMessage(`{"question":"Which DB?","options":[{"label":"a"},{"label":"b"}]}`), false)
	got, _ = json.Marshal(result.QuestionAnswers)
	if want := `[{"prompt":"Which DB?","question_id":"q1","selected_label":"neither","custom":true}]`; string(got) != want {
		t.Errorf("single-form answers = %s, want %s", got, want)
	}
}
