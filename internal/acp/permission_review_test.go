package acp

import (
	"context"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// Review P2: before this change ACP offered "Always allow" for `git status` and the
// second call ran without a prompt. Suggest now skips read-only segments while ACP
// evaluates without AllowReadOnly, so a read-only command, or a command with a
// read-only segment, prompts on every call.
func TestReviewAllowAlwaysCoversReadOnlySegments(t *testing.T) {
	for _, command := range []string{"git status", "make build && git status"} {
		t.Run(command, func(t *testing.T) {
			srv := scriptServer(t, []step{
				{toolName: "probe", toolArgs: `{}`},
				{toolName: "probe", toolArgs: `{}`},
				{text: "done"},
			})
			ran := false
			client := &fakeClient{
				answer: func(req acp.RequestPermissionRequest) permAnswer {
					for _, o := range req.Options {
						if o.Kind == acp.PermissionOptionKindAllowAlways {
							return permAnswer{optionID: optAllowAlways}
						}
					}
					return permAnswer{optionID: optAllowOnce}
				},
			}
			f := newFixture(t, client, nil, factoryFor(srv, []tools.Tool{commandProbe("probe", command, &ran)}))
			f.initialize(t)
			id := f.newSession(t, t.TempDir())
			if _, err := f.conn.SetSessionMode(context.Background(), acp.SetSessionModeRequest{SessionId: id, ModeId: ModeAsk}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.prompt(t, id, "twice"); err != nil {
				t.Fatal(err)
			}
			if len(client.perms) == 0 {
				t.Fatal("no permission request")
			}
			offered := false
			for _, o := range client.perms[0].Options {
				offered = offered || o.Kind == acp.PermissionOptionKindAllowAlways
			}
			if !offered || len(client.perms) != 1 {
				t.Errorf("%q: allow-always offered=%v, permission requests=%d; want offered and 1", command, offered, len(client.perms))
			}
		})
	}
}
