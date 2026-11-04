package acp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// Plan mode keeps web_fetch but still asks before each fetch, offering to remember
// the host; a rejected fetch never reaches the server.
func TestPlanModeAsksBeforeWebFetch(t *testing.T) {
	for _, option := range []string{optAllowOnce, optReject} {
		t.Run(option, func(t *testing.T) {
			var hits atomic.Int32
			page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "text/plain")
				fmt.Fprint(w, "planning notes")
			}))
			defer page.Close()
			srv := scriptServer(t, []step{
				{toolName: "web_fetch", toolArgs: fmt.Sprintf(`{"url":%q}`, page.URL+"/notes")},
				{text: "done"},
			})
			f := newFixture(t, &fakeClient{answer: func(acp.RequestPermissionRequest) permAnswer {
				return permAnswer{optionID: option}
			}}, nil, factoryFor(srv, nil))
			f.initialize(t)
			id := f.newSession(t, t.TempDir())
			if _, err := f.conn.SetSessionMode(context.Background(), acp.SetSessionModeRequest{SessionId: id, ModeId: ModePlan}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.prompt(t, id, "research"); err != nil {
				t.Fatal(err)
			}
			if len(f.client.perms) != 1 {
				t.Fatalf("permission requests = %d, want 1", len(f.client.perms))
			}
			perm := f.client.perms[0]
			if perm.ToolCall.Kind == nil || *perm.ToolCall.Kind != acp.ToolKindFetch {
				t.Errorf("permission kind = %v, want fetch", perm.ToolCall.Kind)
			}
			always := false
			for _, o := range perm.Options {
				always = always || (o.Kind == acp.PermissionOptionKindAllowAlways && strings.Contains(o.Name, "WebFetch(domain:127.0.0.1)"))
			}
			if !always {
				t.Errorf("options should offer to remember the host: %+v", perm.Options)
			}
			var result string
			for _, msg := range f.bridge.getSession(id).ag.MessagesSnapshot() {
				if msg.Role == "tool" {
					result = msg.Content
				}
			}
			if option == optAllowOnce {
				if hits.Load() != 1 || !strings.Contains(result, "planning notes") {
					t.Fatalf("allowed fetch: hits=%d result=%q", hits.Load(), result)
				}
				return
			}
			if hits.Load() != 0 || !strings.Contains(result, "Permission denied") {
				t.Fatalf("rejected fetch: hits=%d result=%q", hits.Load(), result)
			}
		})
	}
}
