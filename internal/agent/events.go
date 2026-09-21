package agent

import "github.com/Stack-Cairn/K-brain/internal/ai"

func FanIn(evs ...Events) Events {
	return Events{
		OnText: func(s string) {
			for _, e := range evs {
				if e.OnText != nil {
					e.OnText(s)
				}
			}
		},
		OnThink: func(s string) {
			for _, e := range evs {
				if e.OnThink != nil {
					e.OnThink(s)
				}
			}
		},
		OnToolStart: func(id, name, args string) {
			for _, e := range evs {
				if e.OnToolStart != nil {
					e.OnToolStart(id, name, args)
				}
			}
		},
		OnToolCall: func(id, name, args string) {
			for _, e := range evs {
				if e.OnToolCall != nil {
					e.OnToolCall(id, name, args)
				}
			}
		},
		OnToolEnd: func(id, name, result string) {
			for _, e := range evs {
				if e.OnToolEnd != nil {
					e.OnToolEnd(id, name, result)
				}
			}
		},
		OnToolOutput: func(id, output string) {
			for _, e := range evs {
				if e.OnToolOutput != nil {
					e.OnToolOutput(id, output)
				}
			}
		},
		OnSteer: func(text string) {
			for _, e := range evs {
				if e.OnSteer != nil {
					e.OnSteer(text)
				}
			}
		},
		OnCompact: func(took, kept int) {
			for _, e := range evs {
				if e.OnCompact != nil {
					e.OnCompact(took, kept)
				}
			}
		},
		OnCompacted: func(sum string, cutoff int, info CompactInfo) {
			for _, e := range evs {
				if e.OnCompacted != nil {
					e.OnCompacted(sum, cutoff, info)
				}
			}
		},
		OnCompactStart: func(took, est int) {
			for _, e := range evs {
				if e.OnCompactStart != nil {
					e.OnCompactStart(took, est)
				}
			}
		},
		OnNewContextStart: func(took, est int) {
			for _, e := range evs {
				if e.OnNewContextStart != nil {
					e.OnNewContextStart(took, est)
				}
			}
		},
		OnNewContext: func(cutoff int) {
			for _, e := range evs {
				if e.OnNewContext != nil {
					e.OnNewContext(cutoff)
				}
			}
		},
		OnUsage: func(u ai.Usage) {
			for _, e := range evs {
				if e.OnUsage != nil {
					e.OnUsage(u)
				}
			}
		},
		OnRetry: func(event ai.RetryEvent) {
			for _, e := range evs {
				if e.OnRetry != nil {
					e.OnRetry(event)
				}
			}
		},
		OnDecay: func(n int) {
			for _, e := range evs {
				if e.OnDecay != nil {
					e.OnDecay(n)
				}
			}
		},
	}
}
