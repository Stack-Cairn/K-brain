package permissions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStoreAddDedupeRemove(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	path, err := UserRulesPath()
	if err != nil {
		t.Fatal(err)
	}
	first, err := AddRule(path, "", StoredRule{Rule: "bash(npm test:*)", Effect: Allow, SourceSession: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.Rule != "Bash(npm test:*)" || first.CreatedAt.IsZero() {
		t.Fatalf("stored rule is normalized and stamped: %+v", first)
	}
	again, err := AddRule(path, "", StoredRule{Rule: "Bash(npm test:*)", Effect: Allow})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID {
		t.Fatalf("same rule and effect is stored once: %+v vs %+v", again, first)
	}
	if _, err := AddRule(path, "", StoredRule{Rule: "Bash(npm test:*)", Effect: Deny}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddRule(path, "", StoredRule{Rule: "Bash(", Effect: Allow}); err == nil {
		t.Fatal("malformed rules are refused")
	}
	if _, err := AddRule(path, "", StoredRule{Rule: "Bash(ls)", Effect: "maybe"}); err == nil {
		t.Fatal("unknown effects are refused")
	}
	file, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if file.Version != fileVersion || len(file.Rules) != 2 {
		t.Fatalf("file = %+v", file)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("rules file mode = %o", info.Mode().Perm())
		}
	}
	removed, err := RemoveRule(path, first.ID)
	if err != nil || !removed {
		t.Fatalf("remove: %v %v", removed, err)
	}
	if removed, err := RemoveRule(path, first.ID); err != nil || removed {
		t.Fatalf("second remove: %v %v", removed, err)
	}
	file, _ = LoadFile(path)
	if len(file.Rules) != 1 || file.Rules[0].Effect != Deny {
		t.Fatalf("after remove: %+v", file)
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	file, err := LoadFile(filepath.Join(t.TempDir(), "none.json"))
	if err != nil || len(file.Rules) != 0 {
		t.Fatalf("missing file: %+v %v", file, err)
	}
}

func TestCorruptFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("a corrupt rules file must not silently become an empty rule set")
	}
	if _, err := AddRule(path, "", StoredRule{Rule: "Bash(ls)", Effect: Allow}); err == nil {
		t.Fatal("adding to a corrupt file must not overwrite it")
	}
}

func TestProjectRulesPath(t *testing.T) {
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	base := t.TempDir()
	a, err := ProjectRulesPath(filepath.Join(base, "a"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ProjectRulesPath(filepath.Join(base, "b"))
	if a == b {
		t.Fatal("different projects need different files")
	}
	same, _ := ProjectRulesPath(filepath.Join(base, "a") + string(filepath.Separator))
	if same != a {
		t.Fatal("a trailing separator is the same project")
	}
	if runtime.GOOS == "windows" {
		upper, _ := ProjectRulesPath(strings.ToUpper(filepath.Join(base, "a")))
		if upper != a {
			t.Fatal("Windows paths compare case-insensitively")
		}
	}
	if _, err := ProjectRulesPath(""); err == nil {
		t.Fatal("an empty project root has no rules file")
	}
	if _, err := ProjectRulesPath("relative/dir"); err == nil {
		t.Fatal("project roots must be absolute")
	}
	stored, err := AddRule(a, filepath.Join(base, "a"), StoredRule{Rule: "Edit(src/**)", Effect: Allow})
	if err != nil {
		t.Fatal(err)
	}
	file, _ := LoadFile(a)
	if file.ProjectRoot != filepath.Join(base, "a") || file.Rules[0].ID != stored.ID {
		t.Fatalf("project file records its root: %+v", file)
	}
}

func TestLegacyTUIImport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	legacy := []string{"bash:git checkout", "write:/tmp/a.txt", "edit:notes.md", "CronTaskManager:list", "mcp__x__y:", "bash:", "garbage"}
	data, _ := json.Marshal(legacy)
	legacyPath := filepath.Join(home, legacyRulesFile)
	if err := os.WriteFile(legacyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := LoadUserRules()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range set.Rules {
		if r.Effect != Allow {
			t.Fatalf("legacy rules are allow rules: %+v", r)
		}
		got[r.Rule] = true
	}
	for _, want := range []string{"Bash(git checkout:*)", "Edit(/tmp/a.txt)", "Edit(notes.md)", "CronTaskManager(list)", "mcp__x__y"} {
		if !got[want] {
			t.Errorf("missing imported rule %q in %v", want, got)
		}
	}
	if len(set.Rules) != 5 {
		t.Fatalf("empty and malformed legacy entries are dropped, got %v", got)
	}
	if set.Scope != ScopeUser {
		t.Fatalf("scope = %q", set.Scope)
	}
	// The imported rule behaves like the old one for the command it was made for.
	if d := Evaluate(Request{Tool: "bash", Command: "git checkout other", Shell: "bash"}, Options{}, set); d.Effect != Allow {
		t.Fatalf("imported rule: %+v", d)
	}
	// Removing an imported rule must not bring it back on the next load.
	path, _ := UserRulesPath()
	for _, r := range set.Rules {
		if r.Rule == "Bash(git checkout:*)" {
			if _, err := RemoveRule(path, r.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	set, err = LoadUserRules()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range set.Rules {
		if r.Rule == "Bash(git checkout:*)" {
			t.Fatal("legacy rules are imported only once")
		}
	}
	if after, _ := os.ReadFile(legacyPath); string(after) != string(data) {
		t.Fatal("the legacy file is left untouched")
	}
}

func TestEvaluateAcrossScopes(t *testing.T) {
	session := RuleSet{Scope: ScopeSession, Rules: []StoredRule{{Rule: "Bash(make:*)", Effect: Allow}}}
	project := RuleSet{Scope: ScopeProject, Rules: []StoredRule{{Rule: "Bash(make deploy)", Effect: Deny}}}
	if d := Evaluate(Request{Tool: "Bash", Command: "make build", Shell: "bash"}, Options{}, session, project); d.Effect != Allow || d.Scope != ScopeSession {
		t.Fatalf("allow from session: %+v", d)
	}
	if d := Evaluate(Request{Tool: "Bash", Command: "make deploy", Shell: "bash"}, Options{}, session, project); d.Effect != Deny || d.Scope != ScopeProject {
		t.Fatalf("deny from project wins: %+v", d)
	}
}
