package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func externalTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func externalTool(t *testing.T, scans []ExternalScan, tool string) ExternalScan {
	t.Helper()
	var found []ExternalScan
	for _, scan := range scans {
		if scan.Tool == tool {
			found = append(found, scan)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected one %s scan, got %d: %+v", tool, len(found), scans)
	}
	return found[0]
}

func TestExternalSkillSources(t *testing.T) {
	home := externalTestHome(t)
	roots := []struct {
		tool  string
		paths []string
	}{
		{"agents", []string{".agents/skills"}},
		{"codex", []string{".codex/skills"}},
		{"claude-code", []string{".claude/skills"}},
		{"gemini", []string{".gemini/skills", ".gemini/config/skills", ".gemini/antigravity-cli/skills", ".gemini/antigravity/skills"}},
	}
	for _, source := range roots {
		for _, path := range source.paths {
			fixtureSkill(t, filepath.Join(home, filepath.FromSlash(path)), "fixture-skill")
		}
	}
	m := &Manager{}
	scans := m.External()
	if len(scans) != len(roots) {
		t.Fatalf("expected %d sources, got %d", len(roots), len(scans))
	}
	for _, source := range roots {
		scan := externalTool(t, scans, source.tool)
		if !scan.Exists || len(scan.Skills) != len(source.paths) || len(scan.Errors) != 0 {
			t.Fatalf("%s: %+v", source.tool, scan)
		}
		var absoluteRoots []string
		for _, path := range source.paths {
			root := filepath.Join(home, filepath.FromSlash(path))
			absoluteRoots = append(absoluteRoots, root)
			found := false
			for _, skill := range scan.Skills {
				if skill.BaseDir == filepath.Join(root, "fixture-skill") {
					found = true
					if skill.Name != "fixture-skill" || skill.Description != "Fixture skill" || skill.SkillFile != filepath.Join(skill.BaseDir, "SKILL.md") {
						t.Fatalf("invalid import metadata: %+v", skill)
					}
				}
			}
			if !found {
				t.Fatalf("missing source root %s", root)
			}
		}
		if scan.RootDir != strings.Join(absoluteRoots, ", ") {
			t.Fatalf("unexpected roots for %s: %s", source.tool, scan.RootDir)
		}
	}
}

func TestExternalAbsentRoots(t *testing.T) {
	home := externalTestHome(t)
	gemini := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(gemini, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gemini, "skills"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	scans := (&Manager{}).External()
	for _, tool := range []string{"agents", "codex", "claude-code", "gemini"} {
		scan := externalTool(t, scans, tool)
		if scan.Exists || len(scan.Skills) != 0 || scan.Skills == nil || scan.Errors == nil {
			t.Fatalf("unexpected missing-root state: %+v", scan)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("scan created a source directory: %v", err)
	}
}

func TestExternalAntigravitySecondaryRoots(t *testing.T) {
	for _, path := range []string{".gemini/config/skills", ".gemini/antigravity-cli/skills", ".gemini/antigravity/skills"} {
		t.Run(path, func(t *testing.T) {
			home := externalTestHome(t)
			root := filepath.Join(home, filepath.FromSlash(path))
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			m := &Manager{}
			if scan := externalTool(t, m.External(), "gemini"); !scan.Exists || len(scan.Skills) != 0 {
				t.Fatalf("empty directory: %+v", scan)
			}
			fixtureSkill(t, root, "secondary")
			scans := m.External()
			if scan := externalTool(t, scans, "gemini"); !scan.Exists || len(scan.Skills) != 1 {
				t.Fatalf("secondary directory: %+v", scan)
			}
			for _, scan := range scans {
				if scan.Tool == "antigravity" {
					t.Fatal("Antigravity must be grouped under Gemini")
				}
			}
		})
	}
}

func TestExternalOverlappingRootsPreservePacksAndDistinctSkills(t *testing.T) {
	root := t.TempDir()
	pack := filepath.Join(root, "collection")
	fixtureSkill(t, pack, "nested")
	fixtureSkill(t, root, "zebra")
	secondary := t.TempDir()
	fixtureSkill(t, secondary, "alpha")
	fixtureSkill(t, secondary, "nested")
	m := &Manager{}
	scan := m.scanExternal("gemini", root, pack, root, secondary)
	var names []string
	for _, skill := range scan.Skills {
		names = append(names, skill.Name)
		if skill.BaseDir == filepath.Join(pack, "nested") && skill.Pack != "collection" {
			t.Fatalf("lost pack metadata: %+v", skill)
		}
	}
	if !reflect.DeepEqual(names, []string{"alpha", "nested", "nested", "zebra"}) || len(scan.Errors) != 0 {
		t.Fatalf("unexpected merged scan: %+v", scan)
	}
}

func TestExternalReportsMalformedSkillsWithoutLosingValidSkills(t *testing.T) {
	home := externalTestHome(t)
	root := filepath.Join(home, ".gemini", "config", "skills")
	fixtureSkill(t, root, "valid")
	broken := fixtureSkill(t, root, "broken")
	if err := os.WriteFile(filepath.Join(broken, "SKILL.md"), []byte("no frontmatter"), 0600); err != nil {
		t.Fatal(err)
	}
	secondary := filepath.Join(home, ".gemini", "antigravity", "skills")
	fixtureSkill(t, secondary, "secondary")
	m := &Manager{}
	scan := externalTool(t, m.External(), "gemini")
	if len(scan.Skills) != 2 || len(scan.Errors) != 1 || !strings.Contains(scan.Errors[0], filepath.Join(broken, "SKILL.md")) {
		t.Fatalf("unexpected partial scan: %+v", scan)
	}
	scan = m.scanExternal("gemini", root, root)
	if len(scan.Skills) != 1 || len(scan.Errors) != 1 {
		t.Fatalf("duplicate errors or skills: %+v", scan)
	}
}

func TestExternalCanonicalRootAliases(t *testing.T) {
	root := t.TempDir()
	fixtureSkill(t, root, "fixture")
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	m := &Manager{}
	scan := m.scanExternal("gemini", root, alias)
	if len(scan.Skills) != 1 || len(scan.Errors) != 0 {
		t.Fatalf("canonical alias was not deduplicated: %+v", scan)
	}
}

func TestExternalManageScanAndImport(t *testing.T) {
	home := externalTestHome(t)
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	m, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".gemini/skills", ".gemini/config/skills", ".gemini/antigravity-cli/skills", ".gemini/antigravity/skills"} {
		src := fixtureSkill(t, filepath.Join(home, filepath.FromSlash(path)), "importable")
		before, err := os.ReadFile(filepath.Join(src, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		response, err := m.Manage(context.Background(), map[string]any{"action": "scan_external"})
		if err != nil {
			t.Fatal(err)
		}
		// Use the JSON shape consumed by /v1/skills/manage clients.
		wire, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		var decoded ManagerResponse
		if err := json.Unmarshal(wire, &decoded); err != nil {
			t.Fatal(err)
		}
		var entry ExternalSkill
		for _, skill := range externalTool(t, decoded.External, "gemini").Skills {
			if skill.BaseDir == src {
				entry = skill
			}
		}
		if entry.BaseDir == "" {
			t.Fatalf("source absent from manage response: %s", src)
		}
		name := "import-" + strings.ReplaceAll(path, "/", "-")[1:]
		installed, err := m.Install(context.Background(), entry.BaseDir, name, "fail", nil, nil)
		if err != nil {
			t.Fatalf("install %s: %v", path, err)
		}
		if installed.Name != name {
			t.Fatalf("unexpected installed name: %+v", installed)
		}
		if validation := m.Validate(name); !validation.OK {
			t.Fatalf("invalid imported skill: %+v", validation)
		}
		after, err := os.ReadFile(filepath.Join(src, "SKILL.md"))
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("import mutated source: %v", err)
		}
	}
}
