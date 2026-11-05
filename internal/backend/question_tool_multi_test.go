package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// agentQuestionClient calls the agent's own question tool with the given
// arguments, then reports the tool result it got back as its reply.
type agentQuestionClient struct {
	scriptedClient
	args string
}

func (c *agentQuestionClient) Clone() ai.Client { return c }
func (c *agentQuestionClient) Stream(_ context.Context, request ai.Request, text, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	for _, message := range request.Messages {
		if message.Role == "tool" && message.Name == "question" {
			if strings.HasPrefix(message.Content, "Error") {
				return ai.Message{}, ai.Usage{}, fmt.Errorf("question failed: %s", message.Content)
			}
			text("RESULT " + message.Content)
			return ai.Message{Role: "assistant", Content: "RESULT " + message.Content, StopReason: ai.StopReasonStop}, ai.Usage{}, nil
		}
	}
	call := ai.ToolCall{ID: "agent-question", Type: "function"}
	call.Function.Name, call.Function.Arguments = "question", c.args
	return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{call}, StopReason: ai.StopReasonToolUse}, ai.Usage{}, nil
}

// The question tool's single and multi-question forms both reach /v1 clients
// as the question.requested shape AskUserQuestion already uses, and the
// answers come back to the model one per question.
func TestAgentQuestionToolFormsOverV1(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    string
		prompts []string
		headers []string
		answers []protocol.QuestionAnswer
		want    []string
	}{
		{"single", `{"question":"Which DB?","options":[{"label":"Postgres"},{"label":"SQLite"}]}`,
			[]string{"Which DB?"}, []string{""},
			[]protocol.QuestionAnswer{{QuestionID: "q1", SelectedLabel: "SQLite"}},
			[]string{`User answered "Which DB?": SQLite`}},
		{"multi", `{"questions":[{"header":"Format","question":"Which format?","options":[{"label":"JSON"},{"label":"YAML"}]},{"header":"Tests","question":"Which tests?","options":[{"label":"unit"},{"label":"e2e"}]}]}`,
			[]string{"Which format?", "Which tests?"}, []string{"Format", "Tests"},
			[]protocol.QuestionAnswer{{QuestionID: "q1", SelectedLabel: "YAML"}, {QuestionID: "q2", SelectedLabel: "only the fast ones", Custom: true}},
			[]string{"1. Which format?\n   → YAML", "2. Which tests?\n   → only the fast ones"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := session.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			backend, err := New(Options{Store: store, EventDir: t.TempDir(), MemoryRoot: t.TempDir(), QuestionTimeout: 5 * time.Second, Factory: func(_ context.Context, _ string, m protocol.ModelRef) (*agent.Agent, error) {
				return agent.New(&agentQuestionClient{args: tc.args}, m.Model, 1024, "system"), nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(backend)
			t.Cleanup(func() { backend.Close(); server.Close(); store.Close() })

			sess := createTestSession(t, server.URL)
			run := runRequest(t, server.URL, sess.ID, "question-run", "ask me")
			response, err := http.Get(server.URL + "/v1/sessions/" + sess.ID + "/events")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			scanner := bufio.NewScanner(response.Body)
			q := nextQuestion(t, scanner)
			if q.ToolCallID != "agent-question" || len(q.Questions) != len(tc.prompts) {
				t.Fatalf("request = %+v", q)
			}
			for i, question := range q.Questions {
				if question.Prompt != tc.prompts[i] || question.Header != tc.headers[i] || question.ID != fmt.Sprintf("q%d", i+1) {
					t.Errorf("question %d = %+v", i, question)
				}
			}
			answer := protocol.QuestionAnswerRequest{ConversationID: sess.ID, RunID: run.RunID, QuestionID: q.QuestionID, Answers: tc.answers}
			r := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/questions/"+q.QuestionID, answer, nil)
			r.Body.Close()
			if r.StatusCode != 200 {
				t.Fatalf("answer status %d", r.StatusCode)
			}
			var reply strings.Builder
			for scanner.Scan() {
				line := scanner.Text()
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var e protocol.Event
				json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e)
				if e.Type == protocol.EventTextDelta {
					var delta struct {
						Text string `json:"text"`
					}
					json.Unmarshal(e.Payload, &delta)
					reply.WriteString(delta.Text)
				}
				if e.Type == protocol.EventRunCompleted || e.Type == protocol.EventRunFailed {
					break
				}
			}
			for _, want := range tc.want {
				if !strings.Contains(reply.String(), want) {
					t.Errorf("model saw %q, want it to contain %q", reply.String(), want)
				}
			}
		})
	}
}
