package agent

import (
	"strings"
	"testing"
)

func TestPlanModeKeepsCLISearchTools(t *testing.T) {
	for _, name := range []string{"grep", "glob", "list"} {
		if !planToolAllowed(name) {
			t.Errorf("plan mode should keep the read-only %s tool", name)
		}
	}
	if !strings.Contains(planInstruction, "grep, glob and list") {
		t.Error("the plan instruction should name the search tools")
	}
}

// web_fetch changes nothing locally, so Plan mode keeps it; its permission gate
// still decides each fetch.
func TestPlanModeKeepsWebFetch(t *testing.T) {
	if !planToolAllowed("web_fetch") {
		t.Error("plan mode should keep web_fetch")
	}
	if !strings.Contains(planInstruction, "web_fetch") {
		t.Error("the plan instruction should name web_fetch")
	}
}
