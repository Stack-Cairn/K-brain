package tui

import (
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	turnStreamInterval  = 40 * time.Millisecond
	turnStreamMaxBytes  = 64 * 1024
	turnStreamMaxChunks = 256
)

type turnStreamChunk struct {
	thinking bool
	data     []byte
}

type turnStream struct {
	mu         sync.Mutex
	deliver    func(tea.Msg)
	pending    []turnStreamChunk
	bytes      int
	timer      *time.Timer
	generation uint64
	closed     bool
}

func newTurnStream(deliver func(tea.Msg)) *turnStream {
	return &turnStream{deliver: deliver}
}

func (s *turnStream) text(delta string)  { s.push(false, delta) }
func (s *turnStream) think(delta string) { s.push(true, delta) }

func (s *turnStream) push(thinking bool, delta string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || delta == "" {
		return
	}
	if len(delta) >= turnStreamMaxBytes {
		s.flushLocked()
		s.deliverDelta(thinking, delta)
		return
	}
	if s.bytes+len(delta) > turnStreamMaxBytes || len(s.pending) >= turnStreamMaxChunks {
		s.flushLocked()
	}
	n := len(s.pending)
	if n > 0 && s.pending[n-1].thinking == thinking {
		s.pending[n-1].data = append(s.pending[n-1].data, delta...)
	} else {
		s.pending = append(s.pending, turnStreamChunk{thinking: thinking, data: []byte(delta)})
	}
	s.bytes += len(delta)
	if s.timer == nil {
		generation := s.generation
		s.timer = time.AfterFunc(turnStreamInterval, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.closed && s.generation == generation {
				s.flushLocked()
			}
		})
	}
}

func (s *turnStream) deliverDelta(thinking bool, delta string) {
	if thinking {
		s.deliver(thinkMsg(delta))
	} else {
		s.deliver(textMsg(delta))
	}
}

func (s *turnStream) flushLocked() {
	s.generation++
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	for _, chunk := range s.pending {
		s.deliverDelta(chunk.thinking, string(chunk.data))
	}
	s.pending, s.bytes = nil, 0
}

func (s *turnStream) emit(msg tea.Msg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.flushLocked()
	s.deliver(msg)
}

func (s *turnStream) finish(msg turnDoneMsg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.flushLocked()
	s.deliver(msg)
}
