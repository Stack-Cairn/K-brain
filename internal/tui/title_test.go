package tui

import (
	"github.com/charmbracelet/x/ansi"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestAutoTitle(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	st, _ := session.Open(filepath.Join(t.TempDir(), "sessions"))
	defer st.Close()
	m.store = st
	m.sessionID, _ = st.Create("/tmp", m.modelName, m.provName)
	m.agent.Messages = append(m.agent.Messages,
		ai.Message{Role: "user", Content: "how do I unstage a file", Authored: true},
		ai.Message{Role: "assistant", Content: "git restore --staged <file>"},
	)

	m.persist()
	m.maybeTitle()
	if !m.titled {
		t.Fatal("maybeTitle should mark the attempt")
	}

	m.Update(titleMsg{sessionID: m.sessionID, previousTitle: "how do I unstage a file", title: "unstage a file"})
	meta, _, _ := st.Load(m.sessionID)
	if meta.Title != "unstage a file" {
		t.Fatalf("title should be the model's, got %q", meta.Title)
	}

	m.maybeTitle()
}

func TestAutoTitleRespectsRename(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	st, _ := session.Open(filepath.Join(t.TempDir(), "sessions"))
	defer st.Close()
	m.store = st
	m.sessionID, _ = st.Create("/tmp", m.modelName, m.provName)
	m.agent.Messages = append(m.agent.Messages,
		ai.Message{Role: "user", Content: "original question", Authored: true},
	)
	m.persist()
	m.store.SetTitle(m.sessionID, "my title")
	m.Update(titleMsg{sessionID: m.sessionID, previousTitle: "original question", title: "model title"})
	meta, _, _ := st.Load(m.sessionID)
	if meta.Title != "my title" {
		t.Fatalf("a renamed session must keep its title, got %q", meta.Title)
	}
}

func TestFirstTurnDisplaysSessionTitle(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	st, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m.store = st
	m.agent.Client, m.agent.CompactClient = nil, nil
	m.Update(mkWinSize(80, 30))
	m.agent.Messages = append(m.agent.Messages,
		ai.Message{Role: "user", Content: "Fix the build", Authored: true},
		ai.Message{Role: "assistant", Content: "Build fixed"},
	)
	m.Update(turnDoneMsg{final: "Build fixed"})
	if !m.titled {
		t.Fatal("first turn did not reach title generation after persistence")
	}
	if m.sessTitle != "Fix the build" {
		t.Fatalf("fallback title = %q", m.sessTitle)
	}
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	if !strings.HasSuffix(lines[len(lines)-1], "Fix the build") {
		t.Fatalf("title is not on the bottom right: %q", lines[len(lines)-1])
	}
	m.Update(titleMsg{sessionID: m.sessionID, previousTitle: "Fix the build", title: "Build repair"})
	if m.sessTitle != "Build repair" {
		t.Fatalf("generated title not displayed: %q", m.sessTitle)
	}
}

func TestAutoTitleStaysWithOriginalSession(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	st, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m.store = st
	original, err := st.Create("/tmp", "model1", "provider")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetTitle(original, "same question"); err != nil {
		t.Fatal(err)
	}
	current, err := st.Create("/tmp", "model1", "provider")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetTitle(current, "same question"); err != nil {
		t.Fatal(err)
	}
	m.sessionID, m.sessTitle = current, "same question"
	m.Update(titleMsg{sessionID: original, previousTitle: "same question", title: "Original title"})
	if m.sessTitle != "same question" {
		t.Fatal("old title replaced active session title")
	}
	for id, want := range map[string]string{original: "Original title", current: "same question"} {
		meta, _, err := st.Load(id)
		if err != nil {
			t.Fatal(err)
		}
		if meta.Title != want {
			t.Fatalf("session %s title = %q, want %q", id, meta.Title, want)
		}
	}
}
