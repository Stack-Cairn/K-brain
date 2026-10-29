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

// The user-identifier exemption is limited to EMAIL/PHONE/LANDLINE/IP/IPV6/MAC. An ID card
// number (valid checksum, born 1990) typed by the user and echoed by Bash under auto_review
// is not one of them and must still need a human; its output must be withheld.
func TestAIApprovalUserIDCardStillNeedsHumanHTTP(t *testing.T) {
	const idCard = "110105199003071239"
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
	args, _ := json.Marshal(map[string]any{"command": "echo id=" + idCard, "cwd": root})
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
	resp := postJSON(t, http.DefaultClient, httpServer.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "idcard", Prompt: "用终端回显我的身份证号 " + idCard, Options: &options}, &accepted)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	events := readToolHistoryRun(t, httpServer.URL, sess.ID, accepted.RunID, "allow_once")
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
	mu.Lock()
	seen := strings.Join(evidence, "\n")
	mu.Unlock()
	if !hasToolHistoryEvent(events, protocol.EventPermissionRequest) {
		t.Errorf("ID card action auto-approved without a permission request; reviewer saw:\n%s", seen)
	}
	if strings.Contains(output, idCard) {
		t.Errorf("ID card output reached the main model unwithheld: %q", output)
	}
}
