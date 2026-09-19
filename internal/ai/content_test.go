package ai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func imageURLPart(url string) ContentPart {
	p := ContentPart{Type: "image_url"}
	p.ImageURL = &struct {
		URL string `json:"url"`
	}{URL: url}
	return p
}

func TestMessageMixedContentRoundTrip(t *testing.T) {
	parts := []ContentPart{{Type: "text", Text: "before"}, ImagePart("png", pngFixture(t, 3, 2)), {Type: "text", Text: "after"}, imageURLPart("https://example.test/image.png"), {Type: "text", Text: "last"}}
	for _, input := range []Message{
		{Role: "user", Parts: parts},
		{Role: "user", Content: "prefix", Parts: parts},
		{Role: "user", Parts: parts[1:]},
		{Role: "user", Parts: append([]ContentPart{{Type: "text", Text: ""}}, parts...)},
	} {
		want := input.ContentParts()
		for range 3 {
			data, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			var restored Message
			if err := json.Unmarshal(data, &restored); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(restored.ContentParts(), want) || restored.TextContent() != input.TextContent() {
				t.Fatalf("round trip lost content: %+v", restored)
			}
			input = restored
		}
	}
	var reused Message
	for _, raw := range []string{`{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.test/image.png"}}]}`, `{"role":"user","content":"plain"}`, `{"role":"assistant","content":null}`, `{"role":"assistant"}`} {
		if err := json.Unmarshal([]byte(raw), &reused); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(raw, "image_url") && len(reused.Parts) != 0 {
			t.Fatal("stale image retained on reused message")
		}
		if !strings.Contains(raw, "plain") && !strings.Contains(raw, "image_url") && reused.Content != "" {
			t.Fatal("stale text retained")
		}
	}
}

func contentResponse(w http.ResponseWriter, protocol string, stream bool) {
	if !stream {
		switch protocol {
		case "responses":
			fmt.Fprint(w, `{"status":"completed","output":[{"type":"message","content":[{"text":"ok"}]}]}`)
		case "anthropic":
			fmt.Fprint(w, `{"content":[{"type":"text","text":"ok"}]}`)
		default:
			fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
		}
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	switch protocol {
	case "responses":
		fmt.Fprint(w, streamFrames(`{"type":"response.completed","response":{"output":[{"type":"message","content":[{"text":"ok"}]}]}}`))
	case "anthropic":
		fmt.Fprint(w, streamFrames(`{"type":"message_start","message":{}}`, `{"type":"content_block_start","content_block":{"type":"text","text":"ok"}}`, `{"type":"message_stop"}`))
	default:
		fmt.Fprint(w, streamFrames(`{"choices":[{"delta":{"content":"ok"}}]}`, `[DONE]`))
	}
}

func TestMultimodalRequestsAcrossProtocols(t *testing.T) {
	image := ImagePart("png", pngFixture(t, 3, 2))
	remote := imageURLPart("https://example.test/image.png")
	input := Message{Role: "user", Content: "before", Parts: []ContentPart{image, {Type: "text", Text: "between"}, remote, {Type: "text", Text: "after"}}}
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", protocol, stream), func(t *testing.T) {
				captured := make(chan map[string]any, 1)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					captured <- body
					contentResponse(w, protocol, stream)
				}))
				defer srv.Close()
				c := streamClient(protocol, srv.URL)
				req := Request{Model: "model", Messages: []Message{input}}
				var err error
				if stream {
					_, _, err = c.Stream(t.Context(), req, nil, nil, nil)
				} else {
					_, _, err = c.Complete(t.Context(), req)
				}
				if err != nil {
					t.Fatal(err)
				}
				body := <-captured
				field := "messages"
				if protocol == "responses" {
					field = "input"
				}
				messages := body[field].([]any)
				blocks := messages[0].(map[string]any)["content"].([]any)
				if len(blocks) != 5 {
					t.Fatalf("content lost: %+v", blocks)
				}
				for i, want := range map[int]string{0: "before", 2: "between", 4: "after"} {
					if blocks[i].(map[string]any)["text"] != want {
						t.Fatalf("text reordered: %+v", blocks)
					}
				}
				for i, part := range map[int]ContentPart{1: image, 3: remote} {
					block := blocks[i].(map[string]any)
					if block["w"] != nil || block["h"] != nil {
						t.Fatalf("local dimensions leaked: %v", block)
					}
					switch protocol {
					case "chat":
						if block["type"] != "image_url" || block["image_url"].(map[string]any)["url"] != part.ImageURL.URL {
							t.Fatalf("bad Chat image: %v", block)
						}
					case "responses":
						if block["type"] != "input_image" || block["image_url"] != part.ImageURL.URL {
							t.Fatalf("bad Responses image: %v", block)
						}
					case "anthropic":
						source := block["source"].(map[string]any)
						if block["type"] != "image" {
							t.Fatalf("bad Anthropic image: %v", block)
						}
						if i == 1 && (source["type"] != "base64" || source["media_type"] != "image/png" || "data:image/png;base64,"+source["data"].(string) != part.ImageURL.URL) {
							t.Fatalf("bad base64 image: %v", source)
						}
						if i == 3 && (source["type"] != "url" || source["url"] != part.ImageURL.URL) {
							t.Fatalf("bad remote image: %v", source)
						}
					}
				}
				if input.Parts[0].W != 3 || input.Parts[0].H != 2 {
					t.Fatal("request modified source dimensions")
				}
			})
		}
	}
}

func TestInvalidImageFailsBeforeRequest(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		for _, part := range []ContentPart{{Type: "image_url"}, imageURLPart("file:///private/image.png"), imageURLPart("data:image/png;base64,!!!"), {Type: "audio"}} {
			t.Run(protocol+"/"+part.Type, func(t *testing.T) {
				var requests atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1) }))
				defer srv.Close()
				c := streamClient(protocol, srv.URL)
				req := Request{Messages: []Message{{Role: "user", Parts: []ContentPart{part}}}}
				if _, _, err := c.Stream(t.Context(), req, nil, nil, nil); err == nil {
					t.Fatal("stream accepted invalid image")
				}
				if _, _, err := c.Complete(t.Context(), req); err == nil {
					t.Fatal("complete accepted invalid image")
				}
				if requests.Load() != 0 {
					t.Fatalf("sent %d invalid requests", requests.Load())
				}
			})
		}
	}
}

func TestAnthropicSystemAndParallelToolResults(t *testing.T) {
	call := func(id string) ToolCall {
		tc := ToolCall{ID: id, Type: "function"}
		tc.Function.Name = "status"
		tc.Function.Arguments = "{}"
		return tc
	}
	image := ImagePart("png", pngFixture(t, 2, 2))
	messages := []Message{
		{Role: "system", Content: "base", Parts: []ContentPart{{Type: "text", Text: " rules"}}},
		{Role: "developer", Content: "memory"},
		{Role: "user", Content: "inspect"},
		{Role: "assistant", ToolCalls: []ToolCall{call("first"), call("second")}},
		{Role: "tool", ToolCallID: "first", Content: "result", Parts: []ContentPart{image, {Type: "text", Text: "caption"}}},
		{Role: "tool", ToolCallID: "second", Content: "second result"},
		{Role: "system", Content: "todos"},
	}
	for _, retention := range []string{"none", "short"} {
		p, err := anthropicPayload(Request{Messages: messages, PromptCacheRetention: retention}, true)
		if err != nil {
			t.Fatal(err)
		}
		system := p["system"].([]any)
		if len(system) != 4 {
			t.Fatalf("system instructions lost: %+v", system)
		}
		for i, want := range []string{"base", " rules", "memory", "todos"} {
			block := system[i].(map[string]any)
			if block["text"] != want {
				t.Fatalf("system order: %+v", system)
			}
			if (block["cache_control"] != nil) != (retention == "short" && i == 3) {
				t.Fatalf("system cache boundary: %v", system)
			}
		}
		out := p["messages"].([]any)
		if len(out) != 3 {
			t.Fatalf("parallel results not grouped: %+v", out)
		}
		blocks := out[2].(map[string]any)["content"].([]any)
		if len(blocks) != 2 {
			t.Fatalf("missing result: %+v", blocks)
		}
		first := blocks[0].(map[string]any)
		parts := first["content"].([]any)
		if first["tool_use_id"] != "first" || len(parts) != 3 || parts[1].(map[string]any)["type"] != "image" || blocks[1].(map[string]any)["tool_use_id"] != "second" {
			t.Fatalf("result content lost: %+v", blocks)
		}
		if (blocks[1].(map[string]any)["cache_control"] != nil) != (retention == "short") {
			t.Fatalf("history cache boundary: %+v", blocks)
		}
	}
}

func TestToolImagesPreservePairingAcrossProtocols(t *testing.T) {
	image := ImagePart("png", pngFixture(t, 2, 2))
	messages := []Message{{Role: "tool", ToolCallID: "one", Name: "screenshot", Content: "first", Parts: []ContentPart{image, {Type: "text", Text: " caption"}}}, {Role: "tool", ToolCallID: "two", Name: "screenshot", Parts: []ContentPart{image}}}
	chat, err := chatMessages(messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(chat) != 3 || chat[0].Role != "tool" || chat[1].Role != "tool" || chat[2].Role != "user" || chat[0].Content != "first caption" || len(chat[0].Parts) != 0 || len(chat[2].Parts) != 4 {
		t.Fatalf("Chat tool pairing lost: %+v", chat)
	}
	responses, err := responsesInput(messages)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"one", "two"} {
		output := responses[i].(map[string]any)
		if output["call_id"] != id || output["type"] != "function_call_output" {
			t.Fatalf("Responses pairing: %+v", output)
		}
		parts := output["output"].([]any)
		imageIndex := 0
		if i == 0 {
			imageIndex = 1
		}
		if parts[imageIndex].(map[string]any)["image_url"] != image.ImageURL.URL {
			t.Fatalf("lost tool image: %+v", parts)
		}
	}
	orphan := repairToolHistory(messages[:1])
	if len(orphan) != 1 || len(orphan[0].Parts) != 2 || orphan[0].Role != "user" {
		t.Fatalf("repair lost orphan image: %+v", orphan)
	}
}
