package permissions

import (
	"path/filepath"
	"reflect"
	"testing"
)

func scopeRules(levels []ScopeLevel) [][]string {
	out := make([][]string, len(levels))
	for i, l := range levels {
		out[i] = l.Rules
	}
	return out
}

func TestSuggestScopesWidenShellCommands(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    [][]string
		def     int
	}{
		{"go test ./internal/...", [][]string{{"Bash(go test ./internal/...)"}, {"Bash(go test:*)"}, {"Bash(go:*)"}}, 1},
		// A one-word prefix has no wider command level.
		{"make lint", [][]string{{"Bash(make lint)"}, {"Bash(make:*)"}}, 1},
		// Interpreters are remembered as the exact command only.
		{"python3 scripts/gen.py", [][]string{{"Bash(python3 scripts/gen.py)"}}, 0},
		// Each simple command of a compound one widens together.
		{"go build ./... && npm run build -- --prod", [][]string{
			{"Bash(go build ./...)", "Bash(npm run build -- --prod)"},
			{"Bash(go build:*)", "Bash(npm run build:*)"},
			{"Bash(go:*)", "Bash(npm:*)"},
		}, 1},
	} {
		req := Request{Tool: "bash", Command: tc.command, Shell: "bash"}
		levels, def := SuggestScopes(req, Options{})
		if got := scopeRules(levels); !reflect.DeepEqual(got, tc.want) || def != tc.def {
			t.Errorf("SuggestScopes(%q) = %v (default %d), want %v (default %d)", tc.command, got, def, tc.want, tc.def)
			continue
		}
		if !reflect.DeepEqual(levels[def].Rules, Suggest(req, Options{})) {
			t.Errorf("%q: default level %v is not Suggest's %v", tc.command, levels[def].Rules, Suggest(req, Options{}))
		}
		for _, l := range levels {
			if !scopeAllows(req, Options{}, l.Rules) {
				t.Errorf("%q: level %s %v does not allow the command", tc.command, l.Kind, l.Rules)
			}
		}
	}
}

func TestSuggestScopesRefuseWhatSuggestRefuses(t *testing.T) {
	for _, command := range []string{"rm -rf build", "./run.sh", "git status && rm x"} {
		if levels, def := SuggestScopes(Request{Tool: "bash", Command: command, Shell: "bash"}, Options{}); levels != nil || def != -1 {
			t.Errorf("SuggestScopes(%q) = %v, %d; want nothing to remember", command, levels, def)
		}
	}
}

func TestSuggestScopesWidenEdits(t *testing.T) {
	root := t.TempDir()
	opts := Options{ProjectRoot: root}
	levels, def := SuggestScopes(Request{Tool: "edit", Command: filepath.Join(root, "internal", "tui", "card.go")}, opts)
	want := [][]string{{"Edit(internal/tui/card.go)"}, {"Edit(internal/tui/**)"}, {"Edit(**)"}}
	if got := scopeRules(levels); !reflect.DeepEqual(got, want) || def != 1 {
		t.Fatalf("SuggestScopes(edit) = %v (default %d), want %v (default 1)", got, def, want)
	}
	if levels[0].Kind != "exact" || levels[1].Kind != "dir" || levels[2].Kind != "project" {
		t.Errorf("kinds = %s %s %s", levels[0].Kind, levels[1].Kind, levels[2].Kind)
	}
	// A file at the project root: the file itself, then the project.
	levels, def = SuggestScopes(Request{Tool: "write", Command: filepath.Join(root, "go.mod")}, opts)
	if got := scopeRules(levels); !reflect.DeepEqual(got, [][]string{{"Edit(go.mod)"}, {"Edit(**)"}}) || def != 0 {
		t.Errorf("root file scopes = %v (default %d)", got, def)
	}
}

func TestSuggestScopesSingleLevelForOtherTools(t *testing.T) {
	levels, def := SuggestScopes(Request{Tool: "web_fetch", Command: "https://pkg.go.dev/net/http"}, Options{})
	if len(levels) != 1 || def != 0 || levels[0].Kind != "host" || levels[0].Rules[0] != "WebFetch(domain:pkg.go.dev)" {
		t.Errorf("web_fetch scopes = %+v, %d", levels, def)
	}
	levels, def = SuggestScopes(Request{Tool: "mcp__docs__search", Command: "{}"}, Options{})
	if len(levels) != 1 || def != 0 || levels[0].Kind != "tool" {
		t.Errorf("mcp scopes = %+v, %d", levels, def)
	}
}
