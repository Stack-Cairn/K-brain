package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	acp "github.com/coder/acp-go-sdk"
)

func TestPromptAndReplayKeepImageOrder(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("png"))
	for _, blocks := range [][]acp.ContentBlock{
		{acp.TextBlock("first"), acp.ImageBlock(encoded, "image/png"), acp.TextBlock("last")},
		{acp.ImageBlock(encoded, "image/png"), acp.TextBlock("last")},
	} {
		text, parts := promptFromBlocks(blocks, true)
		updates := replayUpdates([]ai.Message{{Role: "user", Content: text, Parts: parts}})
		if len(updates) != len(blocks) {
			t.Fatalf("replay lost blocks: %+v", updates)
		}
		for i, block := range blocks {
			if !reflect.DeepEqual(updates[i].UserMessageChunk.Content, block) {
				t.Fatalf("block %d changed: %+v", i, updates[i].UserMessageChunk.Content)
			}
		}
	}
}

func TestMultimodalPromptPersistsAndReloadsAcrossProtocols(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			captured := make(chan map[string]any, 2)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]any
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				captured <- req
				w.Header().Set("Content-Type", "text/event-stream")
				switch protocol {
				case "responses":
					fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\",\"content\":[{\"text\":\"ok\"}]}]}}\n\n")
				case "anthropic":
					fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{}}\n\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"text\",\"text\":\"ok\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
				default:
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
				}
			}))
			defer srv.Close()
			st := testStore(t)
			dir := t.TempDir()
			f := newFixture(t, nil, st, func(_ context.Context, _ string, _ map[string]mcp.ServerConfig) (*agent.Agent, *mcp.Manager, error) {
				var c ai.Client = ai.New(srv.URL, "key")
				if protocol == "responses" {
					c = ai.NewResponses(srv.URL, "key")
				}
				if protocol == "anthropic" {
					c = ai.NewAnthropic(srv.URL, "key")
				}
				ag := agent.New(c, "model", 4096, "sys")
				ag.BrowserDisabled, ag.ComputerDisabled = true, true
				return ag, mcp.NewManager(nil), nil
			})
			f.bridge.vision = true
			f.initialize(t)
			sid := f.newSession(t, dir)
			encoded := base64.StdEncoding.EncodeToString([]byte("png"))
			blocks := []acp.ContentBlock{acp.TextBlock("before"), acp.ImageBlock(encoded, "image/png"), acp.TextBlock("after")}
			if _, err := f.conn.Prompt(t.Context(), acp.PromptRequest{SessionId: sid, Prompt: blocks}); err != nil {
				t.Fatal(err)
			}
			first := <-captured
			_, history, err := st.Load(string(sid))
			if err != nil {
				t.Fatal(err)
			}
			var user ai.Message
			for _, m := range history {
				if m.Role == "user" {
					user = m
					break
				}
			}
			parts := user.ContentParts()
			if len(parts) != 3 || parts[0].Text != "before" || parts[1].ImageURL == nil || parts[2].Text != "after" {
				t.Fatalf("stored content lost: %+v", user)
			}
			if _, err := f.conn.CloseSession(t.Context(), acp.CloseSessionRequest{SessionId: sid}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.conn.LoadSession(t.Context(), acp.LoadSessionRequest{SessionId: sid, Cwd: dir, McpServers: []acp.McpServer{}}); err != nil {
				t.Fatal(err)
			}
			f.client.waitFor(t, func(n acp.SessionNotification) bool {
				return n.Update.UserMessageChunk != nil && n.Update.UserMessageChunk.Content.Image != nil
			}, "replayed image")
			if _, err := f.prompt(t, sid, "continue"); err != nil {
				t.Fatal(err)
			}
			second := <-captured
			field := "messages"
			if protocol == "responses" {
				field = "input"
			}
			findUser := func(req map[string]any) any {
				for _, v := range req[field].([]any) {
					m := v.(map[string]any)
					if m["role"] == "user" {
						return m["content"]
					}
				}
				return nil
			}
			firstUser, secondUser := findUser(first), findUser(second)
			firstParts, ok := firstUser.([]any)
			if !ok || len(firstParts) != 3 {
				t.Fatalf("wire image lost: %+v", firstUser)
			}
			for _, raw := range []any{firstUser, secondUser} {
				for _, v := range raw.([]any) {
					delete(v.(map[string]any), "cache_control")
				}
			}
			if !reflect.DeepEqual(firstUser, secondUser) {
				t.Fatalf("resumed request changed image content: first=%+v second=%+v", firstUser, secondUser)
			}
		})
	}
}
