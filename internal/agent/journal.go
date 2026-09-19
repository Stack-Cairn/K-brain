package agent

import (
	"strings"
	"unicode/utf8"
)

type JournaledEvent struct {
	Kind  int
	S, S2 string
}

type taskJournal struct {
	events    []JournaledEvent
	bytes     int
	Truncated bool
}

const (
	journalBudget    = 128 * 1024
	journalMaxEvents = 1024
)

func (j *taskJournal) append(kind int, s, s2 string) {
	if kind == 0 && s2 == "" && len(j.events) > 0 && j.events[len(j.events)-1].Kind == 0 && j.events[len(j.events)-1].S2 == "" {
		j.events[len(j.events)-1].S += s
		j.bytes += len(s)
	} else {
		j.events = append(j.events, JournaledEvent{Kind: kind, S: s, S2: s2})
		j.bytes += len(s) + len(s2)
	}
	for (j.bytes > journalBudget || len(j.events) > journalMaxEvents) && len(j.events) > 1 {
		j.bytes -= len(j.events[0].S) + len(j.events[0].S2)
		j.events[0] = JournaledEvent{}
		j.events = j.events[1:]
		j.Truncated = true
	}
	if j.bytes > journalBudget {
		event := &j.events[0]
		keepS2 := min(len(event.S2), journalBudget-min(len(event.S), journalBudget/4))
		event.S = journalTail(event.S, journalBudget-keepS2)
		event.S2 = journalTail(event.S2, keepS2)
		j.bytes = len(event.S) + len(event.S2)
		j.Truncated = true
	}
}

func journalTail(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	start := len(s) - limit
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return strings.Clone(s[start:])
}
