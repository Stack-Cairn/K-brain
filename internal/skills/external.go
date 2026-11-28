package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

var externalSkillSources = []struct {
	tool  string
	roots []string
}{
	{"agents", []string{".agents/skills"}},
	{"codex", []string{".codex/skills"}},
	{"claude-code", []string{".claude/skills"}},
	{"gemini", []string{
		".gemini/skills",
		".gemini/config/skills",
		".gemini/antigravity-cli/skills",
		".gemini/antigravity/skills",
	}},
}

func (m *Manager) External() []ExternalScan {
	home, err := os.UserHomeDir()
	if err != nil {
		return []ExternalScan{}
	}
	scans := make([]ExternalScan, 0, len(externalSkillSources))
	for _, source := range externalSkillSources {
		roots := make([]string, 0, len(source.roots))
		for _, root := range source.roots {
			roots = append(roots, filepath.Join(home, filepath.FromSlash(root)))
		}
		scans = append(scans, m.scanExternal(source.tool, roots...))
	}
	return scans
}

func (m *Manager) scanExternal(tool string, roots ...string) ExternalScan {
	x := ExternalScan{Tool: tool, RootDir: strings.Join(roots, ", "), Skills: []ExternalSkill{}, Errors: []string{}}
	seenRoots := map[string]bool{}
	seenSkills := map[string]bool{}
	seenProblems := map[string]bool{}
	for _, root := range roots {
		key := externalPathKey(root)
		if seenRoots[key] {
			continue
		}
		seenRoots[key] = true
		info, err := os.Stat(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			x.Errors = append(x.Errors, fmt.Sprintf("%s: %v", root, err))
			continue
		}
		if !info.IsDir() {
			continue
		}
		x.Exists = true
		if _, err := os.ReadDir(root); err != nil {
			x.Errors = append(x.Errors, fmt.Sprintf("%s: %v", root, err))
			continue
		}
		result := scanRoot(root)
		for _, s := range result.Skills {
			key := externalPathKey(s.Path)
			if seenSkills[key] {
				continue
			}
			seenSkills[key] = true
			x.Skills = append(x.Skills, ExternalSkill{Name: s.Name, Description: s.Description, BaseDir: filepath.Dir(s.Path), SkillFile: s.Path, Pack: s.Pack})
		}
		for _, problem := range result.Problems {
			key := externalPathKey(problem.Path) + ": " + problem.Err
			if !seenProblems[key] {
				seenProblems[key] = true
				x.Errors = append(x.Errors, fmt.Sprintf("%s: %s", problem.Path, problem.Err))
			}
		}
	}
	sort.SliceStable(x.Skills, func(i, j int) bool {
		a, b := strings.ToLower(x.Skills[i].Name), strings.ToLower(x.Skills[j].Name)
		if a != b {
			return a < b
		}
		return x.Skills[i].BaseDir < x.Skills[j].BaseDir
	})
	return x
}

func externalPathKey(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}
