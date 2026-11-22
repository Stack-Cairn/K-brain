package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/session"
)

// dashModel is a model on a temp session store in a temp working directory,
// with the store's directory returned for a second handle on it.
func dashModel(t *testing.T) (*model, string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Chdir(home)
	dir := filepath.Join(home, "sessions")
	st, err := session.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := compactCmdModel()
	m.store = st
	m.Update(mkWinSize(100, 30))
	return m, dir
}

func dashScreen(t *testing.T, m *model) string {
	t.Helper()
	v := m.View()
	w, _ := m.surfaceSize()
	for i, l := range strings.Split(v, "\n") {
		if lw := lipgloss.Width(l); lw > w {
			t.Errorf("row %d is %d cells wide on a %d-column screen: %q", i, lw, w, ansi.Strip(l))
		}
	}
	return ansi.Strip(v)
}

func TestDashboardOpensFromCommandAndPalette(t *testing.T) {
	m, _ := dashModel(t)
	seedSession(t, m.store, cwd(), "local work")
	m.command("/dashboard")
	if !m.dashOpen() || !m.surfaceOpen() || !m.overlayOwnsKeys() {
		t.Fatal("/dashboard did not open the dashboard")
	}
	if v := dashScreen(t, m); !strings.Contains(v, "Dashboard") || !strings.Contains(v, "local work") {
		t.Fatalf("dashboard screen:\n%s", v)
	}
	m = keys(t, m, "esc")
	if m.dashOpen() || m.surfaceOpen() {
		t.Fatal("Esc should close the dashboard")
	}
	if !busyCmd("/dashboard") || !execNow["/dashboard"] {
		t.Error("/dashboard should run at once and while busy")
	}
	found := false
	for _, it := range m.paletteItems() {
		if it.title == "Dashboard" {
			found = true
			it.run(m)
		}
	}
	if !found || !m.dashOpen() {
		t.Fatal("the palette has no Dashboard entry that opens it")
	}
}

func TestDashboardSectionsAndStatus(t *testing.T) {
	m, dir := dashModel(t)
	seedSession(t, m.store, "/srv/elsewhere/api", "gateway rate limiter")
	idle := seedSession(t, m.store, "/srv/elsewhere/web", "web console")
	cur := seedSession(t, m.store, cwd(), "current work")
	m.sessionID = cur

	// A second handle on the store stands in for another kn running a
	// turn in the first session.
	other, err := session.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	page, _ := m.store.ListPage(t.Context(), session.PageOptions{Limit: 10})
	var running string
	for _, meta := range page.Sessions {
		if strings.Contains(meta.Title, "gateway") || meta.CWD == "/srv/elsewhere/api" {
			running = meta.ID
		}
	}
	lease, err := other.AcquireRun(running, "kn")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	m.busy = true
	m.command("/dashboard")
	v := dashScreen(t, m)
	for _, want := range []string{"This workspace", "Other workspaces", "current work", "(current)", "gateway rate limiter", "web console"} {
		if !strings.Contains(v, want) {
			t.Errorf("dashboard lacks %q:\n%s", want, v)
		}
	}
	if strings.Index(v, "This workspace") > strings.Index(v, "Other workspaces") {
		t.Errorf("this workspace should come first:\n%s", v)
	}
	status := func(id string) dashStatus {
		for i := range m.app.dash.rows {
			if r := m.app.dash.rows[i]; r.entry != nil && r.entry.meta.ID == id {
				st, _ := m.sessionStatus(r.entry)
				return st
			}
		}
		t.Fatalf("no row for %s", id)
		return 0
	}
	if st := status(cur); st != dashRunning {
		t.Errorf("busy current session is %v, want running", st.word())
	}
	if st := status(running); st != dashRunning {
		t.Errorf("session leased elsewhere is %v, want running", st.word())
	}
	if st := status(idle); st != dashIdle {
		t.Errorf("unleased session is %v, want idle", st.word())
	}
	m.busy = false
	m.permDialog = &permDialog{}
	if st := status(cur); st != dashWaiting {
		t.Errorf("current session with a permission card is %v, want waiting", st.word())
	}
	m.permDialog = nil

	// The preview says what the status rests on.
	for i := 0; i < 10 && m.app.dash.rows[m.app.dash.sel].entry.meta.ID != running; i++ {
		m = keys(t, m, "j")
	}
	if v := dashScreen(t, m); !strings.Contains(v, "running a turn in kn (pid") {
		t.Errorf("preview should name the process running the turn:\n%s", v)
	}
	m = keys(t, m, "k")
	if v := dashScreen(t, m); !strings.Contains(v, "(stored state)") {
		t.Errorf("an idle session should say its state is the stored one:\n%s", v)
	}
	// Deleting a session another process is running is refused.
	for i := 0; i < 10 && m.app.dash.rows[m.app.dash.sel].entry.meta.ID != running; i++ {
		m = keys(t, m, "j")
	}
	m = keys(t, m, "d")
	if m.app.dash.confirm != "" || !strings.Contains(m.app.dash.note, "running a turn elsewhere") {
		t.Errorf("delete of a running session: confirm %q note %q", m.app.dash.confirm, m.app.dash.note)
	}
}

func TestDashboardFilter(t *testing.T) {
	m, _ := dashModel(t)
	seedSession(t, m.store, cwd(), "parser rewrite")
	seedSession(t, m.store, "/srv/api-gateway", "rate limiter")
	seedSession(t, m.store, "/srv/web", "console styling")
	m.command("/dashboard")
	m = keys(t, m, "/", "g", "a", "t", "e")
	d := m.app.dash
	if !d.typing || d.filter != "gate" {
		t.Fatalf("typing %v filter %q", d.typing, d.filter)
	}
	v := dashScreen(t, m)
	if !strings.Contains(v, "rate limiter") || strings.Contains(v, "parser rewrite") || strings.Contains(v, "console styling") {
		t.Fatalf("filter by workspace:\n%s", v)
	}
	if !strings.Contains(v, "1 of 3 sessions") {
		t.Errorf("header should count the matches:\n%s", v)
	}
	// Enter ends typing; letters are keys again.
	m = keys(t, m, "enter", "r")
	if d.typing || d.filter != "gate" || d.note != "refreshed" {
		t.Errorf("after Enter, r: typing %v filter %q note %q", d.typing, d.filter, d.note)
	}
	m = keys(t, m, "/", "x", "y", "z")
	if v := dashScreen(t, m); !strings.Contains(v, "no sessions match") {
		t.Errorf("no match:\n%s", v)
	}
	// Esc while typing clears the filter; a second Esc closes.
	m = keys(t, m, "esc")
	if d.typing || d.filter != "" || len(d.rows) != 3 {
		t.Errorf("Esc should clear the filter: typing %v filter %q rows %d", d.typing, d.filter, len(d.rows))
	}
	m = keys(t, m, "esc")
	if m.dashOpen() {
		t.Error("second Esc should close")
	}
}

func TestDashboardGroupByWorkspace(t *testing.T) {
	m, _ := dashModel(t)
	seedSession(t, m.store, "/srv/api", "api one")
	seedSession(t, m.store, "/srv/web", "web one")
	seedSession(t, m.store, "/srv/api", "api two")
	seedSession(t, m.store, cwd(), "here")
	m.command("/dashboard")
	m.app.dash.rows[0].entry.meta.CWD = cwd()
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if !m.app.dash.grouped {
		t.Fatal("Tab should group by workspace")
	}
	v := dashScreen(t, m)
	here, api, web := strings.Index(v, "\n  ~\n"), strings.Index(v, "\n  /srv/api\n"), strings.Index(v, "\n  /srv/web\n")
	if here < 0 || api < 0 || web < 0 || here > api || api > web {
		t.Fatalf("want headings ~, /srv/api, /srv/web in that order:\n%s", v)
	}
	if strings.Count(v[api:web], " one") != 1 || strings.Count(v[api:web], "api two") != 1 {
		t.Errorf("both api sessions belong under /srv/api:\n%s", v)
	}
	if !strings.Contains(v, "by workspace") {
		t.Errorf("header should say the list is grouped:\n%s", v)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.app.dash.grouped {
		t.Error("Tab again should ungroup")
	}
}

func TestDashboardDeleteResumeAndNew(t *testing.T) {
	m, _ := dashModel(t)
	gone := seedSession(t, m.store, "/srv/old", "old experiment")
	keep := seedSession(t, m.store, "/srv/keep", "keep me")
	cur := seedSession(t, m.store, cwd(), "current")
	m.sessionID = cur
	m.command("/dashboard")
	d := m.app.dash
	sel := func(id string) {
		t.Helper()
		for i, r := range d.rows {
			if r.entry != nil && r.entry.meta.ID == id {
				d.sel = i
				return
			}
		}
		t.Fatalf("no row for %s", id)
	}

	sel(cur)
	m = keys(t, m, "d")
	if d.confirm != "" || !strings.Contains(d.note, "cannot be deleted") {
		t.Fatalf("current session delete: confirm %q note %q", d.confirm, d.note)
	}
	sel(gone)
	m = keys(t, m, "d")
	if d.confirm != gone || !strings.Contains(dashScreen(t, m), `delete session "old experiment"? y to confirm`) {
		t.Fatalf("no confirmation:\n%s", dashScreen(t, m))
	}
	m = keys(t, m, "n")
	if d.confirm != "" || d.note != "delete cancelled" || !m.dashOpen() {
		t.Fatalf("a key other than y should cancel: %q", d.note)
	}
	m = keys(t, m, "d", "y")
	if _, _, err := m.store.Load(gone); err == nil {
		t.Fatal("confirmed delete left the session in the store")
	}
	if strings.Contains(dashScreen(t, m), "old experiment") || !strings.Contains(d.note, "deleted session") {
		t.Fatalf("deleted session still listed:\n%s", dashScreen(t, m))
	}

	// Enter on the current session just closes.
	sel(cur)
	m = keys(t, m, "enter")
	if m.dashOpen() || m.sessionID != cur {
		t.Fatal("Enter on the current session should close and stay")
	}
	// Enter on another resumes it, unless a turn is running here.
	m.command("/dashboard")
	d = m.app.dash
	sel(keep)
	m.busy = true
	m = keys(t, m, "enter")
	if !m.dashOpen() || !strings.Contains(d.note, "busy") {
		t.Fatalf("Enter while busy: note %q", d.note)
	}
	m.busy = false
	m = keys(t, m, "enter")
	if m.dashOpen() || m.sessionID != keep {
		t.Fatalf("Enter should resume %s, session is %s", keep, m.sessionID)
	}
	// n starts a new session.
	m.command("/dashboard")
	m = keys(t, m, "n")
	if m.dashOpen() || m.sessionID == keep {
		t.Fatalf("n should close and start a new session (session %q)", m.sessionID)
	}
}

func TestDashboardGoalTodosAndUsage(t *testing.T) {
	m, _ := dashModel(t)
	id := seedSession(t, m.store, cwd(), "ship it")
	if err := m.store.SetGoal(id, "ship the dashboard"); err != nil {
		t.Fatal(err)
	}
	todos := `[{"id":"t1","content":"write tests","status":"completed"},{"id":"t2","content":"write docs","status":"in_progress"},{"id":"t3","content":"commit","status":"pending"}]`
	if err := m.store.SetTodos(id, todos); err != nil {
		t.Fatal(err)
	}
	m.command("/dashboard")
	m.app.dash.entries[0].meta.UsageIn, m.app.dash.entries[0].meta.UsageOut = 12000, 345
	m.Update(mkWinSize(130, 30))
	v := dashScreen(t, m)
	for _, want := range []string{"goal: ship the dashboard", "todos 1/3 done · next: write docs", "12.3k tokens", "12.3k"} {
		if !strings.Contains(v, want) {
			t.Errorf("preview lacks %q:\n%s", want, v)
		}
	}
}

func TestDashboardTicksAndEmpty(t *testing.T) {
	m, _ := dashModel(t)
	cmd := m.openDashboard()
	if cmd == nil {
		t.Fatal("opening should schedule a refresh")
	}
	if v := dashScreen(t, m); !strings.Contains(v, "no sessions yet") {
		t.Fatalf("empty dashboard:\n%s", v)
	}
	seedSession(t, m.store, cwd(), "arrived later")
	seq := m.app.dash.seq
	if m.dashTick(dashTickMsg{seq: seq - 1}) != nil {
		t.Error("a stale tick should stop")
	}
	if m.dashTick(dashTickMsg{seq: seq}) == nil {
		t.Error("a live tick should schedule the next one")
	}
	if v := dashScreen(t, m); !strings.Contains(v, "arrived later") {
		t.Fatalf("tick should refresh:\n%s", v)
	}
	m.closeSurface()
	if m.dashTick(dashTickMsg{seq: seq}) != nil {
		t.Error("a closed dashboard should stop ticking")
	}
}

func TestDashboardFitsEveryWidth(t *testing.T) {
	m, _ := dashModel(t)
	seedSession(t, m.store, cwd(), "一个很长的中文标题，用来检查窄屏时的宽度截断是否正确")
	seedSession(t, m.store, "/a/very/long/path/that/keeps/going/and/going/forever", strings.Repeat("long title ", 12))
	for _, ascii := range []bool{false, true} {
		setASCIIGlyphs(ascii)
		m.command("/dashboard")
		for _, w := range []int{24, 40, 63, 64, 89, 90, 110, 120, 160} {
			m.Update(mkWinSize(w, 20))
			v := dashScreen(t, m)
			if n := len(strings.Split(v, "\n")); n != 20 {
				t.Errorf("width %d: %d rows, want 20", w, n)
			}
			if ascii {
				for _, r := range v {
					if r > 0x7e && !(r >= 0x4e00 && r <= 0x9fff) && r != '，' {
						t.Errorf("width %d: ascii view has %q", w, r)
						break
					}
				}
			}
		}
	}
	setASCIIGlyphs(false)
}

// Keys typed quickly arrive as one message; "/web" still starts a filter.
func TestDashboardFilterTypedInOneBurst(t *testing.T) {
	m, _ := dashModel(t)
	seedSession(t, m.store, "/srv/api", "api")
	seedSession(t, m.store, "/srv/web", "web")
	m.command("/dashboard")
	m = keys(t, m, "/web")
	if d := m.app.dash; !d.typing || d.filter != "web" || len(d.rows) != 1 {
		t.Fatalf("typing %v filter %q rows %d", d.typing, d.filter, len(d.rows))
	}
}
