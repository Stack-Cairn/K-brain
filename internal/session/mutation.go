package session

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

var ErrRevision = errors.New("history revision conflict")
var ErrAnchor = errors.New("user message anchor not found")

type HistorySnapshot struct {
	Meta           Meta
	CreatedAt      time.Time
	Messages       []ai.Message
	Offsets        []int
	ActiveMessages []ai.Message
	Revision       string
}

func canonicalList(messages []ai.Message, sessionID string) []ai.Message {
	out := make([]ai.Message, len(messages))
	copy(out, messages)
	for i := range out {
		if out[i].ID == "" {
			b, _ := json.Marshal(out[i])
			sum := sha256.Sum256(append([]byte(fmt.Sprintf("%s:%d:", sessionID, i)), b...))
			out[i].ID = fmt.Sprintf("msg-%x", sum[:16])
		}
	}
	return out
}

func canonicalMessages(d *sessionData) []ai.Message {
	keys := sortedKeys(d.Messages)
	out := make([]ai.Message, 0, len(keys))
	for _, seq := range keys {
		out = append(out, d.canonicalMessage(seq, d.Messages[seq]))
	}
	return out
}

// canonicalMessage computes identity before display-only provenance enrichment.
// Forks persist this projection because their task records are not copied.
func (d *sessionData) canonicalMessage(seq int, msg ai.Message) ai.Message {
	id := canonicalAt(d.Meta.ID, seq, msg)
	msg = d.withLegacyNoticeOrigin(msg)
	msg.ID = id
	return msg
}

// withLegacyNoticeOrigin enriches a display copy only. Old subagent reports had
// neither identity nor authorship; require an exact persisted task match rather
// than treating arbitrary user text with a familiar prefix as a runtime notice.
func (d *sessionData) withLegacyNoticeOrigin(msg ai.Message) ai.Message {
	if msg.ID != "" || msg.Role != "user" || msg.Authored || msg.Origin != "" || len(msg.Parts) != 0 {
		return msg
	}
	for _, task := range d.Tasks {
		if task.ID == "" || (task.Status != "done" && task.Status != "error" && task.Status != "cancelled") {
			continue
		}
		if msg.Content == fmt.Sprintf("[subagent %s %s] %s\n\n%s", task.ID, task.Status, task.Description, legacyNoticeReport(task.Report)) {
			msg.Origin = ai.OriginBackground
			break
		}
	}
	return msg
}

// Keep the legacy wire format fixed even if future runtime report limits change.
// Old notices used capReport while the task record retained the full report.
func legacyNoticeReport(report string) string {
	const limit = 50_000
	if len(report) <= limit {
		return report
	}
	end := limit
	for end > 0 && !utf8.RuneStart(report[end]) {
		end--
	}
	return report[:end] + fmt.Sprintf("\n\n... [report truncated — %d bytes total; the investigation covered more than fits here]", len(report))
}

func canonicalAt(sessionID string, seq int, msg ai.Message) string {
	if msg.ID != "" {
		return msg.ID
	}
	b, _ := json.Marshal(msg)
	sum := sha256.Sum256(append([]byte(fmt.Sprintf("%s:%d:", sessionID, seq)), b...))
	return fmt.Sprintf("msg-%x", sum[:16])
}

func (d *sessionData) ensureMessageIDs() {
	seen := make(map[string]bool)
	for _, seq := range sortedKeys(d.Messages) {
		msg := d.Messages[seq]
		if msg.ID == "" || seen[msg.ID] {
			msg.ID = ""
			b, _ := json.Marshal(msg)
			sum := sha256.Sum256(append([]byte(fmt.Sprintf("%s:%d:", d.Meta.ID, seq)), b...))
			msg.ID = fmt.Sprintf("msg-%x", sum[:16])
			d.Messages[seq] = msg
		}
		seen[msg.ID] = true
	}
}

func (d *sessionData) revision() string {
	b, _ := json.Marshal(struct {
		Messages    map[int]ai.Message
		Compactions map[int]Compaction
	}{d.Messages, d.Compactions})
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:])
}

func (s *Store) HistorySnapshot(id string) (out HistorySnapshot, err error) {
	err = s.withLock(func() error {
		d, e := s.read(id)
		if e != nil {
			return e
		}
		messages := canonicalMessages(d)
		offsets := sortedKeys(d.Messages)
		rawActive := d.contextMessages()
		active := canonicalList(rawActive, d.Meta.ID)
		for i, msg := range rawActive {
			active[i].Origin = d.withLegacyNoticeOrigin(msg).Origin
		}
		out = HistorySnapshot{Meta: d.Meta, CreatedAt: d.CreatedAt, Messages: messages, Offsets: offsets, ActiveMessages: active, Revision: d.revision()}
		return nil
	})
	return
}

// MutateHistory checks the revision and anchor under the transcript lock.
// A nil replacement forks through the anchor's reply; otherwise it replaces the raw suffix.
func (s *Store) MutateHistory(id, messageID, expectedRevision, title string, replacement *ai.Message) (resultID string, err error) {
	err = s.withLock(func() error {
		d, e := s.read(id)
		if e != nil {
			return e
		}
		if expectedRevision != "" && expectedRevision != d.revision() {
			return ErrRevision
		}
		if replacement != nil {
			if err := s.guardExternalRunLocked(id); err != nil {
				return err
			}
		}
		keys := sortedKeys(d.Messages)
		anchor := -1
		for i, seq := range keys {
			msg := d.Messages[seq]
			canonical := msg.ID
			if canonical == "" {
				canonical = canonicalAt(d.Meta.ID, seq, msg)
			}
			if (msg.ID == messageID || canonical == messageID) && msg.Role == "user" {
				anchor = i
				break
			}
		}
		if anchor < 0 {
			return ErrAnchor
		}
		if replacement == nil {
			end := anchor + 1
			for end < len(keys) && d.Messages[keys[end]].Role != "user" {
				end++
			}
			m := d.Meta
			if title == "" {
				title = m.Title
			}
			branch := newData(Meta{CWD: m.CWD, Model: m.Model, Provider: m.Provider, Title: title, ForkedFrom: id, ForkSeq: keys[end-1], UpdatedAt: time.Now().UTC()})
			for _, seq := range keys[:end] {
				// Preserve source identity and recovered provenance without task records.
				branch.Messages[seq] = d.canonicalMessage(seq, d.Messages[seq])
			}
			resultID, e = s.create(branch)
			return e
		}
		if replacement.Role != "user" {
			return errors.New("replacement must be a user message")
		}
		from := keys[anchor]
		for _, seq := range keys[anchor:] {
			delete(d.Messages, seq)
		}
		for seq := range d.Snapshots {
			if seq >= from {
				delete(d.Snapshots, seq)
			}
		}
		// Old summaries and task reports may contain the discarded suffix.
		clear(d.Compactions)
		clear(d.Tasks)
		msg := *replacement
		msg.Authored = true
		now := time.Now().UTC()
		msg.SentAt = &now
		msg.ID = canonicalAt(d.Meta.ID, from, msg)
		d.Messages[from] = msg
		d.Meta.UpdatedAt = now
		resultID = id
		return s.write(d)
	})
	return
}
