package backend

import (
	"context"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

type stubMemoryRuntime struct{}

func (stubMemoryRuntime) Inject(context.Context, string) (string, error) { return "", nil }
func (stubMemoryRuntime) Completed(context.Context, agent.MemoryTurn)    {}
func (stubMemoryRuntime) Abandoned(string, ai.Message)                   {}
func (stubMemoryRuntime) Close() error                                   { return nil }
func (stubMemoryRuntime) Tools(func() string) []tools.Tool {
	return []tools.Tool{{Def: ai.NewTool("memory_manager", "memory", `{"type":"object"}`)}}
}

// The desktop backend attaches a memory runtime to every session, which
// replaces remember and forget with memory_manager. A client still sending
// policies for them must not have every run rejected.
func TestRunOptionsAcceptMemoryToolPoliciesWithMemoryRuntime(t *testing.T) {
	ag := agent.New(nil, "m", 100, "sys")
	ag.SetMemoryRuntime(stubMemoryRuntime{})
	for _, tool := range ag.KnownTools() {
		if name := tool.Def.Function.Name; name == "remember" || name == "forget" {
			t.Fatalf("%s should be replaced by the memory runtime", name)
		}
	}
	policies := map[string]string{"remember": "allow", "forget": "ask", "memory_manager": "allow"}
	got, err := normalizeRunOptions(&protocol.RunOptions{Tools: &protocol.ToolSelection{Policies: policies}}, t.TempDir(), ag.KnownTools())
	if err != nil {
		t.Fatal(err)
	}
	if got.Tools.Policies["remember"] != "allow" || got.Tools.Policies["memory_manager"] != "allow" {
		t.Fatalf("policies = %v", got.Tools.Policies)
	}
	if _, err := normalizeRunOptions(&protocol.RunOptions{Tools: &protocol.ToolSelection{Policies: map[string]string{"Browser": "allow"}}}, t.TempDir(), ag.KnownTools()); err == nil {
		t.Fatal("a tool K-brain does not know is still rejected")
	}
}
