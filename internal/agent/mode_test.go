package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestPlanModeFiltersAndGuardsTools(t *testing.T) {
	a := New(ai.New("http://localhost", "test"), "model1", 100, "system")
	tool := tools.Tool{}
	tool.Def.Function.Name = "write"
	called := false
	tool.Run = func(context.Context, json.RawMessage) (string, error) { called = true; return "ok", nil }
	active := a.modeTools([]tools.Tool{tool})
	a.SetPlanMode(true)
	if len(a.modeTools([]tools.Tool{tool})) != 0 {
		t.Fatal("plan exposed write")
	}
	if _, err := active[0].Run(context.Background(), nil); err == nil || called {
		t.Fatal("already captured tool bypassed plan")
	}
	tool.Def.Function.Name = "read"
	if len(a.modeTools([]tools.Tool{tool})) != 1 {
		t.Fatal("plan blocked read")
	}
	messages := []ai.Message{{Role: "system", Content: "original"}}
	planned := a.modeMessages(messages)
	if !strings.Contains(planned[0].Content, planInstruction) || messages[0].Content != "original" {
		t.Fatal("plan instruction missing or mutated history")
	}
	a.SetPlanMode(false)
	if _, err := active[0].Run(context.Background(), nil); err != nil || !called {
		t.Fatal("leaving plan did not restore tools")
	}
}
