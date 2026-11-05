package tui

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func pickerModel(t *testing.T) *model {
	t.Helper()
	m := resumeBrowseModel(t)
	m.Update(mkWinSize(80, 24))
	return m
}

// typeFilter types s into the open picker and runs the debounced search the
// last keystroke scheduled, as the program would once typing pauses.
func typeFilter(t *testing.T, m *model, s string) {
	t.Helper()
	for _, r := range s {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if !m.picker.pending {
		t.Fatal("typing should schedule the full-text search")
	}
	fire := m.pickerSearchFire(pickerSearchTick{seq: m.picker.seq})
	if fire == nil {
		t.Fatal("the latest tick should start the search")
	}
	m.Update(fire())
}

func TestPickerFilterAsYouType(t *testing.T) {
	m := pickerModel(t)
	seedSession(t, m.store, "/repo/api", "add rate limiting to the gateway")
	seedSession(t, m.store, "/repo/web", "fix the login form")
	newest := seedSession(t, m.store, "/repo/web", "  \n\nrefactor the parser\nand its tests")
	// An untitled session (an old one, or one renamed to nothing) falls back
	// to its first prompt line.
	if err := m.store.SetTitle(newest, ""); err != nil {
		t.Fatal(err)
	}
	m.command("/resume")
	if m.picker == nil || len(m.picker.metas) != 3 {
		t.Fatal("/resume should list all three sessions")
	}
	if m.picker.metas[0].ID != newest {
		t.Fatal("the newest session is first")
	}
	v := surfScreen(m)
	for _, want := range []string{"Resume session", "1/3", "refactor the parser", "fix the login form", "type to filter"} {
		if !strings.Contains(v, want) {
			t.Fatalf("picker lacks %q:\n%s", want, v)
		}
	}
	// The first non-blank line of the first prompt titles an untitled session.
	if got := m.pickerTitle(m.picker.metas[0]); got != "refactor the parser" {
		t.Fatalf("fallback title = %q", got)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("login")})
	if len(m.picker.metas) != 1 || !strings.Contains(m.pickerTitle(m.picker.metas[0]), "login") {
		t.Fatalf("filter should narrow at once: %+v", m.picker.metas)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" web")})
	if len(m.picker.metas) != 1 {
		t.Fatal("terms match title and directory together")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.picker == nil || m.picker.filter != "" || len(m.picker.metas) != 3 {
		t.Fatal("Esc clears the filter first")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.picker != nil {
		t.Fatal("second Esc closes")
	}
}

func TestPickerFullTextSearch(t *testing.T) {
	m := pickerModel(t)
	seedSession(t, m.store, "/repo", "deploy the frontend")
	hit := seedSession(t, m.store, "/repo", "fix the login bug that shows up after a password reset on mobile\nthe kubernetes secret rotation broke staging")
	m.openPicker()
	typeFilter(t, m, "kubernetes")
	if len(m.picker.metas) != 1 || m.picker.metas[0].ID != hit {
		t.Fatalf("message text should be searched: %+v", m.picker.metas)
	}
	v := surfScreen(m)
	for _, want := range []string{"text match", "fix the login bug", "kubernetes secret rotation"} {
		if !strings.Contains(v, want) {
			t.Fatalf("search result lacks %q:\n%s", want, v)
		}
	}
	// A stale search result (the filter moved on) is dropped.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m.pickerSearchDone(pickerSearchMsg{seq: m.picker.seq - 1, query: "kubernetes", metas: m.picker.hits})
	if len(m.picker.metas) != 0 {
		t.Fatal("a stale search must not repopulate the list")
	}
	if !strings.Contains(surfScreen(m), "searching message text") {
		t.Fatal("pending search is shown")
	}
}

func TestPickerDeleteWithConfirm(t *testing.T) {
	m := pickerModel(t)
	keep := seedSession(t, m.store, "/repo", "keep me")
	gone := seedSession(t, m.store, "/repo", "delete me")
	m.openPicker()
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if m.picker.confirm != gone || !strings.Contains(surfScreen(m), "delete session \"delete me\"?") {
		t.Fatalf("Ctrl+D asks first:\n%s", surfScreen(m))
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if _, _, err := m.store.Load(gone); err != nil {
		t.Fatal("any other key cancels")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if _, _, err := m.store.Load(gone); err == nil {
		t.Fatal("y deletes the session from the store")
	}
	if len(m.picker.metas) != 1 || m.picker.metas[0].ID != keep {
		t.Fatalf("the list drops it: %+v", m.picker.metas)
	}
	if !strings.Contains(surfScreen(m), "deleted session "+gone) {
		t.Fatal("the deletion is reported")
	}
	// The running session is not deletable from here.
	m.sessionID = keep
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if m.picker.confirm != "" || !strings.Contains(surfScreen(m), "current session cannot be deleted") {
		t.Fatal("the current session is refused")
	}
	if !strings.Contains(surfScreen(m), "(current)") {
		t.Fatal("the current session is marked")
	}
}

func TestPickerPagesBeyondFifty(t *testing.T) {
	m := pickerModel(t)
	const n = pickerPageSize + 7
	for i := range n {
		seedSession(t, m.store, "/repo", "session "+string(rune('a'+i%26))+strings.Repeat("!", i/26))
	}
	m.openPicker()
	if len(m.picker.all) != pickerPageSize || m.picker.total != n {
		t.Fatalf("first page: %d loaded of %d", len(m.picker.all), m.picker.total)
	}
	if v := surfScreen(m); !strings.Contains(v, "1/57") {
		t.Fatalf("the count covers every session:\n%s", v)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if len(m.picker.all) != n || m.picker.idx != n-1 || m.picker.next != nil {
		t.Fatalf("scrolling past the end loads the rest: %d loaded, idx %d", len(m.picker.all), m.picker.idx)
	}
	seen := map[string]bool{}
	for _, meta := range m.picker.all {
		if seen[meta.ID] {
			t.Fatal("pages overlap")
		}
		seen[meta.ID] = true
	}
}

// ── Rewind ──

func rewindChoiceModel(t *testing.T) (*model, string) {
	t.Helper()
	repo := rewindRepo(t)
	snap := snapshotWorkspace()
	if snap == "" {
		t.Fatal("snapshot failed")
	}
	writeRewindFile(t, repo, "a.txt", "edited by the agent\n")
	m := rewindModel(t,
		ai.Message{Role: "user", Content: "q1", Authored: true},
		ai.Message{Role: "assistant", Content: "a1"},
	)
	m.snapshots = map[int]string{1: snap}
	if err := m.store.SetSnapshot(m.sessionID, 1, snap); err != nil {
		t.Fatal(err)
	}
	m.openRewind()
	m.rewindKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.rew == nil || !m.rew.choosing {
		t.Fatal("a turn with a snapshot asks what to rewind")
	}
	return m, repo
}

func readRewindFile(t *testing.T, repo string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRewindChoiceOffersThreeModes(t *testing.T) {
	m, _ := rewindChoiceModel(t)
	m.height = 24
	v := surfScreen(m)
	for _, want := range []string{"Rewind", "Conversation and code", "Conversation only", "Code only", "q1"} {
		if !strings.Contains(v, want) {
			t.Fatalf("choice lacks %q:\n%s", want, v)
		}
	}
	m.rewindKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.rew == nil || m.rew.choosing {
		t.Fatal("Esc goes back to the list")
	}
}

func TestRewindConversationAndCode(t *testing.T) {
	m, repo := rewindChoiceModel(t)
	m.rewindKey(tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.agent.Messages) != 1 || readRewindFile(t, repo) != "base\n" || m.input.Value() != "q1" {
		t.Fatalf("both: msgs=%d file=%q input=%q", len(m.agent.Messages), readRewindFile(t, repo), m.input.Value())
	}
}

func TestRewindConversationOnly(t *testing.T) {
	m, repo := rewindChoiceModel(t)
	m.rewindKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if len(m.agent.Messages) != 1 || m.input.Value() != "q1" {
		t.Fatalf("conversation only rewinds the conversation: msgs=%d input=%q", len(m.agent.Messages), m.input.Value())
	}
	if readRewindFile(t, repo) != "edited by the agent\n" {
		t.Fatal("conversation only leaves the files alone")
	}
}

func TestRewindCodeOnly(t *testing.T) {
	m, repo := rewindChoiceModel(t)
	before := append([]ai.Message(nil), m.agent.Messages...)
	m.input.SetValue("draft")
	m.rewindKey(tea.KeyMsg{Type: tea.KeyDown})
	m.rewindKey(tea.KeyMsg{Type: tea.KeyDown})
	m.rewindKey(tea.KeyMsg{Type: tea.KeyEnter})
	if readRewindFile(t, repo) != "base\n" {
		t.Fatal("code only restores the files")
	}
	if !reflect.DeepEqual(m.agent.Messages, before) || len(m.future) != 0 || m.snapshots[1] == "" {
		t.Fatal("code only keeps the conversation and its snapshots")
	}
	if m.input.Value() != "draft" || m.rew != nil {
		t.Fatal("code only keeps the draft and closes")
	}
	if !strings.Contains(lastBlock(m), "conversation kept") {
		t.Fatalf("result: %q", lastBlock(m))
	}
}

func TestRewindWithoutSnapshotSkipsChoice(t *testing.T) {
	m := rewindModel(t,
		ai.Message{Role: "user", Content: "q1", Authored: true},
		ai.Message{Role: "assistant", Content: "a1"},
	)
	m.openRewind()
	m.rewindKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.rew != nil || len(m.agent.Messages) != 1 {
		t.Fatal("with no snapshot there is nothing to choose: rewind at once")
	}
}

// ── Model cascade ──

func cascadeModel(t *testing.T) *model {
	t.Helper()
	m := surfaceModel(t)
	m.cfg.Providers["long"] = config.Provider{BaseURL: "https://long", APIKey: "k"}
	m.cfg.Models["glm-5.2-fast"] = config.Model{
		Providers: []string{"inference", "long"},
		Context:   131072,
		ProviderMetadata: map[string]config.Model{
			"long": {Context: 1 << 20},
		},
	}
	return m
}

func TestModelPickerCascade(t *testing.T) {
	m := cascadeModel(t)
	m.openModelPicker(false)
	v := surfScreen(m)
	if !strings.Contains(v, "glm-5.2-fast") || !strings.Contains(v, "2 routes · 128k–1M ctx") {
		t.Fatalf("model rows summarise their routes:\n%s", v)
	}
	// ↑/↓ move between models, not routes.
	for m.mpicker.view()[m.mpicker.idx].model != "glm-5.2-fast" {
		m.Update(tea.KeyMsg{Type: tea.KeyUp})
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mpicker == nil || m.mpicker.stage != stageRoute {
		t.Fatal("a model with two routes asks for the route")
	}
	v = surfScreen(m)
	for _, want := range []string{"choose a route and context window", "inference", "128k ctx", "long", "1M ctx"} {
		if !strings.Contains(v, want) {
			t.Fatalf("route step lacks %q:\n%s", want, v)
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.modelName != "glm-5.2-fast" || m.provName != "long" {
		t.Fatalf("Enter switches to the route: %s@%s", m.modelName, m.provName)
	}
	if m.mpicker == nil || m.mpicker.stage != stageEffort {
		t.Fatal("then asks for the effort")
	}
	if v := surfScreen(m); !strings.Contains(v, "reasoning effort") || !strings.Contains(v, "high") {
		t.Fatalf("effort step:\n%s", v)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mpicker != nil || m.agent.Effort != "low" {
		t.Fatalf("Enter applies the effort: %q", m.agent.Effort)
	}
}

func TestModelPickerSingleRouteAndEscKeepsEffort(t *testing.T) {
	m := cascadeModel(t)
	m.openModelPicker(false)
	for m.mpicker.view()[m.mpicker.idx].model != "kimi-k3-fast" {
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mpicker == nil || m.mpicker.stage != stageEffort {
		t.Fatal("a single-route model goes straight to the effort step")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.mpicker != nil || m.agent.Effort != "" {
		t.Fatal("Esc keeps the current effort")
	}
	m.openModelPicker(false)
	for m.mpicker.view()[m.mpicker.idx].model != "glm-5.2-fast" {
		m.Update(tea.KeyMsg{Type: tea.KeyUp})
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.mpicker == nil || m.mpicker.stage != stageModel {
		t.Fatal("Esc on the route step goes back to the models")
	}
}

// ── Copy fidelity ──

func TestCopyVerifiesAndBacksUp(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	var board string
	var term strings.Builder
	c := clipboard{
		native:   func(s string) error { board = s; return nil },
		readback: func() (string, error) { return board + "\n", nil },
		term:     &term,
		backup:   writeCopyBackup,
	}
	r := c.deliver("hello\r\nworld")
	if r.err != nil || r.route != copyNative || !r.verified {
		t.Fatalf("a read-back copy is verified: %+v", r)
	}
	dir, _ := config.Dir()
	path := filepath.Join(dir, copyBackupName)
	if r.backup != path {
		t.Fatalf("backup = %q", r.backup)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "hello\r\nworld" {
		t.Fatalf("backup content %q %v", b, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode %v", fi.Mode().Perm())
	}
	if got := copyReportNotice(r); got != "Reply copied to clipboard (verified)" {
		t.Fatal(got)
	}
	if term.Len() != 0 {
		t.Fatal("a verified copy sends no OSC 52")
	}
}

func TestCopyMismatchFallsBackToOSC52(t *testing.T) {
	var term strings.Builder
	c := clipboard{
		native:   func(string) error { return nil },
		readback: func() (string, error) { return "something else", nil },
		term:     &term,
		backup:   func(string) (string, error) { return "/data/last-copy.txt", nil },
	}
	r := c.deliver("hello")
	if r.err != nil || r.route != copyOSC52 || r.verified {
		t.Fatalf("a tool that lied falls back to OSC 52: %+v", r)
	}
	if !strings.Contains(term.String(), "\x1b]52;c;") {
		t.Fatal("OSC 52 written")
	}
	if got := copyReportNotice(r); !strings.Contains(got, "unconfirmed") || !strings.Contains(got, "/data/last-copy.txt") {
		t.Fatal(got)
	}
	// Without a terminal route the failure points at the backup.
	c.term = nil
	r = c.deliver("hello")
	if r.err == nil || !strings.Contains(copyReportNotice(r), "saved in /data/last-copy.txt") {
		t.Fatalf("%+v %q", r, copyReportNotice(r))
	}
	// An unreadable clipboard keeps the tool's word, unverified.
	c = clipboard{native: func(string) error { return nil }, readback: func() (string, error) { return "", errors.New("no reader") }}
	if r := c.deliver("x"); r.route != copyNative || r.verified || copyReportNotice(r) != "Reply copied to clipboard" {
		t.Fatalf("%+v", r)
	}
}

// ── Onboarding ──

func TestOnboardingSteps(t *testing.T) {
	o := newOnboardModel("/work/repo", "/home/u/.k-brain/config.json", true, true)
	o.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if !strings.Contains(o.View(), "Welcome to k-brain") || !strings.Contains(o.View(), "step 1 of 3") {
		t.Fatalf("welcome:\n%s", o.View())
	}
	o.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if v := o.View(); !strings.Contains(v, "Do you trust the files in this folder?") || !strings.Contains(v, "/work/repo") {
		t.Fatalf("trust:\n%s", v)
	}
	o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	o.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !o.done || !o.trusted || o.thinking || cmd == nil {
		t.Fatalf("answers: %+v", o)
	}
	if lines := strings.Split(o.View(), "\n"); len(lines) != 1 {
		t.Fatal("a finished program draws nothing")
	}

	o = newOnboardModel("/work/repo", "", true, false)
	o.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !o.quit || o.trusted {
		t.Fatal("Esc on the trust step declines")
	}
}

func TestApplyOnboardingTrustsAndSaves(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	wd := t.TempDir()
	cfg := config.Default()
	o := newOnboardModel(wd, "", true, true)
	o.choose(0)
	o.choose(0)
	o.choose(1)
	ok, err := applyOnboarding(cfg, wd, o)
	if err != nil || !ok {
		t.Fatalf("apply: %v %v", ok, err)
	}
	if !config.Trusted(wd) {
		t.Fatal("the folder is trusted")
	}
	saved, err := config.Load()
	if err != nil || saved.Thinking == nil || *saved.Thinking {
		t.Fatalf("thinking choice saved: %v %+v", err, saved.Thinking)
	}

	declined := newOnboardModel(t.TempDir(), "", true, false)
	declined.choose(1)
	if ok, err := applyOnboarding(cfg, declined.wd, declined); ok || err != nil || config.Trusted(declined.wd) {
		t.Fatal("a declined folder is not trusted")
	}
}
