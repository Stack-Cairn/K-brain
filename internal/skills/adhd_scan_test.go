package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestADHDSkillScans(t *testing.T) {

	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, ".agents", "skills")); err != nil {
		t.Skip("no .agents/skills in this checkout")
	}
	sk := Scan(filepath.Join(root, ".agents", "skills"))
	for _, s := range sk {
		if s.Name == "i-have-adhd" {
			if s.Description == "" {
				t.Fatal("empty description")
			}
			t.Logf("i-have-adhd: %.70s…", s.Description)
			return
		}
	}
	t.Fatal("i-have-adhd not scanned from .agents/skills")
}
