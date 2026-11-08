package sshmgr

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/sshhosts"
	"github.com/Stack-Cairn/K-brain/internal/sshmgr/sshtest"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// trustedFixture is a password host whose key is already trusted.
func trustedFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, sshtest.Config{Password: "pw"}, passwordHost("pw"))
	f.trust()
	return f
}

func TestToolDefinitionMatchesV1(t *testing.T) {
	def := Tool(nil, ToolOptions{}).Def.Function
	if def.Name != "SSHManager" {
		t.Fatalf("name = %q", def.Name)
	}
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(def.Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	var props []string
	for name := range schema.Properties {
		props = append(props, name)
	}
	slices.Sort(props)
	want := []string{"action", "cols", "command", "content", "cwd", "data", "from_path", "host_id", "limit", "local_path", "max_bytes", "offset", "overwrite", "path", "recursive", "remote_path", "rows", "session_id", "session_strategy", "sftp_enabled", "timeout_ms", "title", "to_path", "transfer_id"}
	if !slices.Equal(props, want) || !slices.Equal(schema.Required, []string{"action"}) {
		t.Fatalf("properties = %v required = %v", props, schema.Required)
	}
	if got := schema.Properties["action"].Enum; len(got) != 19 || got[0] != "list_hosts" || got[18] != "sftp_cancel_transfer" {
		t.Fatalf("actions = %v", got)
	}
	if got := schema.Properties["session_strategy"].Enum; !slices.Equal(got, []string{"reuse_or_create", "new", "require_existing"}) {
		t.Fatalf("strategies = %v", got)
	}
}

func TestListHostsAndScope(t *testing.T) {
	f := trustedFixture(t)
	out := f.mustCall(map[string]any{"action": "list_hosts"})
	lines := strings.Split(out, "\n")
	if lines[0] != "Authorized SSH hosts:" || !strings.HasPrefix(lines[1], "credential=saved means") {
		t.Fatalf("list_hosts = %q", out)
	}
	wantHost := "- web · Web · tester@" + f.srv.Address() + " · auth=password · credential=saved"
	if lines[len(lines)-1] != wantHost {
		t.Fatalf("host line = %q, want %q", lines[len(lines)-1], wantHost)
	}
	if strings.Contains(out, "pw") && strings.Contains(out, "password=pw") {
		t.Fatal("list_hosts shows the password")
	}
	if out := f.mustCall(map[string]any{"action": "list_sessions"}); out != "No SSH sessions are open for the current project." {
		t.Fatalf("list_sessions = %q", out)
	}
	// Read-only actions are not gated by the tool itself.
	if _, _, gates := f.counts(); gates != 0 {
		t.Fatalf("gates = %d", gates)
	}

	f.failCall(map[string]any{"action": "nope"}, "SSHManager failed: SSHManager.action is invalid.")
	f.failCall(map[string]any{"action": "exec", "host_id": "other", "command": "echo"}, "SSH host is not associated with the current project.")
	f.failCall(map[string]any{"action": "exec", "host_id": "web"}, "SSHManager.command is required.")
	f.failCall(map[string]any{"action": "exec", "host_id": "web", "command": "echo", "session_strategy": "sometimes"}, "SSHManager.session_strategy is invalid.")
	f.failCall(map[string]any{"action": "exec", "host_id": "web", "session_id": "ssh-x", "command": "echo", "session_strategy": "new"}, "cannot be combined with session_id")
	f.failCall(map[string]any{"action": "send_input", "session_id": "ssh-x"}, "SSHManager.data is required.")
	f.failCall(map[string]any{"action": "list_sessions", "host_id": "other"}, "SSH host is not associated")
	f.failCall(map[string]any{"action": "read_session", "session_id": "ssh-x"}, "SSH session not found in the current project.")
	// Malformed calls fail before the user is asked.
	if _, _, gates := f.counts(); gates != 0 || f.srv.Logins() != 0 {
		t.Fatalf("gates=%d logins=%d", gates, f.srv.Logins())
	}

	// Another project sees no hosts.
	ctx := tools.WithWorkingDir(context.Background(), t.TempDir())
	if _, err := f.callCtx(ctx, map[string]any{"action": "list_hosts"}); err == nil || !strings.Contains(err.Error(), "No SSH hosts are associated with the current project.") {
		t.Fatalf("other project = %v", err)
	}
	if _, err := f.callCtx(context.Background(), map[string]any{"action": "list_hosts"}); err == nil || !strings.Contains(err.Error(), "No current project is selected.") {
		t.Fatalf("no project = %v", err)
	}
}

func TestGateCommandsAndRejection(t *testing.T) {
	f := trustedFixture(t)
	f.reject = true
	msg := f.failCall(execArgs("echo hi"), "Permission denied")
	if !strings.HasPrefix(msg, "SSHManager failed: Permission denied") || f.srv.Logins() != 0 {
		t.Fatalf("rejected: %q logins=%d", msg, f.srv.Logins())
	}
	if g := f.lastGate(); g.Tool != "SSHManager" || g.Command != "exec web echo hi" {
		t.Fatalf("gate = %+v", g)
	}
	f.reject = false
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"action": "sftp_mkdir", "host_id": "web", "path": "/d"}, "sftp_mkdir web /d"},
		{map[string]any{"action": "sftp_rename", "host_id": "web", "from_path": "/d", "to_path": "/b"}, "sftp_rename web /d -> /b"},
		{map[string]any{"action": "create_session", "host_id": "web"}, "create_session web"},
	} {
		f.mustCall(tc.args)
		if g := f.lastGate(); g.Command != tc.want {
			t.Fatalf("gate = %q, want %q", g.Command, tc.want)
		}
	}
}

func TestSessionStrategies(t *testing.T) {
	f := trustedFixture(t)
	f.failCall(map[string]any{"action": "exec", "host_id": "web", "command": "echo hi", "session_strategy": "require_existing"}, "No reusable SSH session exists for this host in the current project.")
	if f.srv.Logins() != 0 {
		t.Fatal("require_existing connected")
	}
	created := f.mustCall(map[string]any{"action": "create_session", "host_id": "web", "title": "build box", "sftp_enabled": false})
	if !strings.HasPrefix(created, "Created SSH session:\n- ssh-") || field(t, created, "session_created") != "true" || field(t, created, "session_strategy") != "reuse_or_create" {
		t.Fatalf("create = %q", created)
	}
	id := field(t, created, "session_id")
	if !strings.Contains(created, "· sftp=false ·") || !strings.HasSuffix(strings.Split(created, "\n")[1], "· build box") {
		t.Fatalf("create line = %q", created)
	}
	reused := f.mustCall(map[string]any{"action": "create_session", "host_id": "web"})
	if !strings.HasPrefix(reused, "Reusing the running SSH session for this host (no new connection was opened):") || field(t, reused, "session_id") != id || field(t, reused, "session_reused") != "true" {
		t.Fatalf("reuse = %q", reused)
	}
	// The reused session got SFTP enabled in place.
	if !strings.Contains(reused, "· sftp=true ·") {
		t.Fatalf("reuse sftp = %q", reused)
	}
	out := f.mustCall(execArgs("echo hi"))
	if field(t, out, "session_id") != id || field(t, out, "session_reused") != "true" || f.srv.Logins() != 1 {
		t.Fatalf("exec reuse = %q logins=%d", out, f.srv.Logins())
	}
	out = f.mustCall(map[string]any{"action": "exec", "session_id": id, "command": "echo hi"})
	if field(t, out, "session_strategy") != "session_id" {
		t.Fatalf("exec by id = %q", out)
	}
	fresh := f.mustCall(map[string]any{"action": "exec", "host_id": "web", "command": "echo hi", "session_strategy": "new"})
	freshID := field(t, fresh, "session_id")
	if freshID == id || field(t, fresh, "session_created") != "true" || f.srv.Logins() != 2 {
		t.Fatalf("new = %q logins=%d", fresh, f.srv.Logins())
	}
	list := f.mustCall(map[string]any{"action": "list_sessions"})
	lines := strings.Split(list, "\n")
	if lines[0] != "Project SSH sessions:" || len(lines) != 3 || !strings.HasPrefix(lines[1], "- "+id+" · host=web · connected · sftp=true · running=true · created_at=") {
		t.Fatalf("list = %q", list)
	}
	if out := f.mustCall(map[string]any{"action": "close_session", "session_id": freshID}); out != "Closed SSH session "+freshID+"." {
		t.Fatalf("close = %q", out)
	}
	f.failCall(map[string]any{"action": "close_session", "session_id": freshID}, "SSH session not found in the current project.")
	waitFor(t, "connection close", func() bool { return f.srv.Connections() == 1 })

	// Sessions belong to their project.
	ctx := tools.WithGate(tools.WithWorkingDir(context.Background(), t.TempDir()), f.gate)
	if _, err := f.callCtx(ctx, map[string]any{"action": "read_session", "session_id": id}); err == nil {
		t.Fatal("another project read the session")
	}
}

func TestSettingsChangeClosesSessions(t *testing.T) {
	f := trustedFixture(t)
	id := field(t, f.mustCall(execArgs("echo hi")), "session_id")
	// Renaming does not touch the connection.
	if _, _, err := f.store.Update(0, func(doc *sshhosts.Document) error { doc.Hosts[0].Name = "Renamed"; return nil }); err != nil {
		t.Fatal(err)
	}
	f.m.Sync()
	if _, ok := f.m.Session(sshhosts.ProjectPathKey(f.workdir), id); !ok {
		t.Fatal("rename closed the session")
	}
	if _, _, err := f.store.Update(0, func(doc *sshhosts.Document) error { doc.Hosts[0].Username = "other"; return nil }); err != nil {
		t.Fatal(err)
	}
	f.m.Sync()
	if _, ok := f.m.Session(sshhosts.ProjectPathKey(f.workdir), id); ok {
		t.Fatal("a session with stale settings stayed open")
	}
	waitFor(t, "connection close", func() bool { return f.srv.Connections() == 0 })
}

func TestDroppedConnectionClosesSession(t *testing.T) {
	f := trustedFixture(t)
	id := field(t, f.mustCall(execArgs("echo hi")), "session_id")
	f.srv.DropConnections()
	project := sshhosts.ProjectPathKey(f.workdir)
	waitFor(t, "session close", func() bool { _, ok := f.m.Session(project, id); return !ok })
	// The next call reconnects.
	out := f.mustCall(execArgs("echo back"))
	if field(t, out, "session_id") == id || f.srv.Logins() != 2 {
		t.Fatalf("after drop = %q", out)
	}
}

func TestExecResults(t *testing.T) {
	f := trustedFixture(t)
	out := f.mustCall(execArgs("echo hello world"))
	want := "exit_code: 0\ntimed_out: false\n\nstdout:\nhello world\n\n\nstderr:\n(empty)"
	if !strings.HasSuffix(out, want) {
		t.Fatalf("exec = %q", out)
	}
	out = f.mustCall(execArgs("exit 3"))
	if field(t, out, "exit_code") != "3" || !strings.HasSuffix(out, "stdout:\n(empty)\n\nstderr:\n(empty)") {
		t.Fatalf("exit = %q", out)
	}
	if out := f.mustCall(execArgs("stderr oops")); !strings.HasSuffix(out, "stderr:\noops\n") {
		t.Fatalf("stderr = %q", out)
	}
	if out := f.mustCall(map[string]any{"action": "exec", "host_id": "web", "command": "pwd", "cwd": "/srv/it's"}); !strings.Contains(out, "stdout:\n/srv/it's\n") {
		t.Fatalf("cwd = %q", out)
	}
	if got := f.srv.Execs(); got[len(got)-1] != `cd '/srv/it'"'"'s' && pwd` {
		t.Fatalf("sent = %q", got[len(got)-1])
	}
	out = f.mustCall(map[string]any{"action": "exec", "host_id": "web", "command": "yes 10000", "max_bytes": 1})
	stdout := strings.SplitN(out, "stdout:\n", 2)[1]
	if !strings.HasPrefix(stdout, strings.Repeat("y", MinExecBytes)+"\n") || strings.HasPrefix(stdout, strings.Repeat("y", MinExecBytes+1)) {
		t.Fatalf("max_bytes clamp: %d bytes", len(stdout))
	}
}

func TestExecTimeout(t *testing.T) {
	f := trustedFixture(t)
	started := time.Now()
	out := f.mustCall(map[string]any{"action": "exec", "host_id": "web", "command": "sleep", "timeout_ms": 1})
	if field(t, out, "timed_out") != "true" || field(t, out, "exit_code") != "unknown" {
		t.Fatalf("timeout = %q", out)
	}
	if elapsed := time.Since(started); elapsed < MinExecTimeout || elapsed > 10*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
	waitFor(t, "SIGTERM", func() bool { return slices.Contains(f.srv.Signals(), "TERM") })
}

func TestExecCancel(t *testing.T) {
	f := trustedFixture(t)
	f.mustCall(execArgs("echo warm"))
	ctx, cancel := context.WithCancel(f.ctx())
	done := make(chan error, 1)
	go func() {
		_, err := f.callCtx(ctx, execArgs("sleep"))
		done <- err
	}()
	waitFor(t, "sleep to start", func() bool { return slices.Contains(f.srv.Execs(), "sleep") })
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not stop exec")
	}
	waitFor(t, "SIGTERM", func() bool { return slices.Contains(f.srv.Signals(), "TERM") })
	// The session survives a cancelled command.
	f.mustCall(execArgs("echo still"))
}

func TestShellActions(t *testing.T) {
	f := trustedFixture(t)
	id := field(t, f.mustCall(map[string]any{"action": "create_session", "host_id": "web", "cols": 100, "rows": 1}), "session_id")
	out := ""
	waitFor(t, "shell banner", func() bool {
		out = f.mustCall(map[string]any{"action": "read_session", "session_id": id})
		return strings.Contains(out, "welcome to sshtest")
	})
	if !strings.HasPrefix(out, "session_id: "+id+"\nhost_id: web\ntruncated: false\n\n") {
		t.Fatalf("read = %q", out)
	}
	if ptys := f.srv.PTYs(); len(ptys) != 1 || ptys[0] != [2]int{100, 6} {
		t.Fatalf("ptys = %v", ptys)
	}
	if out := f.mustCall(map[string]any{"action": "send_input", "session_id": id, "data": "ls é😀\n"}); out != "Sent 7 characters to SSH session "+id+"." {
		t.Fatalf("send = %q", out)
	}
	waitFor(t, "echo", func() bool {
		return strings.Contains(f.mustCall(map[string]any{"action": "read_session", "session_id": id}), "ls é😀")
	})
	if g := f.lastGate(); g.Command != "send_input "+id+" ls é😀\n" {
		t.Fatalf("gate = %q", g.Command)
	}
	if out := f.mustCall(map[string]any{"action": "resize_session", "session_id": id, "cols": 90, "rows": 1000}); out != "Resized SSH session "+id+" to 90x200." {
		t.Fatalf("resize = %q", out)
	}
	if out := f.mustCall(map[string]any{"action": "resize_session", "session_id": id}); out != "Resized SSH session "+id+" to 80x24." {
		t.Fatalf("resize default = %q", out)
	}
	waitFor(t, "window change", func() bool { return len(f.srv.Resizes()) == 2 })
	if got := f.srv.Resizes(); got[0] != [2]int{90, 200} || got[1] != [2]int{80, 24} {
		t.Fatalf("resizes = %v", got)
	}
	// read_session ran without the gate.
	for _, g := range f.gates {
		if strings.HasPrefix(g.Command, "read_session") {
			t.Fatalf("read_session was gated: %+v", g)
		}
	}
}

func TestSFTPFileActions(t *testing.T) {
	f := trustedFixture(t)
	decode := func(out string, v any) {
		t.Helper()
		if err := json.Unmarshal([]byte(out), v); err != nil {
			t.Fatalf("%q: %v", out, err)
		}
	}
	call := func(args map[string]any) string {
		t.Helper()
		args["host_id"] = "web"
		return f.mustCall(args)
	}
	var action SFTPAction
	decode(call(map[string]any{"action": "sftp_write_text", "path": "/srv/app/notes.txt", "content": "hello <sftp> & more"}), &action)
	if action.Action != "write_text" || action.Path != "/srv/app/notes.txt" || action.Entry == nil || action.Entry.SizeBytes != 19 || action.Entry.Kind != "file" {
		t.Fatalf("write = %+v", action)
	}
	out := call(map[string]any{"action": "sftp_read_text", "path": "/srv/app/notes.txt"})
	if !strings.Contains(out, `"content": "hello <sftp> & more"`) || !strings.Contains(out, "\n  \"path\"") {
		t.Fatalf("read json = %q", out)
	}
	var read SFTPReadText
	decode(call(map[string]any{"action": "sftp_read_text", "path": "/srv/app/notes.txt", "offset": 6, "limit": 1}), &read)
	if read.Content != "<sftp> & more" || read.Offset != 6 || read.Truncated || read.SizeBytes != 19 {
		t.Fatalf("read offset = %+v", read)
	}
	f.failCall(map[string]any{"action": "sftp_write_text", "host_id": "web", "path": "/srv/app/notes.txt", "content": "x", "overwrite": false}, "target already exists: /srv/app/notes.txt")

	var stat SFTPStat
	decode(call(map[string]any{"action": "sftp_stat", "path": "/srv/missing"}), &stat)
	if stat.Exists || stat.Entry != nil {
		t.Fatalf("missing stat = %+v", stat)
	}
	if out := call(map[string]any{"action": "sftp_stat", "path": "/srv/missing"}); out != "{\n  \"exists\": false\n}" {
		t.Fatalf("missing stat text = %q", out)
	}
	decode(call(map[string]any{"action": "sftp_mkdir", "path": "/srv/app/sub"}), &action)
	if action.Action != "mkdir" || action.Entry.Kind != "directory" {
		t.Fatalf("mkdir = %+v", action)
	}
	decode(call(map[string]any{"action": "sftp_rename", "from_path": "/srv/app/notes.txt", "to_path": "/srv/app/sub/renamed.txt"}), &action)
	if action.Action != "rename" || action.Path != "/srv/app/sub/renamed.txt" {
		t.Fatalf("rename = %+v", action)
	}
	var list SFTPList
	decode(call(map[string]any{"action": "sftp_list", "path": `\srv\app\`}), &list)
	if list.Path != "/srv/app" || len(list.Entries) != 1 || list.Entries[0].Name != "sub" || list.Entries[0].Path != "/srv/app/sub" {
		t.Fatalf("list = %+v", list)
	}
	f.failCall(map[string]any{"action": "sftp_delete", "host_id": "web", "path": "/srv/app"}, "recursive is required to delete a remote directory")
	decode(call(map[string]any{"action": "sftp_delete", "path": "/srv/app", "recursive": true}), &action)
	if action.Action != "delete" {
		t.Fatalf("delete = %+v", action)
	}
	decode(call(map[string]any{"action": "sftp_stat", "path": "/srv/app/sub/renamed.txt"}), &stat)
	if stat.Exists {
		t.Fatal("recursive delete left a file")
	}
	f.failCall(map[string]any{"action": "sftp_read_text", "host_id": "web", "path": "/srv"}, "remote path is a directory")
	// All SFTP calls shared one connection.
	if f.srv.Logins() != 1 {
		t.Fatalf("logins = %d", f.srv.Logins())
	}
}

func TestSFTPTransfers(t *testing.T) {
	f := trustedFixture(t)
	src := filepath.Join(f.workdir, "site")
	if err := os.MkdirAll(filepath.Join(src, "css"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(src, "index.html"), []byte("<h1>hi</h1>"), 0o644)
	os.WriteFile(filepath.Join(src, "css", "app.css"), []byte("body{}"), 0o644)

	f.failCall(map[string]any{"action": "sftp_upload", "host_id": "web", "local_path": "site", "remote_path": "/www"}, "recursive is required to upload a directory")
	f.failCall(map[string]any{"action": "sftp_upload", "host_id": "web", "local_path": filepath.Join(t.TempDir(), "x"), "remote_path": "/www"}, "SSHManager.local_path")

	var started TransferResponse
	out := f.mustCall(map[string]any{"action": "sftp_upload", "host_id": "web", "local_path": "site", "remote_path": "/www", "recursive": true})
	if err := json.Unmarshal([]byte(out), &started); err != nil {
		t.Fatal(err)
	}
	st := started.Transfer
	if st.Direction != "upload" || st.SourcePath != "site" || st.TargetPath != "/www/site" || st.FilesTotal != 2 || st.BytesTotal != 17 || st.Status != TransferQueued {
		t.Fatalf("upload = %+v", st)
	}
	if g := f.lastGate(); g.Command != "sftp_upload web site -> /www" {
		t.Fatalf("gate = %q", g.Command)
	}
	sessionID := st.SessionID
	waitTransfer := func(id string) TransferState {
		t.Helper()
		var state TransferState
		waitFor(t, "transfer "+id, func() bool {
			var resp TransferResponse
			out := f.mustCall(map[string]any{"action": "sftp_transfer_status", "session_id": sessionID, "transfer_id": id})
			if err := json.Unmarshal([]byte(out), &resp); err != nil {
				t.Fatal(err)
			}
			state = resp.Transfer
			return state.Status != TransferQueued && state.Status != TransferRunning
		})
		return state
	}
	if done := waitTransfer(st.ID); done.Status != TransferCompleted || done.FilesDone != 2 || done.BytesDone != 17 {
		t.Fatalf("upload done = %+v", done)
	}
	out = f.mustCall(map[string]any{"action": "sftp_read_text", "host_id": "web", "path": "/www/site/css/app.css"})
	if !strings.Contains(out, `"content": "body{}"`) {
		t.Fatalf("uploaded = %q", out)
	}
	// Upload again without overwrite fails in the background.
	out = f.mustCall(map[string]any{"action": "sftp_upload", "host_id": "web", "local_path": "site", "remote_path": "/www", "recursive": true})
	json.Unmarshal([]byte(out), &started)
	if done := waitTransfer(started.Transfer.ID); done.Status != TransferFailed || !strings.HasPrefix(done.Error, "target already exists: /www/site/") {
		t.Fatalf("second upload = %+v", done)
	}

	// Download the tree into another folder of the workspace.
	if err := os.MkdirAll(filepath.Join(f.workdir, "back"), 0o755); err != nil {
		t.Fatal(err)
	}
	out = f.mustCall(map[string]any{"action": "sftp_download", "host_id": "web", "remote_path": "/www/site", "local_path": "back", "recursive": true})
	json.Unmarshal([]byte(out), &started)
	if started.Transfer.TargetPath != "back/site" || started.Transfer.Direction != "download" {
		t.Fatalf("download = %+v", started.Transfer)
	}
	if done := waitTransfer(started.Transfer.ID); done.Status != TransferCompleted || done.FilesDone != 2 {
		t.Fatalf("download done = %+v", done)
	}
	data, err := os.ReadFile(filepath.Join(f.workdir, "back", "site", "css", "app.css"))
	if err != nil || string(data) != "body{}" {
		t.Fatalf("downloaded = %q %v", data, err)
	}
	f.failCall(map[string]any{"action": "sftp_transfer_status", "session_id": sessionID, "transfer_id": "sftp-nope"}, "SFTP transfer not found")
	f.failCall(map[string]any{"action": "sftp_cancel_transfer", "session_id": sessionID, "transfer_id": started.Transfer.ID}, "SFTP transfer not found")
}

func TestSFTPTransferCancel(t *testing.T) {
	f := trustedFixture(t)
	f.mustCall(map[string]any{"action": "sftp_write_text", "host_id": "web", "path": "/big.txt", "content": strings.Repeat("x", 256<<10)})
	blocked, release := f.srv.BlockReads()
	defer release()
	var started TransferResponse
	out := f.mustCall(map[string]any{"action": "sftp_download", "host_id": "web", "remote_path": "/big.txt", "local_path": "."})
	if err := json.Unmarshal([]byte(out), &started); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked:
	case <-time.After(10 * time.Second):
		t.Fatal("download never read")
	}
	id, sessionID := started.Transfer.ID, started.Transfer.SessionID
	go func() {
		time.Sleep(50 * time.Millisecond)
		release()
	}()
	if out := f.mustCall(map[string]any{"action": "sftp_cancel_transfer", "session_id": sessionID, "transfer_id": id}); out != "Cancelled SFTP transfer "+id+"." {
		t.Fatalf("cancel = %q", out)
	}
	var status TransferResponse
	waitFor(t, "cancelled status", func() bool {
		json.Unmarshal([]byte(f.mustCall(map[string]any{"action": "sftp_transfer_status", "session_id": sessionID, "transfer_id": id})), &status)
		return status.Transfer.Status == TransferCancelled
	})
	if status.Transfer.Error != "SFTP transfer was cancelled" {
		t.Fatalf("status = %+v", status.Transfer)
	}
	f.failCall(map[string]any{"action": "sftp_cancel_transfer", "session_id": sessionID, "transfer_id": id}, "SFTP transfer not found")
}

func TestNormalizeRemotePath(t *testing.T) {
	for in, want := range map[string]string{"": ".", ".": ".", "/": "/", "a/../b": "a/b", `\srv\app\`: "/srv/app", "/a//b/./c/": "/a/b/c", " x ": "x"} {
		if got := NormalizeRemotePath(in); got != want {
			t.Fatalf("NormalizeRemotePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestManagerCloseEndsSessions(t *testing.T) {
	f := trustedFixture(t)
	f.mustCall(execArgs("echo hi"))
	f.m.Close()
	waitFor(t, "connection close", func() bool { return f.srv.Connections() == 0 })
	f.failCall(map[string]any{"action": "exec", "host_id": "web", "command": "echo", "session_strategy": "new"}, "the backend is closing")
}
