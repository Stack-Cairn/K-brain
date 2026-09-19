package tui

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTaskStreamPreservesOrderAndCompletion(t *testing.T) {
	var q taskStream
	for i := range 1000 {
		wake := q.push(taskEventMsg{kind: 0, s: fmt.Sprintf("%04d|", i)})
		if wake != (i == 0) {
			t.Fatalf("unexpected wake for chunk %d: %v", i, wake)
		}
	}
	if q.push(taskEventMsg{kind: 4}) {
		t.Fatal("completion scheduled a second outstanding wake")
	}
	events, dropped := q.drain()
	var got, want strings.Builder
	for i := range 1000 {
		fmt.Fprintf(&want, "%04d|", i)
	}
	for _, e := range events[:len(events)-1] {
		if e.kind != 0 {
			t.Fatalf("unexpected event before completion: %d", e.kind)
		}
		got.WriteString(e.s)
	}
	if dropped || got.String() != want.String() || events[len(events)-1].kind != 4 {
		t.Fatal("stream reordered chunks or completed before all text")
	}
	if !q.push(taskEventMsg{kind: 0, s: "next reply"}) {
		t.Fatal("draining did not allow scheduling the next wake")
	}
}

func TestTaskStreamBoundsPendingOutput(t *testing.T) {
	var q taskStream
	for range 2000 {
		q.push(taskEventMsg{kind: 2, s: "read", s2: strings.Repeat("输出", 100)})
	}
	if q.bytes > taskStreamBudget || len(q.pending) > taskStreamMaxEvents || !q.dropped {
		t.Fatalf("unbounded queue: bytes=%d events=%d dropped=%v", q.bytes, len(q.pending), q.dropped)
	}
	q.push(taskEventMsg{kind: 2, s: "read", s2: strings.Repeat("输出", taskStreamBudget)})
	events, dropped := q.drain()
	var size int
	for _, event := range events {
		size += len(event.s) + len(event.s2)
		if !utf8.ValidString(event.s) || !utf8.ValidString(event.s2) {
			t.Fatal("truncation split a UTF-8 character")
		}
	}
	if size > taskStreamBudget || !dropped || len(events) != 1 || events[0].s != "read" || events[0].s2 == "" {
		t.Fatalf("oversized result handling: bytes=%d dropped=%v events=%v", size, dropped, len(events))
	}
	q.push(taskEventMsg{kind: 0, s: strings.Repeat("x", taskStreamBudget)})
	q.push(taskEventMsg{kind: 4, s: "finished"})
	events, dropped = q.drain()
	if !dropped || events[len(events)-1].kind != 4 {
		t.Fatal("completion was lost when pending output exceeded the budget")
	}
	for range taskStreamMaxEvents + 10 {
		q.push(taskEventMsg{kind: 1})
	}
	if len(q.pending) != taskStreamMaxEvents || !q.dropped {
		t.Fatal("empty events exceeded the event budget")
	}
}

func TestTaskStreamConcurrentProducersAndClose(t *testing.T) {
	var q taskStream
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Go(func() {
			for i := range 100 {
				q.push(taskEventMsg{kind: 0, s: fmt.Sprintf("%d:%d ", worker, i)})
			}
		})
	}
	wg.Wait()
	events, dropped := q.drain()
	if dropped {
		t.Fatal("small stream was truncated")
	}
	var text strings.Builder
	for _, event := range events {
		text.WriteString(event.s)
	}
	counts := make([]int, 8)
	for _, value := range strings.Fields(text.String()) {
		parts := strings.Split(value, ":")
		worker, _ := strconv.Atoi(parts[0])
		index, _ := strconv.Atoi(parts[1])
		if index != counts[worker] {
			t.Fatalf("worker %d reordered index %d, want %d", worker, index, counts[worker])
		}
		counts[worker]++
	}
	for _, count := range counts {
		if count != 100 {
			t.Fatalf("lost events: counts=%v", counts)
		}
	}
	q.push(taskEventMsg{kind: 0, s: "queued"})
	q.close()
	if q.push(taskEventMsg{kind: 0, s: "late"}) {
		t.Fatal("closed view scheduled a wake")
	}
	if events, dropped := q.drain(); len(events) != 0 || dropped || q.bytes != 0 {
		t.Fatal("closed view retained queued output")
	}
}

type taskStreamHarness struct {
	m *model
}

func (h *taskStreamHarness) Init() tea.Cmd { return nil }
func (h *taskStreamHarness) View() string  { return "" }
func (h *taskStreamHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(taskStreamReadyMsg); ok {
		h.m.Update(msg)
		if !h.m.taskVP.busy {
			return h, tea.Quit
		}
	}
	return h, nil
}

func TestTaskStreamProgramDeliversOrderedBurst(t *testing.T) {
	m := tasksModel("http://unused")
	tv := &taskView{id: "task-1", busy: true}
	m.taskVP = tv
	harness := &taskStreamHarness{m: m}
	p := tea.NewProgram(harness, tea.WithInput(nil), tea.WithoutRenderer(), tea.WithoutSignalHandler())
	defer p.Kill()
	var want strings.Builder
	sent := make(chan struct{})
	go func() {
		for i := range 2000 {
			chunk := fmt.Sprintf("%04d|", i)
			fmt.Fprint(&want, chunk)
			sendTaskMsg(p, taskEventMsg{view: tv, id: tv.id, kind: 0, s: chunk})
		}
		sendTaskMsg(p, taskEventMsg{view: tv, id: tv.id, kind: 4})
		close(sent)
	}()
	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("sender blocked before the program started")
	}
	done := make(chan error, 1)
	go func() {
		_, err := p.Run()
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("program never received the completion event")
	}
	if tv.busy || tv.buf.String() != want.String()+"\n" {
		t.Fatalf("program received reordered/incomplete output: length=%d want=%d busy=%v", tv.buf.Len(), want.Len()+1, tv.busy)
	}
}

func TestTaskStreamUpdateReportsTruncationAndIgnoresClosedView(t *testing.T) {
	m := tasksModel("http://unused")
	tv := &taskView{id: "task-1", busy: true}
	m.taskVP = tv
	tv.stream.push(taskEventMsg{view: tv, id: tv.id, kind: 0, s: strings.Repeat("x", taskStreamBudget+1)})
	tv.stream.push(taskEventMsg{view: tv, id: tv.id, kind: 4})
	m.Update(taskStreamReadyMsg{view: tv})
	if tv.busy || !strings.Contains(tv.buf.String(), "earlier live output dropped") {
		t.Fatal("truncation or completion was not reflected in the view")
	}
	tv.stream.push(taskEventMsg{view: tv, id: tv.id, kind: 0, s: "late"})
	m.closeTaskView()
	m.taskVP = &taskView{id: tv.id}
	m.Update(taskStreamReadyMsg{view: tv})
	if m.taskVP.buf.Len() != 0 {
		t.Fatal("closed view batch leaked into the replacement")
	}
}
