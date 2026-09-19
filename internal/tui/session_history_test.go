package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func compactHistoryForTest(m *model, cut int, summary string) compactMsg {
	before := m.agent.MessagesSnapshot()
	after := append([]ai.Message{before[0], {Role: "system", Content: "Summary of the conversation so far:\n\n" + summary}}, before[cut:]...)
	m.agent.Messages = after
	return compactMsg{before: before, after: after, summary: summary, cutoff: cut, info: agent.CompactInfo{Model: "m"}}
}

func TestTUICompactionDelayedEventsAndNavigation(t *testing.T) {
	for _, storedSystem := range []bool{false, true} {
		t.Run(fmt.Sprint(storedSystem), func(t *testing.T) {
			m := forkModel(t)
			if storedSystem {
				if err := m.store.Save(m.sessionID, 0, m.agent.Messages, "m", "p"); err != nil {
					t.Fatal(err)
				}
			}
			m.agent.Messages = append(m.agent.Messages, ai.Message{Role: "user", Content: "q3", Authored: true}, ai.Message{Role: "assistant", Content: "a3"})
			original := m.agent.MessagesSnapshot()
			first := compactHistoryForTest(m, 3, "first")
			second := compactHistoryForTest(m, 4, "second")
			m.agent.Messages = append(m.agent.Messages, ai.Message{Role: "user", Content: "q4", Authored: true}, ai.Message{Role: "assistant", Content: "a4"})
			final := m.agent.MessagesSnapshot()
			m.busy = true
			m.Update(first)
			m.Update(second)
			m.busy = false
			if !m.persist() {
				t.Fatal(tailBlock(m))
			}
			if len(m.store.Compactions(m.sessionID)) != 2 {
				t.Fatal("lost compactions")
			}
			_, loaded, err := m.store.History(m.sessionID, original[:1])
			if err != nil || !reflect.DeepEqual(loaded, final) {
				t.Fatalf("restored: %+v, %v", loaded, err)
			}
			raw := m.store.RawMessages(m.sessionID)
			wantRaw := append(append([]ai.Message(nil), original[1:]...), final[len(final)-2:]...)
			if storedSystem {
				wantRaw = append(original[:1:1], wantRaw...)
			}
			if !reflect.DeepEqual(raw, wantRaw) {
				t.Fatalf("raw history overwritten: %+v", raw)
			}
			id := m.sessionID
			m.fork(2, "compacted branch")
			if m.sessionID == id || !reflect.DeepEqual(m.agent.Messages, final[:3]) {
				t.Fatalf("fork context: %+v", m.agent.Messages)
			}
			if m.agent.SessionIDValue() != m.sessionID || m.sessTitle != "compacted branch" {
				t.Fatal("fork identity/title not updated")
			}
			if err := m.resume(id); err != nil {
				t.Fatal(err)
			}
			m.compactRetry()
			if len(m.store.Compactions(id)) != 1 || m.agent.Messages[2].Content != "q2" {
				t.Fatalf("undo second: %+v", m.agent.Messages)
			}
			m.compactRetry()
			if m.agent.Messages[1].Content != "q1" || len(m.store.Compactions(id)) != 0 {
				t.Fatalf("undo first: %+v", m.agent.Messages)
			}
			if !reflect.DeepEqual(m.store.RawMessages(id), wantRaw) {
				t.Fatal("undo changed raw records")
			}
		})
	}
}

func TestTUICompactionSaveRetry(t *testing.T) {
	m := forkModel(t)
	if !m.persist() {
		t.Fatal(tailBlock(m))
	}
	path := m.store.TranscriptPath(m.sessionID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	first := compactHistoryForTest(m, 3, "first")
	m.Update(first)
	if strings.Contains(tailBlock(m), "raw history preserved") {
		t.Fatal("failed save reported success")
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if !m.persist() {
			t.Fatal(tailBlock(m))
		}
	}
	if len(m.store.Compactions(m.sessionID)) != 1 || len(m.store.RawMessages(m.sessionID)) != 4 {
		t.Fatal("retry duplicated summary or lost history")
	}
}

func TestTUICompactedRewindAndSnapshots(t *testing.T) {
	m := forkModel(t)
	m.agent.Messages = append(m.agent.Messages, ai.Message{Role: "user", Content: "q3", Authored: true}, ai.Message{Role: "assistant", Content: "a3"})
	event := compactHistoryForTest(m, 3, "summary")
	at := 5
	event.turnAt = &at
	m.Update(event)
	m.recordTurnSnapshot(turnDoneMsg{at: 4, snap: "snapshot-for-q3"})
	if m.snapshots[5] != "snapshot-for-q3" || m.store.Snapshots(m.sessionID)[5] != "snapshot-for-q3" {
		t.Fatal("snapshot used compacted index instead of original sequence")
	}
	delete(m.snapshots, 5)
	if err := m.store.SetSnapshot(m.sessionID, 5, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.applyRewind(4); !ok {
		t.Fatal(tailBlock(m))
	}
	_, loaded, err := m.store.History(m.sessionID, m.agent.Messages[:1])
	if err != nil || !reflect.DeepEqual(loaded, m.agent.Messages) {
		t.Fatalf("rewind did not persist: %+v, %v", loaded, err)
	}
	if len(m.store.RawMessages(m.sessionID)) != 4 {
		t.Fatal("rewind deleted wrong raw messages")
	}
	if _, ok := m.applyRewind(6); !ok {
		t.Fatal(tailBlock(m))
	}
	if len(m.store.RawMessages(m.sessionID)) != 6 {
		t.Fatal("redo did not restore raw messages")
	}
	m.command("/new")
	if m.history != nil || len(m.snapshots) > 0 || m.turnSnapshotSeq != nil {
		t.Fatal("new session retained history state")
	}
}

type historyTurnStart struct{}
type historyHarness struct {
	*model
	compacted int
}

func (h *historyHarness) Init() tea.Cmd { return func() tea.Msg { return historyTurnStart{} } }
func (h *historyHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(historyTurnStart); ok {
		_, cmd := h.model.submitTurn("next question", true)
		return h, cmd
	}
	if _, ok := msg.(compactMsg); ok {
		h.compacted++
	}
	_, cmd := h.model.Update(msg)
	if _, ok := msg.(turnDoneMsg); ok {
		return h, tea.Quit
	}
	return h, cmd
}

func TestTUIRealTurnCompactionPersists(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		t.Run(fmt.Sprint(fresh), func(t *testing.T) {
			t.Chdir(t.TempDir())
			cacheKeys := make(chan string, 16)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req ai.Request
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if !req.Stream {
					fmt.Fprint(w, `{"choices":[{"message":{"content":"summary from provider"}}]}`)
					return
				}
				cacheKeys <- req.PromptCacheKey
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"finished\"}}]}\n\ndata: [DONE]\n\n")
			}))
			defer srv.Close()
			m := forkModel(t)
			if fresh {
				m.sessionID = ""
			}
			msgs := m.agent.Messages
			m.agent = agent.New(ai.New(srv.URL, "key"), "m", 100, "sys")
			m.agent.Messages = msgs
			for i := range 8 {
				m.agent.Messages = append(m.agent.Messages, ai.Message{Role: "user", Content: fmt.Sprint("question ", i)}, ai.Message{Role: "assistant", Content: strings.Repeat("answer ", 20)})
			}
			m.agent.ContextLimit, m.agent.CompactThreshold = 400, 0.1
			m.titled = true
			before := len(m.agent.Messages) - 1
			h := &historyHarness{model: m}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			p := tea.NewProgram(h, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler())
			m.prog = p
			if _, err := p.Run(); err != nil {
				t.Fatal(err)
			}
			if h.compacted == 0 {
				t.Fatal("real turn did not compact")
			}
			select {
			case key := <-cacheKeys:
				if key == "" || key != m.sessionID {
					t.Fatalf("cache key %q != session %q", key, m.sessionID)
				}
			default:
				t.Fatal("no stream request")
			}
			if got := len(m.store.RawMessages(m.sessionID)); got != before+2 {
				t.Fatalf("raw messages = %d, want %d", got, before+2)
			}
			_, loaded, err := m.store.History(m.sessionID, m.agent.Messages[:1])
			wantJSON, marshalErr := json.Marshal(m.agent.Messages)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			gotJSON, marshalErr := json.Marshal(loaded)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if err != nil || !bytes.Equal(wantJSON, gotJSON) {
				t.Fatalf("real turn restore: %+v, %v", loaded, err)
			}
		})
	}
}

func TestTUIRewindKeepsForkWorkspaceSnapshot(t *testing.T) {
	repo := rewindRepo(t)
	m := forkModel(t)
	snap := snapshotWorkspace()
	if snap == "" {
		t.Fatal("snapshot not created")
	}
	m.snapshots = map[int]string{1: snap}
	if err := m.store.SetSnapshot(m.sessionID, 1, snap); err != nil {
		t.Fatal(err)
	}
	source := m.sessionID
	m.fork(len(m.agent.Messages), "shared checkpoint")
	if m.sessionID == source {
		t.Fatal("fork failed")
	}
	writeRewindFile(t, repo, "a.txt", "changed\n")
	if _, ok := m.applyRewind(1); !ok {
		t.Fatal(tailBlock(m))
	}
	if got, err := os.ReadFile(repo + "/a.txt"); err != nil || string(got) != "base\n" {
		t.Fatalf("workspace: %q, %v", got, err)
	}
	if m.store.Snapshots(source)[1] != snap {
		t.Fatal("source lost snapshot")
	}
	rewindGit(t, "rev-parse", "--verify", "refs/k-brain/snapshots/"+snap)
}

func TestTUICompactionRetryWhileBusy(t *testing.T) {
	m := forkModel(t)
	m.Update(compactHistoryForTest(m, 3, "summary"))
	before := m.agent.MessagesSnapshot()
	m.busy = true
	m.command("/compact retry")
	if len(m.store.Compactions(m.sessionID)) != 1 || !reflect.DeepEqual(m.agent.Messages, before) {
		t.Fatal("busy retry mutated history")
	}
}

func TestTUIBusyForkWaitsForFirstSavedTurn(t *testing.T) {
	m := forkModel(t)
	m.sessionID = ""
	m.agent.Messages = m.agent.Messages[:1]
	m.prepareHistory()
	if m.sessionID == "" {
		t.Fatal("session should be allocated before the first request")
	}
	m.busy = true
	m.busyFork("too early")
	if m.pendingForkID != "" || !strings.Contains(tailBlock(m), "nothing to fork yet") {
		t.Fatal("created a fork without any saved messages")
	}
}
