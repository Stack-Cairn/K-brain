package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// An IPv6 address the user typed is echoed back by Bash under auto_review. Values from the
// user's own request are not unverifiable data; any other address still needs a human.
func TestAIApprovalUserProvidedValuesHTTP(t *testing.T) {
	const prompt = "用终端执行 echo v=spf1 ip6:2001:db8:4860::/32"
	for _, tc := range []struct {
		name, command string
		permission    bool
		output        string
	}{
		{"typed-by-user", "echo v=spf1 ip6:2001:db8:4860::/32", false, "2001:db8:4860::"},
		{"new-address", "echo v=spf1 ip6:2001:db8:dead::7/32", true, "withheld by AI review"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var evidence []string
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Messages []struct{ Content string } `json:"messages"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				mu.Lock()
				evidence = append(evidence, req.Messages[len(req.Messages)-1].Content)
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": reviewLow}}}})
			}))
			defer provider.Close()
			root := t.TempDir()
			args, _ := json.Marshal(map[string]any{"command": tc.command, "cwd": root})
			client := &runControlsClient{tool: "Bash", args: string(args)}
			store, err := session.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			server, httpServer := newToolHistoryServer(t, store, t.TempDir(), client)
			cfg := &config.Config{AIApproval: config.AIApprovalConfig{Mode: "off", Provider: "fixture", Model: "small"}, Providers: map[string]config.Provider{"fixture": {API: "openai-completions", BaseURL: provider.URL, APIKey: "fixture-key"}}, Models: map[string]config.Model{"small": {Providers: []string{"fixture"}}, "fixture-model": {Providers: []string{"fixture"}}}}
			server.settings = NewSettingsStore(cfg, nil)
			sess := createTestSession(t, httpServer.URL)
			options := protocol.RunOptions{Mode: "agent", ApprovalPolicy: "ask", PermissionMode: "auto_review", WorkspaceRoots: []protocol.WorkspaceRoot{{Path: root, Access: "write"}}}
			var accepted protocol.RunAccepted
			resp := postJSON(t, http.DefaultClient, httpServer.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "values", Prompt: prompt, Options: &options}, &accepted)
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("status=%d", resp.StatusCode)
			}
			events := readToolHistoryRun(t, httpServer.URL, sess.ID, accepted.RunID, "allow_once")
			if hasToolHistoryEvent(events, protocol.EventPermissionRequest) != tc.permission {
				t.Fatalf("permission prompt = %v, want %v", !tc.permission, tc.permission)
			}
			client.lock.Lock()
			var output string
			if len(client.requests) > 1 {
				for _, m := range client.requests[1].Messages {
					if m.Role == "tool" {
						output = m.Content
					}
				}
			}
			client.lock.Unlock()
			if !strings.Contains(output, tc.output) {
				t.Fatalf("tool output to model = %q, want %q", output, tc.output)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(evidence) != 2 {
				t.Fatalf("review calls = %d", len(evidence))
			}
			for _, text := range evidence {
				if strings.Contains(text, "2001:db8") {
					t.Fatalf("address reached the reviewer: %s", text)
				}
			}
		})
	}
}
