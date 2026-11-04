package agent

import (
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func FanIn(evs ...Events) Events {
	return Events{
		OnPhase: func(stage string) {
			for _, e := range evs {
				if e.OnPhase != nil {
					e.OnPhase(stage)
				}
			}
		},
		OnLifecycle: func(event string) {
			for _, e := range evs {
				if e.OnLifecycle != nil {
					e.OnLifecycle(event)
				}
			}
		},
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
		OnHostedSearch: func(search ai.HostedSearch) {
			for _, e := range evs {
				if e.OnHostedSearch != nil {
					e.OnHostedSearch(search)
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
		OnToolResult: func(id, name string, result tools.Result) {
			for _, e := range evs {
				if e.OnToolResult != nil {
					e.OnToolResult(id, name, result)
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
		OnOutputLimitContinue: func(attempt int) {
			for _, e := range evs {
				if e.OnOutputLimitContinue != nil {
					e.OnOutputLimitContinue(attempt)
				}
			}
		},
	}
}
