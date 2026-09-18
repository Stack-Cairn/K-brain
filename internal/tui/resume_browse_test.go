package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func resumeBrowseModel(t *testing.T) *model {
	t.Helper()
	st, err := session.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := compactCmdModel()
	m.store = st
	return m
}

func seedSession(t *testing.T, st *session.Store, cwd, content string) string {
	t.Helper()
	id, err := st.Create(cwd, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(id, 0, []ai.Message{{Role: "user", Content: content, Authored: true}}, "m", "p"); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestContinueRecentResumesNewestInCwd(t *testing.T) {
	m := resumeBrowseModel(t)

	seedSession(t, m.store, "/elsewhere", "other")
	seedSession(t, m.store, "/other-new", "newest-overall")
	want := seedSession(t, m.store, cwd(), "local work")

	if err := m.continueRecent(); err != nil {
		t.Fatalf("continueRecent: %v", err)
	}
	if m.sessionID != want {
		t.Fatalf("continueRecent resumed %q, want newest-in-cwd %q", m.sessionID, want)
	}
}

func TestContinueRecentNoSessionInDirStartsFresh(t *testing.T) {
	m := resumeBrowseModel(t)

	seedSession(t, m.store, "/somewhere-else", "x")
	before := len(m.blocks)
	hadSession := m.sessionID

	if err := m.continueRecent(); err != nil {
		t.Fatalf("continueRecent on empty dir should not error: %v", err)
	}
	if m.sessionID != hadSession {
		t.Fatalf("continueRecent resumed a foreign-dir session: sessionID=%q", m.sessionID)
	}
	if len(m.blocks) != before+1 {
		t.Fatalf("expected one notice block, got %d blocks (was %d)", len(m.blocks), before)
	}
	if !strings.Contains(ansi.Strip(m.blocks[before].render(m.width)), "no previous session in this directory") {
		t.Errorf("unexpected notice: %q", m.blocks[before].render(m.width))
	}
}

func TestBrowseOpensPickerAndEnterResumes(t *testing.T) {
	m := resumeBrowseModel(t)
	want := seedSession(t, m.store, cwd(), "browse me")

	m.openPicker()
	if m.picker == nil {
		t.Fatal("openPicker should set m.picker")
	}
	if len(m.picker.metas) != 1 || m.picker.metas[0].ID != want {
		t.Fatalf("picker row = %+v, want [%s]", m.picker.metas, want)
	}

	mm, _ := m.pickerKey(tea.KeyMsg{Type: tea.KeyEnter})
	if mm != m {
		t.Fatal("pickerKey should return the same model")
	}
	if m.picker != nil {
		t.Fatal("enter should dismiss the picker")
	}
	if m.sessionID != want {
		t.Fatalf("picker enter resumed %q, want %q", m.sessionID, want)
	}
}

func TestBrowseNoSessionsPrintsEmptyState(t *testing.T) {
	m := resumeBrowseModel(t)
	before := len(m.blocks)

	m.openPicker()
	if m.picker != nil {
		t.Fatal("openPicker with no sessions should not set a picker")
	}
	if len(m.blocks) != before+1 {
		t.Fatalf("expected one empty-state notice, got %d blocks (was %d)", len(m.blocks), before)
	}
	if !strings.Contains(ansi.Strip(m.blocks[before].render(m.width)), "no previous sessions") {
		t.Errorf("unexpected notice: %q", m.blocks[before].render(m.width))
	}
}
