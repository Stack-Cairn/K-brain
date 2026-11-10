package recording

import (
	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

type Recorder struct {
	store   *session.Store
	history *session.History
	agent   *agent.Agent
	id      string
	err     error
}

func Open(store *session.Store, id string, ag *agent.Agent) (*Recorder, error) {
	if store == nil {
		return nil, nil
	}
	meta, stored, err := store.Load(id)
	if err != nil {
		return nil, err
	}
	history, msgs, err := store.History(id, ag.MessagesSnapshot())
	if err != nil {
		return nil, err
	}
	ag.Messages = msgs
	ag.RestoreUsage(meta.UsageSummary(stored))
	ag.CompactionDir = func() string { return store.CompactionDir(id) }
	return &Recorder{store: store, history: history, agent: ag, id: id}, nil
}

type Snapshot struct {
	Messages []ai.Message
	History  *session.History
	Usage    ai.UsageSummary
}

func (r *Recorder) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}
	return Snapshot{Messages: r.agent.MessagesSnapshot(), History: r.history.Clone(), Usage: r.agent.UsageSummary()}
}

func (r *Recorder) Restore(snapshot Snapshot) {
	if r == nil {
		return
	}
	// The Agent transaction restores messages and its complete usage state.
	r.history = snapshot.History
	r.err = nil
}

func (r *Recorder) Events() agent.Events {
	if r == nil {
		return agent.Events{}
	}
	return agent.Events{
		OnCompactStart: func(int, int) {
			if r.err == nil {
				r.err = r.history.Observe(r.agent.MessagesSnapshot())
			}
		},
		OnNewContextStart: func(int, int) {
			if r.err == nil {
				r.err = r.history.Observe(r.agent.MessagesSnapshot())
			}
		},
		OnNewContext: func(cutoff int) {
			if r.err == nil {
				r.err = r.history.NewContext(cutoff)
			}
		},
		OnCompacted: func(summary string, cutoff int, info agent.CompactInfo) {
			if r.err == nil {
				r.err = r.history.Compact(summary, cutoff, info.Model, info.Usage)
			}
		},
	}
}

// Stale reports whether another process changed the transcript after this
// recorder last loaded or saved it; saving would then be refused.
func (r *Recorder) Stale() (bool, error) {
	if r == nil {
		return false, nil
	}
	return r.store.HistoryStale(r.id, r.history)
}

func (r *Recorder) Save() error {
	return r.save("")
}

func (r *Recorder) SaveAtRevision(expectedRevision string) error {
	return r.save(expectedRevision)
}

func (r *Recorder) save(expectedRevision string) error {
	if r == nil {
		return nil
	}
	if r.err != nil {
		return r.err
	}
	return r.store.SaveHistoryWithUsageAtRevision(r.id, r.history, r.agent.MessagesSnapshot(), r.agent.ModelName, r.agent.Provider, r.agent.UsageSummary(), expectedRevision)
}
