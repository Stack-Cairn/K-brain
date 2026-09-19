package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func captureTool() tools.Tool {
	return tools.Tool{Def: ai.NewTool("capture", "", `{"type":"object"}`), Run: func(ctx context.Context, args json.RawMessage) (string, error) {
		var input struct{ Image string }
		if err := json.Unmarshal(args, &input); err != nil {
			return "", err
		}
		tools.AttachScreenshot(ctx, []byte(input.Image))
		return "captured", nil
	}}
}

func imageCall(id string) ai.ToolCall {
	call := ai.ToolCall{ID: id, Type: "function"}
	call.Function.Name = "capture"
	call.Function.Arguments = fmt.Sprintf(`{"image":%q}`, id)
	return call
}

func TestParallelAgentsAndSubagentsKeepOwnImages(t *testing.T) {
	parent := New(ai.New("http://unused", "key"), "vision", 100, "sys")
	parent.Vision = true
	parent.Tools = []tools.Tool{captureTool()}
	inherited := parent.newSub(SubModel{})
	parent.TaskDefault = SubModel{Client: parent.Client, Model: "text"}
	textSub := parent.newSub(SubModel{})
	overridden := parent.newSub(SubModel{Client: parent.Client, Model: "other", Vision: true})
	if err := parent.SetModel(ModelConfig{Client: parent.Client.Clone(), ID: "text"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i, ag := range []*Agent{parent, inherited, textSub, overridden} {
		wg.Go(func() {
			calls := []ai.ToolCall{imageCall(fmt.Sprintf("agent-%d-first", i)), imageCall(fmt.Sprintf("agent-%d-second", i))}
			results := ag.runTools(t.Context(), calls, Events{})
			for j, result := range results {
				wantImage := i == 1 || i == 3
				if (len(result.Parts) == 1) != wantImage {
					t.Errorf("agent %d: unexpected images %+v", i, result)
					continue
				}
				if wantImage && result.Parts[0].ImageURL.URL != ai.ImagePart("jpg", []byte(calls[j].ID)).ImageURL.URL {
					t.Errorf("agent %d call %d received another call's image", i, j)
				}
			}
			if len(ag.drainPending()) != 0 {
				t.Error("screenshots were injected as steering")
			}
		})
	}
	wg.Wait()
}

func TestToolImagesReachFollowupAcrossProtocols(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			requests := make(chan string, 4)
			calls := []ai.ToolCall{imageCall("first-image"), imageCall("second-image")}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				requests <- string(body)
				w.Header().Set("Content-Type", "text/event-stream")
				send := func(event any) {
					b, _ := json.Marshal(event)
					fmt.Fprintf(w, "data: %s\n\n", b)
				}
				followup := strings.Contains(string(body), "captured")
				switch protocol {
				case "responses":
					output := []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "done"}}}}
					if !followup {
						output = nil
						for _, call := range calls {
							output = append(output, map[string]any{"type": "function_call", "id": "item-" + call.ID, "call_id": call.ID, "name": "capture", "arguments": call.Function.Arguments})
						}
					}
					send(map[string]any{"type": "response.completed", "response": map[string]any{"output": output}})
				case "anthropic":
					send(map[string]any{"type": "message_start", "message": map[string]any{}})
					if followup {
						send(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": "done"}})
						send(map[string]any{"type": "content_block_stop", "index": 0})
					} else {
						for i, call := range calls {
							send(map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "tool_use", "id": call.ID, "name": "capture", "input": json.RawMessage(call.Function.Arguments)}})
							send(map[string]any{"type": "content_block_stop", "index": i})
						}
					}
					send(map[string]any{"type": "message_stop"})
				default:
					if followup {
						fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
					} else {
						for i, call := range calls {
							fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":%d,\"id\":%q,\"type\":\"function\",\"function\":{\"name\":\"capture\",\"arguments\":%q}}]}}]}\n\n", i, call.ID, call.Function.Arguments)
						}
					}
					fmt.Fprint(w, "data: [DONE]\n\n")
				}
			}))
			defer srv.Close()
			var client ai.Client = ai.New(srv.URL, "key")
			if protocol == "responses" {
				client = ai.NewResponses(srv.URL, "key")
			} else if protocol == "anthropic" {
				client = ai.NewAnthropic(srv.URL, "key")
			}
			ag := New(client, "vision", 100, "sys")
			ag.Vision, ag.MaxTurns = true, 3
			ag.Tools = []tools.Tool{captureTool()}
			if out, err := ag.Turn(t.Context(), "inspect", Events{}); err != nil || out != "done" {
				t.Fatalf("turn: %q, %v", out, err)
			}
			<-requests
			followup := <-requests
			for i, call := range calls {
				encoded := base64.StdEncoding.EncodeToString([]byte(call.ID))
				if strings.Count(followup, encoded) != 1 {
					t.Fatalf("missing or duplicate screenshot %s in %s", call.ID, followup)
				}
				msg := ag.Messages[3+i]
				if msg.Role != "tool" || msg.ToolCallID != call.ID || len(msg.Parts) != 1 || !strings.Contains(msg.Parts[0].ImageURL.URL, encoded) {
					t.Fatalf("screenshot attached to wrong history entry: %+v", msg)
				}
			}
		})
	}
}
