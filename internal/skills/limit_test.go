package skills

import (
	"fmt"
	"testing"
)

func TestRepoSkillsSpecClean(t *testing.T) {
	sk, problems := ScanDetailed("../../.agents/skills")
	for _, p := range problems {
		t.Errorf("unparseable skill: %s: %s", p.Path, p.Err)
	}
	for _, s := range sk {
		if s.Warning != "" {
			t.Errorf("%s: %s", s.Name, s.Warning)
		}
	}
}

func TestSkillBlockBudget(t *testing.T) {
	sk := Scan("../../.agents/skills")
	block := PromptBlock(sk)
	const budget = 30_000
	if len(block) > budget {
		t.Errorf("skills block = %d chars (budget %d)", len(block), budget)
	}
	fmt.Printf("skills block: %d chars across %d skills\n", len(block), len(sk))
}
