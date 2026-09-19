package acp

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

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/tools"
	acp "github.com/coder/acp-go-sdk"
)

func TestSessionToolImagesAreIsolatedAndPersisted(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ai.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		identity := ""
		for _, msg := range req.Messages {
			if msg.Role == "user" {
				identity = msg.Content
				break
			}
		}
		if identity != "session-one" && identity != "session-two" {
			t.Errorf("unexpected session: %q", identity)
		}
		followup, images := false, 0
		want := ai.ImagePart("jpg", []byte(identity)).ImageURL.URL
		for _, msg := range req.Messages {
			followup = followup || msg.Role == "tool"
			for _, part := range msg.Parts {
				if part.ImageURL != nil {
					images++
					if part.ImageURL.URL != want {
						t.Errorf("%s received another session's image", identity)
					}
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if followup {
			if images != 1 {
				t.Errorf("followup lost images: %d", images)
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		} else {
			args := fmt.Sprintf(`{"image":%q}`, identity)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"capture-id\",\"type\":\"function\",\"function\":{\"name\":\"capture\",\"arguments\":%q}}]}}]}\n\n", args)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	store := testStore(t)
	f := newFixture(t, nil, store, func(_ context.Context, _ string, _ map[string]mcp.ServerConfig) (*agent.Agent, *mcp.Manager, error) {
		ag := agent.New(ai.New(srv.URL, "key"), "vision", 4096, "sys")
		ag.Vision, ag.BrowserDisabled, ag.ComputerDisabled = true, true, true
		ag.MaxTurns = 3
		ag.Tools = []tools.Tool{{Def: ai.NewTool("capture", "", `{"type":"object"}`), Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var input struct{ Image string }
			if err := json.Unmarshal(args, &input); err != nil {
				return "", err
			}
			tools.AttachScreenshot(ctx, []byte(input.Image))
			return "captured", nil
		}}}
		return ag, mcp.NewManager(nil), nil
	})
	f.initialize(t)
	dir := t.TempDir()
	ids := []acp.SessionId{f.newSession(t, dir), f.newSession(t, dir)}
	names := []string{"session-one", "session-two"}
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Go(func() {
			if _, err := f.prompt(t, id, names[i]); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for i, id := range ids {
		_, history, err := store.Load(string(id))
		if err != nil {
			t.Fatal(err)
		}
		images := 0
		for _, msg := range history {
			for _, part := range msg.Parts {
				if part.ImageURL != nil {
					images++
					if msg.Role != "tool" || msg.ToolCallID != "capture-id" || !strings.HasSuffix(part.ImageURL.URL, base64.StdEncoding.EncodeToString([]byte(names[i]))) {
						t.Fatalf("wrong persisted image: %+v", msg)
					}
				}
			}
		}
		if images != 1 {
			t.Fatalf("persisted images=%d", images)
		}
		if _, err := f.conn.CloseSession(t.Context(), acp.CloseSessionRequest{SessionId: id}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.conn.LoadSession(t.Context(), acp.LoadSessionRequest{SessionId: id, Cwd: dir, McpServers: []acp.McpServer{}}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.prompt(t, id, "continue"); err != nil {
			t.Fatal(err)
		}
	}
}
