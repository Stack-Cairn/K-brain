package tui

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTurnStreamPreservesOrderAcrossBoundaries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var got []tea.Msg
		s := newTurnStream(func(msg tea.Msg) { got = append(got, msg) })
		s.text("before ")
		s.text("thinking")
		s.think("reason ")
		s.think("one")
		s.text("after thinking")
		preview := toolCallMsg{id: "t", name: "read", args: "{}"}
		s.emit(preview)
		s.think("reason two")
		start := toolStartMsg{id: "t", name: "read", args: "{}"}
		s.emit(start)
		s.emit(toolOutputMsg{id: "t", text: "output"})
		end := toolEndMsg{id: "t", name: "read", result: "done"}
		s.emit(end)
		s.text("final")
		done := turnDoneMsg{final: "final"}
		s.finish(done)
		want := []tea.Msg{textMsg("before thinking"), thinkMsg("reason one"), textMsg("after thinking"), preview, thinkMsg("reason two"), start, toolOutputMsg{id: "t", text: "output"}, end, textMsg("final"), done}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("events: got %#v want %#v", got, want)
		}
		s.text("late")
		s.think("late")
		s.emit(toolCallMsg{id: "late"})
		s.finish(turnDoneMsg{final: "duplicate"})
		time.Sleep(2 * turnStreamInterval)
		synctest.Wait()
		if !reflect.DeepEqual(got, want) {
			t.Fatal("closed stream emitted late events")
		}
	})
}

func TestTurnStreamTimerAndExplicitFlush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var got []tea.Msg
		s := newTurnStream(func(msg tea.Msg) { got = append(got, msg) })
		s.text("first")
		time.Sleep(turnStreamInterval - time.Millisecond)
		synctest.Wait()
		if len(got) != 0 {
			t.Fatal("text was emitted before the batch interval")
		}
		s.think("thought")
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if want := []tea.Msg{textMsg("first"), thinkMsg("thought")}; !reflect.DeepEqual(got, want) {
			t.Fatalf("timer did not deliver pending content: %#v", got)
		}
		s.text("second")
		s.emit(steeredMsg("guidance"))
		s.text("third")
		time.Sleep(turnStreamInterval)
		synctest.Wait()
		s.finish(turnDoneMsg{})
		want := []tea.Msg{textMsg("first"), thinkMsg("thought"), textMsg("second"), steeredMsg("guidance"), textMsg("third"), turnDoneMsg{}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("timer/explicit flush order: %#v", got)
		}
	})
}

func TestTurnStreamFinishWaitsForInFlightDelivery(t *testing.T) {
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var got []tea.Msg
	s := newTurnStream(func(msg tea.Msg) {
		if text, ok := msg.(textMsg); ok && text == "buffered" {
			close(entered)
			<-release
		}
		got = append(got, msg)
	})
	s.text("buffered")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("timer did not start delivery")
	}
	go func() {
		s.finish(turnDoneMsg{final: "complete"})
		close(finished)
	}()
	select {
	case <-finished:
		t.Error("turn finished before in-flight text was delivered")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not finish after delivery resumed")
	}
	if want := []tea.Msg{textMsg("buffered"), turnDoneMsg{final: "complete"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("completion overtook text: %#v", got)
	}
}

func TestTurnStreamBoundsPendingContentWithoutDropping(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var text, thinking strings.Builder
		s := newTurnStream(func(msg tea.Msg) {
			switch msg := msg.(type) {
			case textMsg:
				text.WriteString(string(msg))
			case thinkMsg:
				thinking.WriteString(string(msg))
			}
		})
		var wantText, wantThink strings.Builder
		for i := range turnStreamMaxChunks * 3 {
			delta := strings.Repeat("文字🙂", 200)
			s.text(delta)
			wantText.WriteString(delta)
			s.think("思")
			wantThink.WriteString("思")
			if s.bytes > turnStreamMaxBytes || len(s.pending) > turnStreamMaxChunks {
				t.Fatalf("unbounded content: %d bytes, %d chunks", s.bytes, len(s.pending))
			}
			if i%100 == 0 {
				s.text("")
			}
		}
		for range turnStreamMaxChunks * 2 {
			s.text("x")
			s.think("y")
			wantText.WriteString("x")
			wantThink.WriteString("y")
			if len(s.pending) > turnStreamMaxChunks {
				t.Fatal("small deltas exceeded the chunk limit")
			}
		}
		large := strings.Repeat("一", turnStreamMaxBytes)
		s.text(large)
		wantText.WriteString(large)
		s.finish(turnDoneMsg{})
		if text.String() != wantText.String() || thinking.String() != wantThink.String() {
			t.Fatal("buffer limit dropped, duplicated, or corrupted content")
		}
	})
}

func TestTurnStreamConcurrentProducers(t *testing.T) {
	var text strings.Builder
	var finished bool
	s := newTurnStream(func(msg tea.Msg) {
		if finished {
			t.Error("content delivered after completion")
		}
		switch msg := msg.(type) {
		case textMsg:
			text.WriteString(string(msg))
		case turnDoneMsg:
			finished = true
		}
	})
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Go(func() {
			for i := range 100 {
				s.text(fmt.Sprintf("%d:%d ", worker, i))
			}
		})
	}
	wg.Wait()
	s.finish(turnDoneMsg{})
	counts := make([]int, 8)
	for _, token := range strings.Fields(text.String()) {
		var worker, seq int
		if _, err := fmt.Sscanf(token, "%d:%d", &worker, &seq); err != nil {
			t.Fatal(err)
		}
		if worker < 0 || worker >= len(counts) || counts[worker] != seq {
			t.Fatalf("producer content reordered or lost: %q, counts %v", token, counts)
		}
		counts[worker]++
	}
	for worker, count := range counts {
		if count != 100 {
			t.Fatalf("worker %d delivered %d/100 chunks", worker, count)
		}
	}
}
