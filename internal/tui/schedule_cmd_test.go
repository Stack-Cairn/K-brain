package tui

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestSchedulePersistence(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	st, err := session.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	m := compactCmdModel()
	m.store = st

	m.agent.Messages = append(m.agent.Messages, ai.Message{Role: "user", Content: "q", Authored: true})
	m.persist()
	if m.sessionID == "" {
		t.Fatal("persist should create the session")
	}

	m.scheduleCommand([]string{"@every", "10m", "check the deploy status"})
	m.scheduleCommand([]string{"@at", time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "one-shot reminder"})

	tasks := st.Schedules(m.sessionID)
	if len(tasks) != 2 {
		t.Fatalf("two tasks stored, got %d", len(tasks))
	}
	if tasks[0].Schedule != "@every 10m0s" || tasks[0].Prompt != "check the deploy status" {
		t.Fatalf("task 1: %+v", tasks[0])
	}
	if !strings.HasPrefix(tasks[1].Schedule, "@at ") {
		t.Fatalf("task 2: %+v", tasks[1])
	}

	m.scheduleCommand([]string{"cancel", "1"})
	if tasks := st.Schedules(m.sessionID); len(tasks) != 1 {
		t.Fatalf("after cancel: %d tasks", len(tasks))
	}
}

func TestScheduleFiresWakeup(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	st, err := session.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m.store = st
	m.sessionID, _ = st.Create("/tmp", m.modelName, m.provName)
	m.agent.SetSessionID(m.sessionID)

	if _, err := st.AddSchedule(m.sessionID, "@every 10m", "say hi", time.Now().Add(-2*time.Minute).Truncate(time.Second)); err != nil {
		t.Fatal(err)
	}

	if cmd := m.fireDueSchedules(); cmd == nil {
		t.Fatal("a due task should fire")
	}

	if !m.busy {
		t.Fatal("the fired task should have started a turn")
	}
	var wakeup ai.Message
	for range 200 {
		for _, v := range slices.Backward(m.agent.MessagesSnapshot()) {
			if v.Role == "user" {
				wakeup = v
				break
			}
		}
		if wakeup.Role != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(wakeup.Content, "Scheduled task") {
		t.Fatalf("wakeup should be a machine-authored user turn, got %+v", wakeup)
	}
	var sawMarker bool
	for _, b := range m.blocks {
		if strings.Contains(ansi.Strip(b.render(m.width)), "⏰ scheduled task") {
			sawMarker = true
		}
	}
	if !sawMarker {
		t.Fatal("transcript should show the ⏰ fire marker")
	}

	tasks := st.Schedules(m.sessionID)
	if tasks[0].LastFire.IsZero() {
		t.Fatal("fire should stamp last_fire")
	}
	m.busy = false
	if cmd := m.fireDueSchedules(); cmd != nil {
		t.Fatal("a just-fired task is not due again")
	}
}

func TestScheduleOneShotCompletes(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	st, _ := session.Open(filepath.Join(t.TempDir(), "s.db"))
	defer st.Close()
	m.store = st
	m.sessionID, _ = st.Create("/tmp", m.modelName, m.provName)

	past := time.Now().Add(-time.Minute).UTC()
	if _, err := st.AddSchedule(m.sessionID, "@at "+past.Format(time.RFC3339), "remind", past); err != nil {
		t.Fatal(err)
	}
	if cmd := m.fireDueSchedules(); cmd == nil {
		t.Fatal("a past @at should fire once")
	}
	m.busy = false
	if cmd := m.fireDueSchedules(); cmd != nil {
		t.Fatal("a fired one-shot is done")
	}

	m.blocks = nil
	m.scheduleCommand(nil)
	var listed bool
	for _, b := range m.blocks {
		if strings.Contains(ansi.Strip(b.render(m.width)), "(fired)") {
			listed = true
		}
	}
	if !listed {
		t.Fatal("a fired one-shot should stay listed as (fired)")
	}
}

func TestScheduleDefersWhileBusy(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	m := compactCmdModel()
	st, _ := session.Open(filepath.Join(t.TempDir(), "s.db"))
	defer st.Close()
	m.store = st
	m.sessionID, _ = st.Create("/tmp", m.modelName, m.provName)
	st.AddSchedule(m.sessionID, "@every 10m", "ping", time.Now().Add(-2*time.Minute).Truncate(time.Second))

	m.busy = true
	if cmd := m.fireDueSchedules(); cmd != nil {
		t.Fatal("a busy agent defers the fire")
	}
	if tasks := st.Schedules(m.sessionID); !tasks[0].LastFire.IsZero() {
		t.Fatal("a deferred fire is not stamped")
	}
}
