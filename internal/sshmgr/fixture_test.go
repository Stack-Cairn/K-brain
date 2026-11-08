package sshmgr

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/elicit"
	"github.com/Stack-Cairn/K-brain/internal/sshhosts"
	"github.com/Stack-Cairn/K-brain/internal/sshmgr/sshtest"
	"github.com/Stack-Cairn/K-brain/internal/testhome"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// TestMain points HOME at a throwaway directory so nothing here can read or
// write the user's own ~/.ssh or K-brain settings.
func TestMain(m *testing.M) { os.Exit(testhome.Run(m)) }

// fixture is a manager with one host ("web") pointing at an in-process SSH
// server and associated with a temporary project directory.
type fixture struct {
	t       *testing.T
	srv     *sshtest.Server
	store   *sshhosts.Store
	m       *Manager
	workdir string

	mu          sync.Mutex
	unattended  bool
	noAsk       bool
	noElicit    bool
	hostKeyPick string
	asks        []tools.AskRequest
	elicits     []elicit.Request
	answer      func(elicit.Request) elicit.Response
	gates       []tools.GateRequest
	reject      bool
}

func newFixture(t *testing.T, cfg sshtest.Config, host sshhosts.HostInput) *fixture {
	t.Helper()
	srv := sshtest.New(t, cfg)
	store, err := sshhosts.Open(filepath.Join(t.TempDir(), "ssh"))
	if err != nil {
		t.Fatal(err)
	}
	workdir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if host.ID == "" {
		host.ID = "web"
	}
	if host.Name == "" {
		host.Name = "Web"
	}
	host.Host, host.Port = srv.Host, sshhosts.PortValue(srv.Port)
	if host.Username == "" {
		host.Username = srv.User
	}
	hosts := []sshhosts.HostInput{host}
	assoc := map[string][]string{workdir: {host.ID}}
	if _, _, err := store.Update(0, sshhosts.DocumentInput{Hosts: &hosts, ProjectHostAssociations: &assoc}.ApplyTo); err != nil {
		t.Fatal(err)
	}
	m := New(store, Options{})
	t.Cleanup(m.Close)
	return &fixture{t: t, srv: srv, store: store, m: m, workdir: workdir, hostKeyPick: hostKeyTrustSave}
}

// passwordHost is a password host input with password saved.
func passwordHost(password string) sshhosts.HostInput {
	return sshhosts.HostInput{AuthType: sshhosts.AuthPassword, Password: password}
}

func (f *fixture) interaction(context.Context) Interaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	inter := Interaction{Unattended: f.unattended}
	if !f.noAsk {
		inter.Ask = f.ask
	}
	if !f.noElicit {
		inter.Elicit = f.elicit
	}
	return inter
}

func (f *fixture) ask(_ context.Context, req tools.AskRequest) ([]tools.AskAnswer, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asks = append(f.asks, req)
	if f.hostKeyPick == "" {
		return nil, false
	}
	return []tools.AskAnswer{{Labels: []string{f.hostKeyPick}}}, true
}

func (f *fixture) elicit(_ context.Context, req elicit.Request) elicit.Response {
	f.mu.Lock()
	f.elicits = append(f.elicits, req)
	answer := f.answer
	f.mu.Unlock()
	if answer == nil {
		return elicit.Response{Action: elicit.Decline, Reason: "user"}
	}
	return answer(req)
}

func (f *fixture) gate(req tools.GateRequest) (tools.GateDecision, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gates = append(f.gates, req)
	if f.reject {
		return tools.GateReject, ""
	}
	return tools.GateAllowOnce, ""
}

func (f *fixture) ctx() context.Context {
	ctx := tools.WithWorkingDir(context.Background(), f.workdir)
	return tools.WithGate(ctx, f.gate)
}

func (f *fixture) tool() tools.Tool {
	return Tool(f.m, ToolOptions{Interaction: f.interaction})
}

// call runs SSHManager with args in the fixture's project.
func (f *fixture) call(args map[string]any) (string, error) {
	f.t.Helper()
	return f.callCtx(f.ctx(), args)
}

func (f *fixture) callCtx(ctx context.Context, args map[string]any) (string, error) {
	f.t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		f.t.Fatal(err)
	}
	return f.tool().Run(ctx, raw)
}

// mustCall runs SSHManager and fails the test on an error.
func (f *fixture) mustCall(args map[string]any) string {
	f.t.Helper()
	out, err := f.call(args)
	if err != nil {
		f.t.Fatalf("%v: %v", args, err)
	}
	return out
}

// failCall runs SSHManager and requires an error containing want.
func (f *fixture) failCall(args map[string]any, want string) string {
	f.t.Helper()
	out, err := f.call(args)
	if err == nil {
		f.t.Fatalf("%v succeeded: %s", args, out)
	}
	if !strings.Contains(err.Error(), want) {
		f.t.Fatalf("%v: error %q does not contain %q", args, err, want)
	}
	return err.Error()
}

func (f *fixture) counts() (asks, elicits, gates int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.asks), len(f.elicits), len(f.gates)
}

func (f *fixture) lastGate() tools.GateRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.gates) == 0 {
		f.t.Fatal("no gate request")
	}
	return f.gates[len(f.gates)-1]
}

// trust saves the server's current host key, as "Trust and save" does.
func (f *fixture) trust() {
	f.t.Helper()
	if err := f.store.KnownHosts().Add(f.srv.Address(), f.srv.HostKey()); err != nil {
		f.t.Fatal(err)
	}
}

// field returns the "key: value" line value of a tool result.
func field(t *testing.T, text, key string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if v, ok := strings.CutPrefix(line, key+": "); ok {
			return v
		}
	}
	t.Fatalf("no %s in %q", key, text)
	return ""
}

// waitFor polls cond for up to 10 seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
