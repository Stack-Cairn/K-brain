package acp

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestToolCallUpdateCarriesRunMeta(t *testing.T) {
	dir := t.TempDir()
	srv := scriptServer(t, []step{
		{toolName: "bash", toolArgs: `{"command":"exit 2"}`},
		{text: "done"},
	})
	f := newFixture(t, &fakeClient{}, nil, factoryFor(srv, tools.All()))
	f.initialize(t)
	id := f.newSession(t, dir)
	if _, err := f.prompt(t, id, "run it"); err != nil {
		t.Fatal(err)
	}
	n := f.client.waitFor(t, func(n acp.SessionNotification) bool {
		u := n.Update.ToolCallUpdate
		return u != nil && u.Status != nil && *u.Status != acp.ToolCallStatusInProgress
	}, "finished tool_call_update")
	info, _ := n.Update.ToolCallUpdate.Meta["kbrain"].(map[string]any)
	// The update crossed the JSON wire, so numbers arrive as float64.
	if info == nil || info["exit_code"] != float64(2) {
		t.Fatalf("_meta = %+v", n.Update.ToolCallUpdate.Meta)
	}
	if ms, _ := info["duration_ms"].(float64); ms < 1 {
		t.Fatalf("duration_ms = %v", info["duration_ms"])
	}
}

func TestWithRunMetaOmitsUnknown(t *testing.T) {
	u := withRunMeta(endToolCall("c1", "read", `{}`, "text"), nil, false, 0)
	if u.ToolCallUpdate.Meta != nil {
		t.Fatalf("meta = %+v", u.ToolCallUpdate.Meta)
	}
	u = withRunMeta(endToolCall("c1", "bash", `{}`, "Error: timed out"), nil, true, 1000)
	info := u.ToolCallUpdate.Meta["kbrain"].(map[string]any)
	if _, ok := info["exit_code"]; ok || info["timed_out"] != true || info["duration_ms"] != int64(1000) {
		t.Fatalf("meta = %+v", info)
	}
}

func TestPermissionPromptShowsEditPreview(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	srv := scriptServer(t, []step{
		{toolName: "write", toolArgs: `{"path":` + strconv.Quote(path) + `,"content":"hello\n"}`},
		{text: "done"},
	})
	f := newFixture(t, &fakeClient{
		answer: func(acp.RequestPermissionRequest) permAnswer { return permAnswer{optionID: optReject} },
	}, nil, factoryFor(srv, tools.All()))
	f.initialize(t)
	id := f.newSession(t, dir)
	if _, err := f.conn.SetSessionMode(context.Background(), acp.SetSessionModeRequest{SessionId: id, ModeId: ModeAsk}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.prompt(t, id, "write it"); err != nil {
		t.Fatal(err)
	}
	f.client.mu.Lock()
	defer f.client.mu.Unlock()
	if len(f.client.perms) != 1 {
		t.Fatalf("permission requests = %d", len(f.client.perms))
	}
	call := f.client.perms[0].ToolCall
	if len(call.Locations) != 1 || call.Locations[0].Path != path {
		t.Fatalf("locations = %+v", call.Locations)
	}
	if len(call.Content) != 1 || call.Content[0].Content == nil || call.Content[0].Content.Content.Text == nil ||
		!strings.Contains(call.Content[0].Content.Content.Text.Text, "1 + hello") {
		t.Fatalf("content = %+v", call.Content)
	}
}
