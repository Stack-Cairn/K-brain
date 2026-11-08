package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/sshhosts"
	"github.com/Stack-Cairn/K-brain/internal/sshmgr/sshtest"
)

const sshTestSecret = "ssh-secret-must-not-be-journaled"

func doSSH(t *testing.T, method, url string, body any) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(data))
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func TestSSHSettingsSecretsAreWriteOnly(t *testing.T) {
	_, base, root := newHostBridgeServer(t, &hostToolClient{})
	url := base + "/v1/settings/ssh"
	code, body := doSSH(t, http.MethodGet, url, nil)
	if code != http.StatusOK || !strings.Contains(body, `"hosts":[]`) || !strings.Contains(body, `"revision":1`) {
		t.Fatalf("empty GET = %d %s", code, body)
	}
	put := map[string]any{
		"hosts": []map[string]any{
			{"id": "web", "name": "Web", "host": "10.0.0.8", "port": "2222", "username": "deploy", "authType": "password", "password": sshTestSecret,
				"proxy": map[string]any{"type": "socks5", "url": "127.0.0.1:1080", "password": sshTestSecret + "-proxy"}},
			{"id": "mfa", "name": "MFA", "host": "mfa.internal", "username": "me", "authType": "keyboardInteractive"},
		},
		"projectHostAssociations": map[string][]string{"/repo/": {"web", "mfa", "ghost"}},
	}
	code, body = doSSH(t, http.MethodPut, url, put)
	if code != http.StatusOK || strings.Contains(body, sshTestSecret) {
		t.Fatalf("PUT = %d %s", code, body)
	}
	var doc sshhosts.PublicDocument
	_, body = doSSH(t, http.MethodGet, url, nil)
	if strings.Contains(body, sshTestSecret) {
		t.Fatalf("GET leaks a secret: %s", body)
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	web := doc.Hosts[0]
	if doc.Revision != 2 || web.Port != 2222 || !web.PasswordConfigured || web.Password != "" || !web.Proxy.PasswordConfigured || web.Credential != "saved" ||
		doc.Hosts[1].Credential != "interactive" || len(doc.ProjectHostAssociations["/repo"]) != 2 {
		t.Fatalf("doc = %+v", doc)
	}
	if want := filepath.Join(root, "events", "ssh", "known_hosts"); doc.KnownHostsPath != want {
		t.Fatalf("known_hosts path = %q, want %q", doc.KnownHostsPath, want)
	}
	data, err := os.ReadFile(filepath.Join(root, "events", "ssh", "hosts.json"))
	if err != nil || !strings.Contains(string(data), sshTestSecret) {
		t.Fatalf("hosts.json = %v", err)
	}

	// Sending back GET's document keeps the secrets.
	var echoed map[string]any
	json.Unmarshal([]byte(body), &echoed)
	echoed["baseRevision"] = doc.Revision
	if code, body = doSSH(t, http.MethodPut, url, echoed); code != http.StatusOK {
		t.Fatalf("echo PUT = %d %s", code, body)
	}
	data, _ = os.ReadFile(filepath.Join(root, "events", "ssh", "hosts.json"))
	if !strings.Contains(string(data), `"password": "`+sshTestSecret+`"`) {
		t.Fatal("echoing GET dropped the password")
	}
	// A stale base revision conflicts.
	if code, _ = doSSH(t, http.MethodPut, url, map[string]any{"baseRevision": 1, "hosts": []any{}}); code != http.StatusConflict {
		t.Fatalf("stale PUT = %d", code)
	}
	if code, body = doSSH(t, http.MethodPut, url, map[string]any{"hosts": []map[string]any{{"id": "x", "name": "", "host": "h"}}}); code != http.StatusBadRequest || !strings.Contains(body, "name is required") {
		t.Fatalf("invalid PUT = %d %s", code, body)
	}

	// One host at a time.
	hostURL := url + "/hosts/db"
	if code, body = doSSH(t, http.MethodPut, hostURL, map[string]any{"name": "DB", "host": "db.internal", "username": "pg", "password": sshTestSecret}); code != http.StatusOK || strings.Contains(body, sshTestSecret) || !strings.Contains(body, `"id":"db"`) {
		t.Fatalf("PUT host = %d %s", code, body)
	}
	if code, _ = doSSH(t, http.MethodPut, hostURL, map[string]any{"id": "other", "name": "DB", "host": "db"}); code != http.StatusBadRequest {
		t.Fatalf("mismatched id = %d", code)
	}
	if code, _ = doSSH(t, http.MethodGet, hostURL, nil); code != http.StatusMethodNotAllowed {
		t.Fatalf("GET host = %d", code)
	}
	if code, body = doSSH(t, http.MethodDelete, url+"/hosts/db/host-keys", nil); code != http.StatusOK || !strings.Contains(body, `"removed":0`) {
		t.Fatalf("forget keys = %d %s", code, body)
	}
	if code, _ = doSSH(t, http.MethodDelete, url+"/hosts/nope/host-keys", nil); code != http.StatusNotFound {
		t.Fatalf("forget keys of unknown host = %d", code)
	}
	if code, _ = doSSH(t, http.MethodDelete, hostURL, nil); code != http.StatusOK {
		t.Fatalf("DELETE host = %d", code)
	}
	if code, _ = doSSH(t, http.MethodDelete, hostURL, nil); code != http.StatusNotFound {
		t.Fatalf("DELETE missing host = %d", code)
	}
	if code, _ = doSSH(t, http.MethodGet, url+"/hosts/a/b", nil); code != http.StatusNotFound {
		t.Fatalf("nested path = %d", code)
	}
	if code, _ = doSSH(t, http.MethodPost, url, map[string]any{}); code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d", code)
	}
}

func TestSSHManagerCapabilityInHealth(t *testing.T) {
	_, base, _ := newHostBridgeServer(t, &hostToolClient{})
	resp, err := http.Get(base + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Capabilities map[string]any `json:"capabilities"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if out.Capabilities["ssh_manager"] != float64(1) || out.Capabilities["host_bridge"] != float64(1) {
		t.Fatalf("capabilities = %v", out.Capabilities)
	}
}

// sshProject points host "web" at srv and associates it with the session's
// working directory.
func sshProject(t *testing.T, base string, sess protocol.Session, srv *sshtest.Server, host map[string]any) {
	t.Helper()
	host["id"], host["name"], host["host"], host["port"], host["username"] = "web", "Web", srv.Host, srv.Port, srv.User
	assoc := map[string][]string{sess.CWD: {"web"}}
	if real, err := filepath.EvalSymlinks(sess.CWD); err == nil {
		assoc[real] = []string{"web"}
	}
	code, body := doSSH(t, http.MethodPut, base+"/v1/settings/ssh", map[string]any{"hosts": []any{host}, "projectHostAssociations": assoc})
	if code != http.StatusOK {
		t.Fatalf("PUT settings = %d %s", code, body)
	}
}

func TestSSHManagerToolFollowsAssociations(t *testing.T) {
	client := &hostToolClient{}
	_, base, _ := newHostBridgeServer(t, client)
	sess := createTestSession(t, base)
	runRules(t, base, sess.ID, "normal", nil)
	if _, ok := client.lastTools()["SSHManager"]; ok {
		t.Fatal("SSHManager offered without associated hosts")
	}
	srv := sshtest.New(t, sshtest.Config{Password: "pw"})
	sshProject(t, base, sess, srv, map[string]any{"authType": "password", "password": "pw"})
	runRules(t, base, sess.ID, "normal", nil)
	schema, ok := client.lastTools()["SSHManager"]
	if !ok || !strings.Contains(schema, "sftp_cancel_transfer") {
		t.Fatalf("SSHManager missing after association: %v", client.lastTools())
	}
	// Removing the association removes the tool on the next run.
	doSSH(t, http.MethodPut, base+"/v1/settings/ssh", map[string]any{"projectHostAssociations": map[string][]string{}})
	runRules(t, base, sess.ID, "normal", nil)
	if _, ok := client.lastTools()["SSHManager"]; ok {
		t.Fatal("SSHManager still offered after the association was removed")
	}
}

// sshRunHandlers answer what an SSH run asks; nil fails the test.
type sshRunHandlers struct {
	permission  func(protocol.PermissionRequest) string
	question    func(protocol.QuestionRequest) string
	elicitation func(protocol.ElicitationRequest) (string, map[string]any)
}

// driveSSHRun starts a run and answers its prompts until it ends.
func driveSSHRun(t *testing.T, base, sessionID string, options protocol.RunOptions, h sshRunHandlers) []protocol.Event {
	t.Helper()
	var accepted protocol.RunAccepted
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sessionID+"/runs", protocol.PromptRequest{ConversationID: sessionID, ClientRequestID: fmt.Sprintf("ssh-%d", rulesRequestSeq.Add(1)), Prompt: "go", Options: &options}, &accepted)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("accept status=%d", resp.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/events?after_seq=%d", base, sessionID, accepted.AcceptedSeq-1), nil)
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	answer := func(path string, body any) {
		t.Helper()
		r := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sessionID+path, body, nil)
		data, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Fatalf("answer %s = %d %s", path, r.StatusCode, data)
		}
	}
	var events []protocol.Event
	scanner := bufio.NewScanner(stream.Body)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		line, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var event protocol.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.RunID != accepted.RunID {
			continue
		}
		events = append(events, event)
		switch event.Type {
		case protocol.EventPermissionRequest:
			var p protocol.PermissionRequest
			json.Unmarshal(event.Payload, &p)
			if h.permission == nil {
				t.Fatalf("unexpected permission prompt %+v", p)
			}
			answer("/permissions/"+p.PermissionID, protocol.PermissionDecisionRequest{ConversationID: sessionID, RunID: accepted.RunID, Decision: protocol.PermissionDecision{PermissionID: p.PermissionID, Decision: h.permission(p)}})
		case protocol.EventQuestionRequested:
			var q protocol.QuestionRequest
			json.Unmarshal(event.Payload, &q)
			if h.question == nil {
				t.Fatalf("unexpected question %+v", q)
			}
			label := h.question(q)
			answer("/questions/"+q.QuestionID, protocol.QuestionAnswerRequest{ConversationID: sessionID, RunID: accepted.RunID, QuestionID: q.QuestionID,
				Answers: []protocol.QuestionAnswer{{QuestionID: q.Questions[0].ID, Prompt: q.Questions[0].Prompt, SelectedLabel: label}}})
		case protocol.EventElicitationRequested:
			var e protocol.ElicitationRequest
			json.Unmarshal(event.Payload, &e)
			if h.elicitation == nil {
				t.Fatalf("unexpected elicitation %+v", e)
			}
			action, content := h.elicitation(e)
			answer("/elicitations/"+e.ElicitationID, protocol.ElicitationAnswerRequest{ConversationID: sessionID, RunID: accepted.RunID, ElicitationID: e.ElicitationID, Action: action, Content: content})
		case protocol.EventRunCompleted, protocol.EventRunFailed, protocol.EventRunCancelled:
			return events
		}
	}
	t.Fatalf("run did not finish: %+v", events)
	return nil
}

func TestSSHManagerRunAsksAndKeepsSecretsOut(t *testing.T) {
	client := &hostToolClient{}
	_, base, root := newHostBridgeServer(t, client)
	sess := createTestSession(t, base)
	srv := sshtest.New(t, sshtest.Config{Password: sshTestSecret})
	// No saved password: the login form asks for it.
	sshProject(t, base, sess, srv, map[string]any{"authType": "password"})

	list, exec := hostCall("SSHManager", `{"action":"list_hosts"}`), hostCall("SSHManager", `{"action":"exec","host_id":"web","command":"echo deployed"}`)
	client.queue(list, exec)
	var permissions []protocol.PermissionRequest
	var questions []protocol.QuestionRequest
	var forms []protocol.ElicitationRequest
	events := driveSSHRun(t, base, sess.ID, protocol.RunOptions{Mode: "agent", MCPElicitation: true}, sshRunHandlers{
		permission: func(p protocol.PermissionRequest) string { permissions = append(permissions, p); return "allow_once" },
		question: func(q protocol.QuestionRequest) string {
			questions = append(questions, q)
			return "Trust and save"
		},
		elicitation: func(e protocol.ElicitationRequest) (string, map[string]any) {
			forms = append(forms, e)
			return "accept", map[string]any{"password": sshTestSecret}
		},
	})
	results := resultsByID(t, events)
	if r := results[list.ID]; r.Failed || !strings.Contains(r.Output, "credential=missing") {
		t.Fatalf("list_hosts = %+v", r)
	}
	if r := results[exec.ID]; r.Failed || !strings.Contains(r.Output, "stdout:\ndeployed\n") {
		t.Fatalf("exec = %+v", r)
	}
	// Only exec asked for permission (Bash-like default); list_hosts is read-only.
	if len(permissions) != 1 || permissions[0].Tool != "SSHManager" || permissions[0].Command != "exec web echo deployed" {
		t.Fatalf("permissions = %+v", permissions)
	}
	if len(questions) != 1 || len(questions[0].Questions[0].Options) != 3 || !strings.Contains(questions[0].Questions[0].Prompt, "SHA256:") {
		t.Fatalf("questions = %+v", questions)
	}
	if len(forms) != 1 || forms[0].Source != "ssh" || forms[0].Server != "ssh:Web" || len(forms[0].Fields) != 1 || !forms[0].Fields[0].Sensitive {
		t.Fatalf("forms = %+v", forms)
	}
	// The accepted key is saved in K-brain's own known_hosts.
	data, err := os.ReadFile(filepath.Join(root, "events", "ssh", "known_hosts"))
	if err != nil || !strings.Contains(string(data), fmt.Sprintf("[127.0.0.1]:%d", srv.Port)) {
		t.Fatalf("known_hosts = %q %v", data, err)
	}

	// The typed password is in no event, history entry or settings file.
	resp, err := http.Get(base + "/v1/sessions/" + sess.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range readSSE(t, resp) {
		if strings.Contains(string(e.Payload), sshTestSecret) {
			t.Fatalf("%s carries the password: %s", e.Type, e.Payload)
		}
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		code, history := doSSH(t, http.MethodGet, base+"/v1/sessions/"+sess.ID+"/history?max_messages=100", nil)
		if code != http.StatusOK {
			t.Fatalf("history = %d", code)
		}
		if strings.Contains(history, sshTestSecret) {
			t.Fatal("history carries the password")
		}
		if strings.Contains(history, `"source":"ssh"`) && strings.Contains(history, `"redacted":["password"]`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("history never recorded the redacted login form: %s", history)
		}
	}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if data, _ := os.ReadFile(path); strings.Contains(string(data), sshTestSecret) {
			t.Errorf("%s holds the typed password", path)
		}
		return nil
	})

	// Rejecting the permission prompt never connects.
	logins := srv.Logins()
	reject := hostCall("SSHManager", `{"action":"exec","host_id":"web","command":"echo again","session_strategy":"new"}`)
	client.queue(reject)
	events = driveSSHRun(t, base, sess.ID, protocol.RunOptions{Mode: "agent", MCPElicitation: true}, sshRunHandlers{
		permission: func(protocol.PermissionRequest) string { return "reject" },
	})
	if r := resultsByID(t, events)[reject.ID]; !r.Failed || !strings.Contains(r.Output, "Permission denied") || srv.Logins() != logins {
		t.Fatalf("rejected exec = %+v logins=%d", r, srv.Logins())
	}
}

func TestSSHManagerWithoutFormClientFailsClearly(t *testing.T) {
	client := &hostToolClient{}
	_, base, _ := newHostBridgeServer(t, client)
	sess := createTestSession(t, base)
	srv := sshtest.New(t, sshtest.Config{Password: "pw"})
	sshProject(t, base, sess, srv, map[string]any{"authType": "password"})
	exec := hostCall("SSHManager", `{"action":"exec","host_id":"web","command":"echo hi"}`)
	client.queue(exec)
	events := driveSSHRun(t, base, sess.ID, protocol.RunOptions{Mode: "agent"}, sshRunHandlers{permission: func(protocol.PermissionRequest) string { return "allow_once" }})
	if r := resultsByID(t, events)[exec.ID]; !r.Failed || !strings.Contains(r.Output, "options.mcp_elicitation") || srv.Logins() != 0 {
		t.Fatalf("exec = %+v", r)
	}
}

func TestSSHManagerUnattendedNeedsAllowRuleAndTrustedKey(t *testing.T) {
	client := &hostToolClient{}
	s, base, root := newHostBridgeServer(t, client)
	sess := createTestSession(t, base)
	srv := sshtest.New(t, sshtest.Config{Password: "pw"})
	sshProject(t, base, sess, srv, map[string]any{"authType": "password", "password": "pw"})

	exec, list := hostCall("SSHManager", `{"action":"exec","host_id":"web","command":"echo hi"}`), hostCall("SSHManager", `{"action":"list_hosts"}`)
	client.queue(exec, list)
	started := time.Now()
	events := runUnattended(t, s, base, sess.ID, "ssh-unattended-1")
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("unattended run took %s", elapsed)
	}
	for _, e := range events {
		if e.Type == protocol.EventPermissionRequest || e.Type == protocol.EventQuestionRequested || e.Type == protocol.EventElicitationRequested {
			t.Fatalf("unattended run asked: %s", e.Type)
		}
	}
	results := resultsByID(t, events)
	if !results[exec.ID].Failed || !strings.Contains(results[exec.ID].Output, "unattended") || results[list.ID].Failed || srv.Logins() != 0 {
		t.Fatalf("results = %+v", results)
	}

	// An allow rule lets exec through the gate; the unknown host key still
	// fails at once.
	resp := postJSON(t, http.DefaultClient, base+"/v1/permissions/rules", protocol.PermissionRuleCreate{Rule: "SSHManager(exec web:*)", Effect: "allow", Scope: "user"}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add rule = %d", resp.StatusCode)
	}
	exec = hostCall("SSHManager", `{"action":"exec","host_id":"web","command":"echo hi"}`)
	client.queue(exec)
	results = resultsByID(t, runUnattended(t, s, base, sess.ID, "ssh-unattended-2"))
	if r := results[exec.ID]; !r.Failed || !strings.Contains(r.Output, "no trusted host key yet") || srv.Logins() != 0 {
		t.Fatalf("untrusted = %+v", r)
	}

	// With the key trusted, the scheduled exec runs.
	known := sshhosts.NewKnownHosts(filepath.Join(root, "events", "ssh", "known_hosts"))
	if err := known.Add(srv.Address(), srv.HostKey()); err != nil {
		t.Fatal(err)
	}
	exec = hostCall("SSHManager", `{"action":"exec","host_id":"web","command":"echo scheduled"}`)
	client.queue(exec)
	results = resultsByID(t, runUnattended(t, s, base, sess.ID, "ssh-unattended-3"))
	if r := results[exec.ID]; r.Failed || !strings.Contains(r.Output, "stdout:\nscheduled\n") {
		t.Fatalf("trusted = %+v", r)
	}
}

func TestSSHHostKeysDeleteClosesSessions(t *testing.T) {
	client := &hostToolClient{}
	_, base, root := newHostBridgeServer(t, client)
	sess := createTestSession(t, base)
	srv := sshtest.New(t, sshtest.Config{Password: "pw"})
	sshProject(t, base, sess, srv, map[string]any{"authType": "password", "password": "pw"})
	known := sshhosts.NewKnownHosts(filepath.Join(root, "events", "ssh", "known_hosts"))
	if err := known.Add(srv.Address(), srv.HostKey()); err != nil {
		t.Fatal(err)
	}
	exec := hostCall("SSHManager", `{"action":"exec","host_id":"web","command":"echo hi"}`)
	client.queue(exec)
	events := driveSSHRun(t, base, sess.ID, protocol.RunOptions{Mode: "agent"}, sshRunHandlers{permission: func(protocol.PermissionRequest) string { return "allow_once" }})
	if r := resultsByID(t, events)[exec.ID]; r.Failed {
		t.Fatalf("exec = %+v", r)
	}
	if srv.Connections() != 1 {
		t.Fatalf("connections = %d", srv.Connections())
	}
	code, body := doSSH(t, http.MethodDelete, base+"/v1/settings/ssh/hosts/web/host-keys", nil)
	if code != http.StatusOK || !strings.Contains(body, `"removed":1`) || !strings.Contains(body, `"hostKeys":[]`) {
		t.Fatalf("forget = %d %s", code, body)
	}
	for deadline := time.Now().Add(10 * time.Second); srv.Connections() != 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("session survived forgetting the host key")
		}
	}
}
