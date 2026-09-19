package tui

import (
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	taskStreamBudget    = 128 * 1024
	taskStreamMaxEvents = 1024
	taskStreamChunkSize = 4096
)

type taskStreamReadyMsg struct {
	view *taskView
}

type taskStream struct {
	mu        sync.Mutex
	pending   []taskEventMsg
	bytes     int
	scheduled bool
	dropped   bool
	closed    bool
}

func (q *taskStream) push(msg taskEventMsg) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	if len(msg.s)+len(msg.s2) > taskStreamBudget {
		keepS2 := min(len(msg.s2), taskStreamBudget-min(len(msg.s), taskStreamBudget/4))
		msg.s = taskStreamTail(msg.s, taskStreamBudget-keepS2)
		msg.s2 = taskStreamTail(msg.s2, keepS2)
		q.dropped = true
	}
	n := len(q.pending)
	if msg.kind == 0 && msg.s2 == "" && n > 0 && q.pending[n-1].kind == 0 && q.pending[n-1].s2 == "" && len(q.pending[n-1].s)+len(msg.s) <= taskStreamChunkSize {
		q.pending[n-1].s += msg.s
	} else {
		q.pending = append(q.pending, msg)
	}
	q.bytes += len(msg.s) + len(msg.s2)
	for q.bytes > taskStreamBudget || len(q.pending) > taskStreamMaxEvents {
		q.bytes -= len(q.pending[0].s) + len(q.pending[0].s2)
		q.pending[0] = taskEventMsg{}
		q.pending = q.pending[1:]
		q.dropped = true
	}
	if q.scheduled {
		return false
	}
	q.scheduled = true
	return true
}

func (q *taskStream) drain() ([]taskEventMsg, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	events, dropped := q.pending, q.dropped
	q.pending, q.bytes, q.dropped, q.scheduled = nil, 0, false, false
	return events, dropped
}

func (q *taskStream) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.pending, q.bytes, q.dropped = nil, 0, false
}

func taskStreamTail(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	start := len(s) - limit
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return strings.Clone(s[start:])
}

func (m *model) applyTaskEvent(msg taskEventMsg) bool {
	tv := m.taskVP
	if tv == nil || msg.id != tv.id || (msg.view != nil && msg.view != tv) {
		return false
	}
	if msg.kind == 4 {
		tv.busy, tv.followCancel = false, nil
	}
	renderTaskEvent(&tv.buf, msg.kind, msg.s, msg.s2)
	return true
}
