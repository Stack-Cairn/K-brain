package recording

import (
	"github.com/Stack-Cairn/K-brain/internal/agent"
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
	return &Recorder{store: store, history: history, agent: ag, id: id}, nil
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
		OnCompacted: func(summary string, cutoff int, info agent.CompactInfo) {
			if r.err == nil {
				r.err = r.history.Compact(summary, cutoff, info.Model, info.Usage)
			}
		},
	}
}

func (r *Recorder) Save() error {
	if r == nil {
		return nil
	}
	if r.err != nil {
		return r.err
	}
	return r.store.SaveHistoryWithUsage(r.id, r.history, r.agent.MessagesSnapshot(), r.agent.ModelName, r.agent.Provider, r.agent.UsageSummary())
}
