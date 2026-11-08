package sshmgr

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/elicit"
	"github.com/Stack-Cairn/K-brain/internal/sshhosts"
	"github.com/Stack-Cairn/K-brain/internal/sshmgr/sshtest"
	"golang.org/x/crypto/ssh"
)

func execArgs(command string) map[string]any {
	return map[string]any{"action": "exec", "host_id": "web", "command": command}
}

func TestHostKeyUnknownTrustAndSave(t *testing.T) {
	f := newFixture(t, sshtest.Config{Password: "pw"}, passwordHost("pw"))
	out := f.mustCall(execArgs("echo hi"))
	if !strings.Contains(out, "stdout:\nhi\n") {
		t.Fatalf("exec = %q", out)
	}
	asks, _, _ := f.counts()
	if asks != 1 {
		t.Fatalf("asks = %d", asks)
	}
	q := f.asks[0]
	fp := ssh.FingerprintSHA256(f.srv.HostKey())
	if !strings.Contains(q.Question, fp) || !strings.Contains(q.Question, f.srv.Address()) {
		t.Fatalf("question = %q", q.Question)
	}
	var labels []string
	for _, o := range q.Options {
		labels = append(labels, o.Label)
	}
	if !slices.Equal(labels, []string{"Trust once", "Trust and save", "Reject"}) || !q.Options[2].Recommended {
		t.Fatalf("options = %+v", q.Options)
	}
	keys := f.store.KnownHosts().Keys(f.srv.Address())
	if len(keys) != 1 || keys[0].Fingerprint != fp {
		t.Fatalf("known keys = %+v", keys)
	}
	// The saved key is trusted from now on, also for a new connection.
	f.mustCall(map[string]any{"action": "exec", "host_id": "web", "command": "echo again", "session_strategy": "new"})
	if asks, _, _ := f.counts(); asks != 1 || f.srv.Logins() != 2 {
		t.Fatalf("asks = %d logins = %d", asks, f.srv.Logins())
	}
	data, err := os.ReadFile(f.store.KnownHosts().Path())
	if err != nil || !strings.Contains(string(data), "[127.0.0.1]:") {
		t.Fatalf("known_hosts = %q %v", data, err)
	}
}

func TestHostKeyTrustOnceAsksAgain(t *testing.T) {
	f := newFixture(t, sshtest.Config{Password: "pw"}, passwordHost("pw"))
	f.hostKeyPick = hostKeyTrustOnce
	f.mustCall(execArgs("echo hi"))
	if len(f.store.KnownHosts().Keys(f.srv.Address())) != 0 {
		t.Fatal("trust once saved the key")
	}
	// The session keeps working without asking again...
	f.mustCall(execArgs("echo hi"))
	if asks, _, _ := f.counts(); asks != 1 {
		t.Fatalf("asks = %d", asks)
	}
	// ...but a new connection asks again.
	f.mustCall(map[string]any{"action": "exec", "host_id": "web", "command": "echo hi", "session_strategy": "new"})
	if asks, _, _ := f.counts(); asks != 2 {
		t.Fatalf("asks = %d", asks)
	}
}

func TestHostKeyRejectAndNoAnswer(t *testing.T) {
	for _, pick := range []string{hostKeyReject, ""} {
		f := newFixture(t, sshtest.Config{Password: "pw"}, passwordHost("pw"))
		f.hostKeyPick = pick
		f.failCall(execArgs("echo hi"), "did not trust the host key")
		if f.srv.Logins() != 0 || len(f.store.KnownHosts().Keys(f.srv.Address())) != 0 || len(f.m.Sessions(sshhosts.ProjectPathKey(f.workdir))) != 0 {
			t.Fatalf("pick %q: logins=%d", pick, f.srv.Logins())
		}
	}
	// Nobody to ask: refused without a question.
	f := newFixture(t, sshtest.Config{Password: "pw"}, passwordHost("pw"))
	f.noAsk = true
	f.failCall(execArgs("echo hi"), "nobody can confirm it")
}

func TestHostKeyChangedIsRefused(t *testing.T) {
	f := newFixture(t, sshtest.Config{Password: "pw"}, passwordHost("pw"))
	f.trust()
	f.mustCall(execArgs("echo hi"))
	old := ssh.FingerprintSHA256(f.srv.HostKey())
	f.srv.SetHostKey(sshtest.NewHostKey(t))
	msg := f.failCall(map[string]any{"action": "exec", "host_id": "web", "command": "echo hi", "session_strategy": "new"}, "REMOTE HOST KEY CHANGED")
	if !strings.Contains(msg, old) || !strings.Contains(msg, ssh.FingerprintSHA256(f.srv.HostKey())) {
		t.Fatalf("changed message = %q", msg)
	}
	if asks, _, _ := f.counts(); asks != 0 || f.srv.Logins() != 1 {
		t.Fatalf("asks = %d logins = %d", asks, f.srv.Logins())
	}
	// Forgetting the old key lets the user confirm the new one.
	if n, err := f.store.KnownHosts().Remove(f.srv.Address()); err != nil || n != 1 {
		t.Fatalf("remove = %d %v", n, err)
	}
	f.mustCall(map[string]any{"action": "exec", "host_id": "web", "command": "echo hi", "session_strategy": "new"})
	if asks, _, _ := f.counts(); asks != 1 {
		t.Fatalf("asks = %d", asks)
	}
}

func TestMissingPasswordUsesLoginForm(t *testing.T) {
	const secret = "typed-secret-pw"
	f := newFixture(t, sshtest.Config{Password: secret}, sshhosts.HostInput{AuthType: sshhosts.AuthPassword})
	f.trust()
	f.answer = func(req elicit.Request) elicit.Response {
		return elicit.Response{Action: elicit.Accept, Reason: elicit.ReasonUser, Content: map[string]any{"password": secret}}
	}
	out := f.mustCall(execArgs("echo hi"))
	if !strings.Contains(out, "stdout:\nhi\n") {
		t.Fatalf("exec = %q", out)
	}
	if len(f.elicits) != 1 {
		t.Fatalf("elicits = %+v", f.elicits)
	}
	req := f.elicits[0]
	if req.Source != elicit.SourceSSH || req.Server != "ssh:Web" || req.Tool != ToolName || len(req.Fields) != 1 || req.Fields[0].Name != "password" || !req.Fields[0].Sensitive {
		t.Fatalf("form = %+v", req)
	}
	if strings.Contains(req.Message, secret) || strings.Contains(out, secret) {
		t.Fatal("secret leaked into the form or result")
	}
	// The typed password is not saved.
	if h, _ := f.store.Host("web"); h.Password != "" {
		t.Fatal("typed password was saved")
	}
	data, _ := os.ReadFile(filepath.Join(filepath.Dir(f.store.KnownHosts().Path()), "hosts.json"))
	if strings.Contains(string(data), secret) {
		t.Fatal("typed password reached hosts.json")
	}

	// Declined: the call fails and nothing connects.
	f.answer = nil
	f.failCall(map[string]any{"action": "exec", "host_id": "web", "command": "echo hi", "session_strategy": "new"}, "declined the SSH login form")
	// A wrong password fails as an authentication error.
	f.answer = func(elicit.Request) elicit.Response {
		return elicit.Response{Action: elicit.Accept, Content: map[string]any{"password": "wrong"}}
	}
	f.failCall(map[string]any{"action": "exec", "host_id": "web", "command": "echo hi", "session_strategy": "new"}, "authentication failed")
}

func TestMissingPasswordWithoutFormClient(t *testing.T) {
	f := newFixture(t, sshtest.Config{Password: "pw"}, sshhosts.HostInput{AuthType: sshhosts.AuthPassword})
	f.noElicit = true
	f.failCall(execArgs("echo hi"), "options.mcp_elicitation")
	if asks, _, _ := f.counts(); asks != 0 || f.srv.Logins() != 0 {
		t.Fatalf("asks=%d logins=%d", asks, f.srv.Logins())
	}
}

func TestKeyboardInteractiveLogin(t *testing.T) {
	cfg := sshtest.Config{Questions: []sshtest.Question{{Prompt: "Password: ", Answer: "pw"}, {Prompt: "Verification code: ", Echo: true, Answer: "123456"}}}
	f := newFixture(t, cfg, sshhosts.HostInput{AuthType: sshhosts.AuthKeyboardInteractive})
	f.trust()
	f.answer = func(req elicit.Request) elicit.Response {
		return elicit.Response{Action: elicit.Accept, Content: map[string]any{"response_1": "pw", "response_2": "123456"}}
	}
	f.mustCall(execArgs("echo hi"))
	if len(f.elicits) != 1 {
		t.Fatalf("elicits = %d", len(f.elicits))
	}
	fields := f.elicits[0].Fields
	if len(fields) != 2 || fields[0].Title != "Password:" || !fields[0].Sensitive || fields[1].Title != "Verification code:" || fields[1].Sensitive {
		t.Fatalf("fields = %+v", fields)
	}
	if !strings.Contains(f.elicits[0].Message, "Test login") {
		t.Fatalf("message = %q", f.elicits[0].Message)
	}
	// A wrong code fails.
	f.answer = func(req elicit.Request) elicit.Response {
		return elicit.Response{Action: elicit.Accept, Content: map[string]any{"response_1": "pw", "response_2": "000000"}}
	}
	f.failCall(map[string]any{"action": "exec", "host_id": "web", "command": "echo hi", "session_strategy": "new"}, "SSH connection to Web failed")
}

func TestSavedPasswordAnswersKeyboardInteractivePrompt(t *testing.T) {
	// Servers often offer password logins only as keyboard-interactive.
	f := newFixture(t, sshtest.Config{Questions: []sshtest.Question{{Prompt: "Password: ", Answer: "pw"}}}, passwordHost("pw"))
	f.trust()
	f.mustCall(execArgs("echo hi"))
	if _, elicits, _ := f.counts(); elicits != 0 {
		t.Fatalf("elicits = %d", elicits)
	}
}

func TestPrivateKeyLogins(t *testing.T) {
	t.Run("inline", func(t *testing.T) {
		f := newFixture(t, sshtest.Config{PublicKey: true}, sshhosts.HostInput{AuthType: sshhosts.AuthPrivateKey})
		setKeyHost(t, f, sshhosts.HostInput{PrivateKey: string(f.srv.ClientKeyPEM)})
		f.trust()
		f.mustCall(execArgs("echo hi"))
		if tries := f.srv.AuthTries(); !slices.Contains(tries, "publickey") {
			t.Fatalf("tries = %v", tries)
		}
	})
	t.Run("encrypted saved passphrase", func(t *testing.T) {
		f := newFixture(t, sshtest.Config{PublicKey: true}, sshhosts.HostInput{AuthType: sshhosts.AuthPrivateKey})
		srv := f.srv
		setKeyHost(t, f, sshhosts.HostInput{PrivateKey: string(srv.EncryptedClientKey(t, "phrase")), PrivateKeyPassphrase: "phrase"})
		f.trust()
		f.mustCall(execArgs("echo hi"))
		if _, elicits, _ := f.counts(); elicits != 0 {
			t.Fatalf("elicits = %d", elicits)
		}
	})
	t.Run("encrypted asks passphrase", func(t *testing.T) {
		f := newFixture(t, sshtest.Config{PublicKey: true}, sshhosts.HostInput{AuthType: sshhosts.AuthPrivateKey})
		srv := f.srv
		setKeyHost(t, f, sshhosts.HostInput{PrivateKey: string(srv.EncryptedClientKey(t, "phrase"))})
		f.trust()
		f.answer = func(req elicit.Request) elicit.Response {
			return elicit.Response{Action: elicit.Accept, Content: map[string]any{"passphrase": "phrase"}}
		}
		f.mustCall(execArgs("echo hi"))
		if len(f.elicits) != 1 || f.elicits[0].Fields[0].Name != "passphrase" || !f.elicits[0].Fields[0].Sensitive {
			t.Fatalf("elicits = %+v", f.elicits)
		}
		f.answer = func(req elicit.Request) elicit.Response {
			return elicit.Response{Action: elicit.Accept, Content: map[string]any{"passphrase": "nope"}}
		}
		f.failCall(map[string]any{"action": "exec", "host_id": "web", "command": "echo hi", "session_strategy": "new"}, "passphrase is wrong")
	})
	t.Run("key file under home", func(t *testing.T) {
		f := newFixture(t, sshtest.Config{PublicKey: true}, sshhosts.HostInput{AuthType: sshhosts.AuthPrivateKey})
		srv := f.srv
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(home, "sshmgr-test-keys")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "id_test"), srv.ClientKeyPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		setKeyHost(t, f, sshhosts.HostInput{PrivateKeyPath: "~/sshmgr-test-keys/id_test"})
		f.trust()
		f.mustCall(execArgs("echo hi"))
	})
	t.Run("missing key", func(t *testing.T) {
		f := newFixture(t, sshtest.Config{PublicKey: true}, sshhosts.HostInput{AuthType: sshhosts.AuthPrivateKey})
		f.failCall(execArgs("echo hi"), "no saved private key")
		if f.srv.Logins() != 0 {
			t.Fatal("connected without a key")
		}
	})
}

// setKeyHost points the fixture's host at f.srv with a private key login.
func setKeyHost(t *testing.T, f *fixture, in sshhosts.HostInput) {
	t.Helper()
	in.ID, in.Name, in.Host, in.Port, in.Username, in.AuthType = "web", "Web", f.srv.Host, sshhosts.PortValue(f.srv.Port), f.srv.User, sshhosts.AuthPrivateKey
	if _, _, err := f.store.Update(0, func(doc *sshhosts.Document) error {
		_, err := sshhosts.PutHost(doc, in)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUnattendedFailsFast(t *testing.T) {
	cases := []struct {
		name  string
		cfg   sshtest.Config
		host  sshhosts.HostInput
		trust bool
		want  string
	}{
		{"unknown host key", sshtest.Config{Password: "pw"}, passwordHost("pw"), false, "no trusted host key yet"},
		{"missing password", sshtest.Config{Password: "pw"}, sshhosts.HostInput{AuthType: sshhosts.AuthPassword}, true, "unattended run cannot ask"},
		{"keyboard-interactive", sshtest.Config{Questions: []sshtest.Question{{Prompt: "Code: ", Answer: "1"}}}, sshhosts.HostInput{AuthType: sshhosts.AuthKeyboardInteractive}, true, "unattended run cannot answer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, tc.cfg, tc.host)
			f.unattended = true
			if tc.trust {
				f.trust()
			}
			started := time.Now()
			f.failCall(execArgs("echo hi"), tc.want)
			if time.Since(started) > 5*time.Second {
				t.Fatalf("took %s", time.Since(started))
			}
			if asks, elicits, _ := f.counts(); asks != 0 || elicits != 0 || f.srv.Logins() != 0 {
				t.Fatalf("asks=%d elicits=%d logins=%d", asks, elicits, f.srv.Logins())
			}
		})
	}
	t.Run("encrypted key without passphrase", func(t *testing.T) {
		f := newFixture(t, sshtest.Config{PublicKey: true}, sshhosts.HostInput{AuthType: sshhosts.AuthPrivateKey})
		srv := f.srv
		setKeyHost(t, f, sshhosts.HostInput{PrivateKey: string(srv.EncryptedClientKey(t, "phrase"))})
		f.trust()
		f.unattended = true
		f.failCall(execArgs("echo hi"), "no passphrase is saved")
		if _, elicits, _ := f.counts(); elicits != 0 {
			t.Fatalf("elicits = %d", elicits)
		}
	})
	t.Run("saved credential and trusted key", func(t *testing.T) {
		f := newFixture(t, sshtest.Config{Password: "pw"}, passwordHost("pw"))
		f.unattended = true
		f.trust()
		f.mustCall(execArgs("echo hi"))
	})
}

func TestConnectCancelledWhileAsking(t *testing.T) {
	f := newFixture(t, sshtest.Config{Password: "pw"}, sshhosts.HostInput{AuthType: sshhosts.AuthPassword})
	f.trust()
	ctx, cancel := context.WithCancel(f.ctx())
	f.answer = func(req elicit.Request) elicit.Response {
		cancel()
		return elicit.Response{Action: elicit.Cancel, Reason: elicit.ReasonCancelled}
	}
	_, err := f.callCtx(ctx, execArgs("echo hi"))
	if err != context.Canceled {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.m.Sessions(sshhosts.ProjectPathKey(f.workdir))); n != 0 {
		t.Fatalf("sessions = %d", n)
	}
}

func TestPreflightNeedsUsername(t *testing.T) {
	f := newFixture(t, sshtest.Config{Password: "pw"}, passwordHost("pw"))
	if _, _, err := f.store.Update(0, func(doc *sshhosts.Document) error {
		doc.Hosts[0].Username = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.failCall(execArgs("echo hi"), "has no username")
}
