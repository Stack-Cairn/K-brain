package skills

import (
	"strings"
	"testing"
)

func TestPonytailSkillIndexed(t *testing.T) {
	idx := Scan("../../.agents/skills")
	var found bool
	for _, s := range idx {
		if s.Name == "ponytail" {
			found = true
			if !strings.Contains(s.Description, "lazy senior dev") {
				t.Errorf("description: %q", s.Description)
			}
		}
	}
	if !found {
		var names []string
		for _, s := range idx {
			names = append(names, s.Name)
		}
		t.Fatalf("ponytail not indexed; got %d skills", len(names))
	}
}
