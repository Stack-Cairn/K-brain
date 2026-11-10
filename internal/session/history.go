package session

import (
	"fmt"
	"slices"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

type History struct {
	prefix      int
	start       int
	refs        []int
	raw         []int
	next        int
	pending     map[int]ai.Message
	compactions []Compaction
	// revision 是这个 History 最后一次与磁盘同步（加载或保存）时的记录版本。
	// 另一个进程随后写入消息会让它过期，保存时据此拒绝覆盖。
	revision string
}

// HistoryStale 报告磁盘上会话 id 的消息是否已被别处修改，与 h 最后一次加载或保存时不同。
func (s *Store) HistoryStale(id string, h *History) (bool, error) {
	if h == nil || h.revision == "" {
		return false, nil
	}
	d, err := s.get(id)
	if err != nil {
		return false, err
	}
	return d.revision() != h.revision, nil
}

func (h *History) Clone() *History {
	if h == nil {
		return nil
	}
	clone := *h
	clone.refs = append([]int(nil), h.refs...)
	clone.raw = append([]int(nil), h.raw...)
	clone.pending = make(map[int]ai.Message, len(h.pending))
	for seq, msg := range h.pending {
		clone.pending[seq] = msg
	}
	clone.compactions = append([]Compaction(nil), h.compactions...)
	return &clone
}

func (s *Store) History(id string, initial []ai.Message) (*History, []ai.Message, error) {
	d, err := s.get(id)
	if err != nil {
		return nil, nil, err
	}
	h, msgs := d.history(initial)
	return h, msgs, nil
}

func (d *sessionData) history(initial []ai.Message) (*History, []ai.Message) {
	h := &History{prefix: len(initial), raw: sortedKeys(d.Messages), pending: map[int]ai.Message{}, revision: d.revision()}
	if len(h.raw) > 0 {
		h.next = h.raw[len(h.raw)-1] + 1
		h.start = h.raw[0]
		if d.Messages[h.start].Role == "system" {
			h.start++
		}
	}
	msgs, refs := d.contextEntries()
	if len(msgs) > 0 && msgs[0].Role == "system" && refs[0] >= 0 {
		msgs, refs = msgs[1:], refs[1:]
	}
	h.refs = make([]int, len(initial))
	for i := range h.refs {
		h.refs[i] = -1
	}
	h.refs = append(h.refs, refs...)
	return h, append(slices.Clone(initial), msgs...)
}

func (h *History) Observe(msgs []ai.Message) error {
	if len(msgs) < len(h.refs) {
		return fmt.Errorf("session history shortened without a compaction: %d < %d", len(msgs), len(h.refs))
	}
	for i, msg := range msgs {
		if i >= len(h.refs) {
			h.refs = append(h.refs, h.next)
			h.raw = append(h.raw, h.next)
			h.next++
		}
		if seq := h.refs[i]; seq >= 0 {
			h.pending[seq] = msg
		}
	}
	return nil
}

func (h *History) Compact(summary string, cutoff int, model string, usage ai.Usage) error {
	if cutoff <= 1 || cutoff > len(h.refs) {
		return fmt.Errorf("invalid compaction cutoff %d for %d messages", cutoff, len(h.refs))
	}
	rawCutoff := len(h.raw)
	for _, seq := range h.refs[cutoff:] {
		if seq >= 0 {
			rawCutoff = slices.Index(h.raw, seq)
			break
		}
	}
	h.compactions = append(h.compactions, Compaction{Cutoff: rawCutoff, Summary: summary, Model: model, Usage: usage, DropPrior: true})
	h.refs = append([]int{h.refs[0], -1}, h.refs[cutoff:]...)
	return nil
}

func (h *History) NewContext(cutoff int) error {
	if cutoff <= 1 || cutoff > len(h.refs) {
		return fmt.Errorf("invalid new-context cutoff %d for %d messages", cutoff, len(h.refs))
	}
	rawCutoff := len(h.raw)
	for _, seq := range h.refs[cutoff:] {
		if seq >= 0 {
			rawCutoff = slices.Index(h.raw, seq)
			break
		}
	}
	h.compactions = append(h.compactions, Compaction{Cutoff: rawCutoff, Fresh: true})
	h.refs = []int{h.refs[0]}
	return nil
}

func (s *Store) SaveHistory(id string, h *History, msgs []ai.Message, model, provider string) error {
	return s.saveHistory(id, h, msgs, model, provider, nil, "")
}

func (s *Store) SaveHistoryWithUsage(id string, h *History, msgs []ai.Message, model, provider string, usage ai.UsageSummary) error {
	return s.saveHistory(id, h, msgs, model, provider, &usage, "")
}

func (s *Store) SaveHistoryWithUsageAtRevision(id string, h *History, msgs []ai.Message, model, provider string, usage ai.UsageSummary, expectedRevision string) error {
	return s.saveHistory(id, h, msgs, model, provider, &usage, expectedRevision)
}

func (s *Store) saveHistory(id string, h *History, msgs []ai.Message, model, provider string, usage *ai.UsageSummary, expectedRevision string) error {
	if err := h.Observe(msgs); err != nil {
		return err
	}
	var saved string
	err := s.update(id, func(d *sessionData) error {
		if expectedRevision != "" && expectedRevision != d.revision() {
			return ErrRevision
		}
		if err := s.guardExternalRunLocked(id); err != nil {
			return err
		}
		if h.revision != "" && h.revision != d.revision() {
			return ErrHistoryStale
		}
		if usage != nil {
			d.Meta.setUsage(*usage)
		}
		for seq, msg := range h.pending {
			d.Messages[seq] = msg
		}
		n := 0
		for seq := range d.Compactions {
			n = max(n, seq)
		}
		for _, c := range h.compactions {
			n++
			c.Seq = n
			d.Compactions[n] = c
		}
		raw := d.rawMessages()
		if err := saveMessages(d, len(raw), raw, model, provider); err != nil {
			return err
		}
		saved = d.revision()
		return nil
	})
	if err == nil {
		clear(h.pending)
		h.compactions = nil
		h.revision = saved
	}
	return err
}

func (d *sessionData) contextEntries() ([]ai.Message, []int) {
	refs := sortedKeys(d.Messages)
	msgs := d.rawMessages()
	keys := sortedKeys(d.Compactions)
	if len(keys) > 0 {
		c := d.Compactions[keys[len(keys)-1]]
		indexes := compactionIndexes(c, msgs)
		if indexes != nil {
			view := make([]ai.Message, 0, len(indexes))
			mapped := make([]int, 0, len(indexes))
			for _, i := range indexes {
				if i < 0 {
					view = append(view, ai.Message{Role: "system", Content: "Summary of the conversation so far:\n\n" + c.Summary})
					mapped = append(mapped, -1)
				} else {
					view = append(view, msgs[i])
					mapped = append(mapped, refs[i])
				}
			}
			msgs, refs = view, mapped
		}
	}
	return answerDanglingToolCalls(msgs, refs)
}

func compactionIndexes(c Compaction, msgs []ai.Message) []int {
	if c.Cutoff < 0 || c.Cutoff > len(msgs) || (!c.DropPrior && c.Cutoff <= 1) {
		return nil
	}
	var out []int
	if c.Fresh {
		if len(msgs) > 0 && msgs[0].Role == "system" {
			out = append(out, 0)
		}
		for i := c.Cutoff; i < len(msgs); i++ {
			out = append(out, i)
		}
		return out
	}
	if len(msgs) > 0 && (!c.DropPrior || msgs[0].Role == "system") {
		out = append(out, 0)
	}
	out = append(out, -1)
	fold := c.Cutoff
	if !c.DropPrior {
		for fold < len(msgs) && msgs[fold].Role == "system" {
			fold++
		}
		prior := -1
		for i := 1; i < fold; i++ {
			if msgs[i].Role == "system" {
				prior = i
			}
		}
		if prior >= 0 {
			out = append(out, prior)
		}
	}
	for i := fold; i < len(msgs); i++ {
		out = append(out, i)
	}
	return out
}
