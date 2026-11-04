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
