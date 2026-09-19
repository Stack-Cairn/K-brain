package session

import (
	"fmt"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func saveMessages(d *sessionData, from int, msgs []ai.Message, model, provider string) error {
	if from < 0 {
		return fmt.Errorf("invalid message offset %d", from)
	}
	for i := from; i < len(msgs); i++ {
		if msgs[i].Role != "" {
			d.Messages[i] = msgs[i]
		}
	}
	d.Meta.Model, d.Meta.Provider, d.Meta.UpdatedAt = model, provider, time.Now().UTC()
	if d.Meta.Title == "" {
		for _, m := range msgs {
			if m.Role == "user" {
				d.Meta.Title = truncate(strings.Join(strings.Fields(m.TextContent()), " "), 64)
				break
			}
		}
	}
	return nil
}

func (s *Store) Save(id string, from int, msgs []ai.Message, model, provider string) error {
	return s.update(id, func(d *sessionData) error { return saveMessages(d, from, msgs, model, provider) })
}

func (s *Store) Load(idOrPrefix string) (meta Meta, msgs []ai.Message, err error) {
	err = s.withLock(func() error {
		ids, err := s.ids()
		if err != nil {
			return err
		}
		var matches []string
		for _, id := range ids {
			if strings.HasPrefix(id, idOrPrefix) {
				matches = append(matches, id)
			}
		}
		if len(matches) == 0 {
			return fmt.Errorf("no session matching %q: %w", idOrPrefix, ErrNotFound)
		}
		if len(matches) != 1 {
			return fmt.Errorf("session id %q is ambiguous", idOrPrefix)
		}
		d, err := s.read(matches[0])
		if err != nil {
			return err
		}
		meta, msgs = d.Meta, d.contextMessages()
		return nil
	})
	return
}

func (d *sessionData) rawMessages() []ai.Message {
	var msgs []ai.Message
	for _, seq := range sortedKeys(d.Messages) {
		msgs = append(msgs, d.Messages[seq])
	}
	return msgs
}

func (d *sessionData) contextMessages() []ai.Message {
	msgs, _ := d.contextEntries()
	return msgs
}

func (s *Store) RawMessages(id string) []ai.Message {
	d, err := s.get(id)
	if err != nil {
		return nil
	}
	return d.rawMessages()
}

func (s *Store) LastExchange(id string) (user, assistant string) {
	for _, m := range s.RawMessages(id) {
		switch m.Role {
		case "user":
			user = m.TextContent()
		case "assistant":
			assistant = m.TextContent()
		}
	}
	return
}

func (s *Store) ClearMessages(id string) error {
	return s.update(id, func(d *sessionData) error { clear(d.Messages); return nil })
}

func (s *Store) DeleteFrom(id string, from int) error {
	return s.update(id, func(d *sessionData) error {
		for seq := range d.Messages {
			if seq >= from {
				delete(d.Messages, seq)
			}
		}
		for seq := range d.Snapshots {
			if seq >= from {
				delete(d.Snapshots, seq)
			}
		}
		return nil
	})
}

func answerDanglingToolCalls(msgs []ai.Message, refs []int) ([]ai.Message, []int) {
	answered := make(map[string]bool, len(msgs))
	dangling := false
	for _, m := range msgs {
		if m.Role == "tool" {
			answered[m.ToolCallID] = true
		}
	}
	for _, m := range msgs {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				dangling = dangling || !answered[tc.ID]
			}
		}
	}
	if !dangling {
		return msgs, refs
	}
	out := make([]ai.Message, 0, len(msgs)+4)
	mapped := make([]int, 0, len(refs)+4)
	for i, m := range msgs {
		out = append(out, m)
		mapped = append(mapped, refs[i])
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			if !answered[tc.ID] {
				out = append(out, ai.Message{
					Role:       "tool",
					Content:    "Error: tool call interrupted — the session ended before a result was recorded",
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
				})
				mapped = append(mapped, -1)
			}
		}
	}
	return out, mapped
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) > n {
		return string(runes[:n-1]) + "…"
	}
	return s
}
