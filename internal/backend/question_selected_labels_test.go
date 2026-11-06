package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// selectQuestions are a single-select and a multi-select question, the second
// with a label that itself holds ", ".
var selectQuestions = []protocol.Question{
	{ID: "q1", Prompt: "Which DB?", Options: []protocol.QuestionOption{{Label: "Postgres"}, {Label: "SQLite"}}},
	{ID: "q2", Prompt: "Add what?", Multiple: true, Options: []protocol.QuestionOption{{Label: "Docs"}, {Label: "Search, filter"}, {Label: "CI"}}},
}

func TestQuestionSelectedLabelsValidation(t *testing.T) {
	single := protocol.QuestionAnswer{QuestionID: "q1", SelectedLabel: "SQLite"}
	for _, tc := range []struct {
		name string
		in   protocol.QuestionAnswer // the answer to q2
		want protocol.QuestionAnswer
	}{
		{"options in option order", protocol.QuestionAnswer{QuestionID: "q2", SelectedLabels: []string{"CI", "Search, filter"}},
			protocol.QuestionAnswer{Prompt: "Add what?", QuestionID: "q2", SelectedLabel: "Search, filter, CI", SelectedLabels: []string{"Search, filter", "CI"}}},
		{"options and own words", protocol.QuestionAnswer{QuestionID: "q2", SelectedLabels: []string{"Docs"}, SelectedLabel: " a badge ", Custom: true},
			protocol.QuestionAnswer{Prompt: "Add what?", QuestionID: "q2", SelectedLabel: "a badge", SelectedLabels: []string{"Docs"}, Custom: true}},
		{"empty list is the legacy form", protocol.QuestionAnswer{QuestionID: "q2", SelectedLabels: []string{}, SelectedLabel: "Docs"},
			protocol.QuestionAnswer{Prompt: "Add what?", QuestionID: "q2", SelectedLabel: "Docs"}},
		{"legacy joined text", protocol.QuestionAnswer{QuestionID: "q2", SelectedLabel: "Docs, CI", Custom: true},
			protocol.QuestionAnswer{Prompt: "Add what?", QuestionID: "q2", SelectedLabel: "Docs, CI", Custom: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateQuestionAnswers(selectQuestions, []protocol.QuestionAnswer{single, tc.in})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got[1], tc.want) {
				t.Fatalf("answer = %+v, want %+v", got[1], tc.want)
			}
		})
	}

	// A single-select question takes selected_labels with exactly one label.
	got, err := validateQuestionAnswers(selectQuestions, []protocol.QuestionAnswer{{QuestionID: "q1", SelectedLabels: []string{"SQLite"}}, {QuestionID: "q2", SelectedLabels: []string{"CI"}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := (protocol.QuestionAnswer{Prompt: "Which DB?", QuestionID: "q1", SelectedLabel: "SQLite", SelectedLabels: []string{"SQLite"}}); !reflect.DeepEqual(got[0], want) {
		t.Fatalf("single-select answer = %+v, want %+v", got[0], want)
	}

	for _, tc := range []struct {
		name    string
		answers []protocol.QuestionAnswer
		want    string
	}{
		{"unknown label", []protocol.QuestionAnswer{single, {QuestionID: "q2", SelectedLabels: []string{"Docs", "Search"}}}, `"Search", which is not one of its option labels`},
		{"label not exact", []protocol.QuestionAnswer{single, {QuestionID: "q2", SelectedLabels: []string{" Docs"}}}, "not one of its option labels"},
		{"duplicate label", []protocol.QuestionAnswer{single, {QuestionID: "q2", SelectedLabels: []string{"CI", "CI"}}}, `"CI" twice`},
		{"two on single-select", []protocol.QuestionAnswer{{QuestionID: "q1", SelectedLabels: []string{"Postgres", "SQLite"}}, {QuestionID: "q2", SelectedLabels: []string{"CI"}}}, "single-select"},
		{"custom beside an option on single-select", []protocol.QuestionAnswer{{QuestionID: "q1", SelectedLabels: []string{"SQLite"}, SelectedLabel: "or both", Custom: true}, {QuestionID: "q2", SelectedLabels: []string{"CI"}}}, "single-select"},
		{"custom without text", []protocol.QuestionAnswer{single, {QuestionID: "q2", SelectedLabels: []string{"CI"}, SelectedLabel: " ", Custom: true}}, "needs the user's text"},
		{"selected_label beside selected_labels", []protocol.QuestionAnswer{single, {QuestionID: "q2", SelectedLabels: []string{"CI"}, SelectedLabel: "CI"}}, "selected_label must be empty"},
		{"custom text too long", []protocol.QuestionAnswer{single, {QuestionID: "q2", SelectedLabels: []string{"CI"}, SelectedLabel: strings.Repeat("x", 2001), Custom: true}}, "exceeds 2000"},
		{"nothing", []protocol.QuestionAnswer{single, {QuestionID: "q2", SelectedLabels: []string{}}}, "cannot be empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validateQuestionAnswers(selectQuestions, tc.answers); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// askingClient calls tool with args, then replies with the tool result it got.
type askingClient struct {
	scriptedClient
	tool, args string
}

func (c *askingClient) Clone() ai.Client { return c }
func (c *askingClient) Stream(_ context.Context, request ai.Request, text, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	for _, message := range request.Messages {
		if message.Role == "tool" && message.Name == c.tool {
			text("RESULT " + message.Content)
			return ai.Message{Role: "assistant", Content: "RESULT " + message.Content, StopReason: ai.StopReasonStop}, ai.Usage{}, nil
		}
	}
	call := ai.ToolCall{ID: "ask-call", Type: "function"}
	call.Function.Name, call.Function.Arguments = c.tool, c.args
	return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{call}, StopReason: ai.StopReasonToolUse}, ai.Usage{}, nil
}

func askingServer(t *testing.T, store *session.Store, eventDir, tool, args string) *httptest.Server {
	t.Helper()
	backend, err := New(Options{Store: store, EventDir: eventDir, MemoryRoot: t.TempDir(), QuestionTimeout: 10 * time.Second, Factory: func(_ context.Context, _ string, m protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(&askingClient{tool: tool, args: args}, m.Model, 1024, "system"), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(backend)
	t.Cleanup(func() { server.Close(); backend.Close() })
	return server
}

// Both question tools take selected_labels over /v1: a bad answer is refused
// and leaves the question open, the model sees the selections as chosen
// options, and question.resolved, tool.result and the call's history (also
// after a restart) carry them as sent.
func TestQuestionSelectedLabelsOverV1(t *testing.T) {
	const questionArgs = `{"questions":[{"question":"Which DB?","options":[{"label":"Postgres"},{"label":"SQLite"}]},{"question":"Add what?","multiple":true,"options":[{"label":"Docs"},{"label":"Search, filter"},{"label":"CI"}]}]}`
	const askArgs = `{"questions":[{"prompt":"Which DB?","options":[{"label":"Postgres"},{"label":"SQLite"}]},{"prompt":"Add what?","multiple":true,"options":[{"label":"Docs"},{"label":"Search, filter"},{"label":"CI"}]}]}`
	answers := []protocol.QuestionAnswer{{QuestionID: "q1", SelectedLabels: []string{"SQLite"}}, {QuestionID: "q2", SelectedLabels: []string{"CI", "Search, filter"}, SelectedLabel: "a badge", Custom: true}}
	recorded := `[{"prompt":"Which DB?","question_id":"q1","selected_label":"SQLite","selected_labels":["SQLite"]},{"prompt":"Add what?","question_id":"q2","selected_label":"a badge","selected_labels":["Search, filter","CI"],"custom":true}]`
	modelSees := "1. Which DB?\n   → SQLite\n2. Add what?\n   → Search, filter, CI; plus a badge (user-typed answer via Other, not a listed option)"
	for _, tc := range []struct{ tool, args string }{{"question", questionArgs}, {"AskUserQuestion", askArgs}} {
		t.Run(tc.tool, func(t *testing.T) {
			store, err := session.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			eventDir := t.TempDir()
			server := askingServer(t, store, eventDir, tc.tool, tc.args)
			sess := createTestSession(t, server.URL)
			run := runRequest(t, server.URL, sess.ID, "selected-labels", "ask me")
			response, err := http.Get(server.URL + "/v1/sessions/" + sess.ID + "/events")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			scanner := bufio.NewScanner(response.Body)
			q := nextQuestion(t, scanner)
			if len(q.Questions) != 2 || !q.Questions[1].Multiple {
				t.Fatalf("request = %+v", q)
			}
			path := server.URL + "/v1/sessions/" + sess.ID + "/questions/" + q.QuestionID
			post := func(answers []protocol.QuestionAnswer) (int, string) {
				r := postJSON(t, http.DefaultClient, path, protocol.QuestionAnswerRequest{ConversationID: sess.ID, RunID: run.RunID, QuestionID: q.QuestionID, Answers: answers}, nil)
				defer r.Body.Close()
				body, _ := io.ReadAll(r.Body)
				return r.StatusCode, string(body)
			}
			bad := []protocol.QuestionAnswer{answers[0], {QuestionID: "q2", SelectedLabels: []string{"Search"}}}
			if code, body := post(bad); code != http.StatusBadRequest || !strings.Contains(body, "not one of its option labels") {
				t.Fatalf("unknown label: %d %s", code, body)
			}
			if code, body := post(answers); code != http.StatusOK {
				t.Fatalf("answer: %d %s", code, body)
			}
			if code, body := post(answers); code != http.StatusOK || !strings.Contains(body, `"duplicate":true`) {
				t.Fatalf("repeated answer: %d %s", code, body)
			}

			var resolution, toolResult json.RawMessage
			var reply strings.Builder
			for scanner.Scan() {
				line := scanner.Text()
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var e protocol.Event
				json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e)
				switch e.Type {
				case protocol.EventQuestionResolved:
					var r struct {
						Answers json.RawMessage `json:"answers"`
					}
					json.Unmarshal(e.Payload, &r)
					resolution = r.Answers
				case protocol.EventToolResult:
					var r struct {
						ToolResult struct {
							QuestionAnswers json.RawMessage `json:"question_answers"`
						} `json:"tool_result"`
					}
					json.Unmarshal(e.Payload, &r)
					toolResult = r.ToolResult.QuestionAnswers
				case protocol.EventTextDelta:
					var d struct {
						Text string `json:"text"`
					}
					json.Unmarshal(e.Payload, &d)
					reply.WriteString(d.Text)
				}
				if e.Type == protocol.EventRunCompleted || e.Type == protocol.EventRunFailed {
					break
				}
			}
			if string(resolution) != recorded {
				t.Errorf("question.resolved answers = %s\nwant %s", resolution, recorded)
			}
			if string(toolResult) != recorded {
				t.Errorf("tool.result question_answers = %s\nwant %s", toolResult, recorded)
			}
			seen := reply.String()
			if tc.tool == "AskUserQuestion" {
				// Its result is the resolution as JSON, text included.
				var result protocol.QuestionResolution
				if err := json.Unmarshal([]byte(strings.TrimPrefix(seen, "RESULT ")), &result); err != nil {
					t.Fatalf("result %q: %v", seen, err)
				}
				if got, _ := json.Marshal(result.Answers); string(got) != recorded {
					t.Errorf("result answers = %s", got)
				}
				seen = result.Text
			}
			if !strings.Contains(seen, modelSees) {
				t.Errorf("model saw %q, want it to contain %q", seen, modelSees)
			}

			historyAnswers := func(base string) string {
				t.Helper()
				var page struct {
					Session struct {
						Messages []struct {
							ToolCalls []struct {
								ID              string          `json:"id"`
								QuestionAnswers json.RawMessage `json:"question_answers"`
							} `json:"tool_calls"`
						} `json:"messages"`
					} `json:"session"`
				}
				if code := doJSON(t, http.MethodGet, base+"/v1/sessions/"+sess.ID+"/history?max_messages=100", nil, &page); code != http.StatusOK {
					t.Fatalf("history status %d", code)
				}
				for _, m := range page.Session.Messages {
					for _, call := range m.ToolCalls {
						if call.ID == "ask-call" {
							return string(call.QuestionAnswers)
						}
					}
				}
				t.Fatalf("history has no ask-call: %+v", page)
				return ""
			}
			if got := historyAnswers(server.URL); got != recorded {
				t.Errorf("history question_answers = %s\nwant %s", got, recorded)
			}
			server.Close()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := session.Open(store.SessionsDir())
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if got := historyAnswers(askingServer(t, reopened, eventDir, tc.tool, tc.args).URL); got != recorded {
				t.Errorf("history after restart question_answers = %s\nwant %s", got, recorded)
			}
		})
	}
}

// A text answer from a client that predates selected_labels keeps its form:
// the joined labels arrive as the user's own words, as before.
func TestQuestionLegacyTextAnswerUnchanged(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := askingServer(t, store, t.TempDir(), "question", `{"question":"Add what?","multiple":true,"options":[{"label":"Docs"},{"label":"CI"}]}`)
	sess := createTestSession(t, server.URL)
	run := runRequest(t, server.URL, sess.ID, "legacy", "ask me")
	response, err := http.Get(server.URL + "/v1/sessions/" + sess.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	q := nextQuestion(t, scanner)
	answer := protocol.QuestionAnswerRequest{ConversationID: sess.ID, RunID: run.RunID, QuestionID: q.QuestionID, Answers: []protocol.QuestionAnswer{{QuestionID: "q1", SelectedLabel: "Docs, CI", Custom: true}}}
	r := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/questions/"+q.QuestionID, answer, nil)
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("answer status %d", r.StatusCode)
	}
	var resolution string
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var e protocol.Event
		json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e)
		if e.Type == protocol.EventQuestionResolved {
			var r struct {
				Answers json.RawMessage `json:"answers"`
			}
			json.Unmarshal(e.Payload, &r)
			resolution = string(r.Answers)
		}
		if e.Type == protocol.EventRunCompleted || e.Type == protocol.EventRunFailed {
			break
		}
	}
	if want := `[{"prompt":"Add what?","question_id":"q1","selected_label":"Docs, CI","custom":true}]`; resolution != want {
		t.Fatalf("answers = %s, want %s", resolution, want)
	}
}

func TestHealthAdvertisesQuestionSelectedLabels(t *testing.T) {
	caps := healthJSON(t, &Server{token: healthTestToken})["capabilities"].(map[string]any)
	if got := jsonInt(t, caps["question_selected_labels"], "question_selected_labels"); got != 1 {
		t.Fatalf("capabilities.question_selected_labels = %d", got)
	}
}
