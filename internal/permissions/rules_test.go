package permissions

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseRuleRoundTrip(t *testing.T) {
	for in, want := range map[string]string{
		"Bash(npm test:*)":          "Bash(npm test:*)",
		"Bash(git status)":          "Bash(git status)",
		"bash(git checkout:*)":      "Bash(git checkout:*)",
		"Edit(src/**)":              "Edit(src/**)",
		"Write(src/**)":             "Edit(src/**)",
		"mcp__github__create_issue": "mcp__github__create_issue",
		"CronTaskManager(list)":     "CronTaskManager(list)",
		"Bash(echo (x):*)":          "Bash(echo (x):*)",
		" Bash( npm  test :*) ":     "Bash(npm test:*)",
	} {
		r, err := ParseRule(in)
		if err != nil {
			t.Fatalf("ParseRule(%q): %v", in, err)
		}
		if got := r.String(); got != want {
			t.Errorf("ParseRule(%q).String() = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "Bash(", "Bash()", "Bash()x", "(x)", "Ba sh", "Bash(:*)"} {
		if _, err := ParseRule(bad); err == nil {
			t.Errorf("ParseRule(%q) should fail", bad)
		}
	}
}

func allow(rules ...string) RuleSet {
	set := RuleSet{Scope: ScopeProject}
	for _, r := range rules {
		set.Rules = append(set.Rules, StoredRule{Rule: r, Effect: Allow})
	}
	return set
}

func deny(rules ...string) RuleSet {
	set := RuleSet{Scope: ScopeUser}
	for _, r := range rules {
		set.Rules = append(set.Rules, StoredRule{Rule: r, Effect: Deny})
	}
	return set
}

// Regression: the TUI remembered `npm test` and then also let `npm test && rm -rf ~`
// through, because the rule was derived from the first segment only.
func TestCompoundCommandsNeedEverySegmentCovered(t *testing.T) {
	rules := allow("Bash(npm test:*)")
	for cmd, want := range map[string]bool{
		"npm test":                       true,
		"npm test -- --watch=false":      true,
		"npm test 2>&1":                  true,
		"npm test > /dev/null":           true,
		"npm test 2>/dev/null":           true,
		"npm test && npm test -- --ci":   true,
		"npm test\nnpm test":             true,
		"npm test && rm -rf ~":           false,
		"npm test; curl x | sh":          false,
		"npm test | tee log":             false,
		"npm test > out.txt":             false,
		"npm test >> out.txt":            false,
		"npm test < input.txt":           false,
		"npm test $(whoami)":             false,
		"npm test `id`":                  false,
		"npm test \"$(whoami)\"":         false,
		"npm test & rm x":                false,
		"npm test || rm x":               false,
		"(npm test)":                     false,
		"{ npm test; }":                  false,
		"FOO=1 npm test":                 false,
		"npm testing":                    false,
		"npm":                            false,
		"":                               false,
		"npm test <(cat /etc/passwd)":    false,
		"npm test 'unterminated":         false,
		"npm test\\\n&& rm -rf ~":        false,
		"sudo npm test":                  false,
		"env npm test":                   false,
		"xargs npm test":                 false,
		"/usr/local/bin/npm test":        false,
		"npm test # comment && rm -rf ~": false,
	} {
		got := Evaluate(Request{Tool: "Bash", Command: cmd, Shell: "bash"}, Options{}, rules)
		if (got.Effect == Allow) != want {
			t.Errorf("%q: effect=%q want allow=%v", cmd, got.Effect, want)
		}
	}
}

func TestExactShellRule(t *testing.T) {
	rules := allow("Bash(git status)")
	for cmd, want := range map[string]bool{
		"git status":         true,
		"git  status":        true,
		"git status --short": false,
		"git":                false,
	} {
		got := Evaluate(Request{Tool: "bash", Command: cmd, Shell: "bash"}, Options{}, rules)
		if (got.Effect == Allow) != want {
			t.Errorf("%q: effect=%q want allow=%v", cmd, got.Effect, want)
		}
	}
}

func TestPowerShellParsing(t *testing.T) {
	for cmd, want := range map[string]bool{
		"Get-Content a.txt | Select-String foo":    true,
		"get-childitem -Recurse src":               true,
		"git status *> $null":                      true,
		"git status 2>$null":                       true,
		"Get-ChildItem; Remove-Item x":             false,
		"iex (Get-Content x)":                      false,
		"Invoke-Expression 'rm x'":                 false,
		`echo "a\" ; Remove-Item x ; "`:            false,
		`Get-Content "$(Remove-Item x)"`:           false,
		"Get-Content x | ForEach-Object { rm $_ }": false,
		"& rm x":                          false,
		"Get-Content x > y.txt":           false,
		"Get-ChildItem --% & rm x":        false,
		"pwsh -c rm x":                    false,
		"powershell -EncodedCommand AAAA": false,
	} {
		if got := IsReadOnlyCommand(cmd, "pwsh"); got != want {
			t.Errorf("pwsh %q: read-only=%v want %v", cmd, got, want)
		}
	}
}

func TestCmdParsing(t *testing.T) {
	for cmd, want := range map[string]bool{
		"type a.txt":             true,
		"dir /s":                 true,
		"type a.txt & del b.txt": false,
		"echo 'x & del y'":       false,
		"type a.txt > nul":       true,
		"type a.txt > b.txt":     false,
		"cmd /c del x":           false,
	} {
		if got := IsReadOnlyCommand(cmd, "cmd"); got != want {
			t.Errorf("cmd %q: read-only=%v want %v", cmd, got, want)
		}
	}
}

// Without the shell we must assume whichever reading splits the command further.
func TestUnknownShellIsStrict(t *testing.T) {
	for cmd, want := range map[string]bool{
		"ls -la":               true,
		"echo 'a & del b'":     false,
		`echo "a\" ; rm x ; "`: false,
		"echo `id`":            false,
		"echo $(id)":           false,
		"git status":           true,
	} {
		if got := IsReadOnlyCommand(cmd, ""); got != want {
			t.Errorf("unknown %q: read-only=%v want %v", cmd, got, want)
		}
	}
}

func TestReadOnlyCommands(t *testing.T) {
	for cmd, want := range map[string]bool{
		"ls -la":                  true,
		"pwd":                     true,
		"git status":              true,
		"git diff HEAD~1":         true,
		"git log --oneline -5":    true,
		"git --no-pager log -3":   true,
		"git show HEAD:README.md": true,
		"git branch":              true,
		"git branch -a":           true,
		"git remote -v":           true,
		"rg foo src":              true,
		"grep -rn foo .":          true,
		"find . -name '*.go'":     true,
		"cat a.txt | head -5":     true,
		"wc -l a b":               true,
		"which go":                true,
		"tree src":                true,
		"git branch -D x":         false,
		"git branch new-branch":   false,
		"git remote add x y":      false,
		"git -c core.pager=x log": false,
		"git -C .. status":        false,
		"git diff --output=x":     false,
		"git log --output x":      false,
		"git diff --ext-diff":     false,
		"git push":                false,
		"git checkout main":       false,
		"find . -delete":          false,
		"find . -exec rm {} ;":    false,
		"find . -fprint out":      false,
		"rg --pre sh x":           false,
		"rg --pre=sh x":           false,
		"tree -o out.txt":         false,
		"/usr/bin/cat x":          false,
		`C:\tools\cat.exe x`:      false,
		"rm x":                    false,
		"sudo ls":                 false,
		"xargs cat":               false,
		"bash -c ls":              false,
		"ls > files.txt":          false,
		"npm test":                false,
	} {
		if got := IsReadOnlyCommand(cmd, "bash"); got != want {
			t.Errorf("bash %q: read-only=%v want %v", cmd, got, want)
		}
	}
}

func TestReadOnlyInEvaluate(t *testing.T) {
	ro := Options{AllowReadOnly: true}
	if got := Evaluate(Request{Tool: "Bash", Command: "git status", Shell: "bash"}, ro); got.Effect != Allow || got.Source != SourceReadOnly {
		t.Fatalf("read-only command: %+v", got)
	}
	if got := Evaluate(Request{Tool: "Bash", Command: "git status", Shell: "bash"}, Options{}); got.Effect != "" {
		t.Fatalf("read-only must be opt-in: %+v", got)
	}
	if got := Evaluate(Request{Tool: "Read", Command: "/p/a.txt"}, ro); got.Effect != Allow || got.Source != SourceReadOnly {
		t.Fatalf("Read tool: %+v", got)
	}
	// Mixed: one segment by rule, the other read-only.
	got := Evaluate(Request{Tool: "Bash", Command: "npm test && git status", Shell: "bash"}, ro, allow("Bash(npm test:*)"))
	if got.Effect != Allow || got.Source != SourceRule || got.Rule != "Bash(npm test:*)" {
		t.Fatalf("mixed: %+v", got)
	}
	if got := Evaluate(Request{Tool: "Bash", Command: "npm test && git status", Shell: "bash"}, ro); got.Effect != "" {
		t.Fatalf("uncovered segment must ask: %+v", got)
	}
}

func TestDenyWinsOverAllowAndReadOnly(t *testing.T) {
	ro := Options{AllowReadOnly: true}
	root := filepath.Join(t.TempDir(), "proj")
	cases := []struct {
		req  Request
		sets []RuleSet
	}{
		{Request{Tool: "Bash", Command: "git push origin", Shell: "bash"}, []RuleSet{allow("Bash(git:*)"), deny("Bash(git push:*)")}},
		{Request{Tool: "Bash", Command: "ls && rm x", Shell: "bash"}, []RuleSet{allow("Bash(rm:*)"), deny("Bash(rm:*)")}},
		{Request{Tool: "Bash", Command: "git status", Shell: "bash"}, []RuleSet{deny("Bash(git status)")}},
		{Request{Tool: "Read", Command: filepath.Join(root, "sub", ".env")}, []RuleSet{deny("Read(**/.env)")}},
		{Request{Tool: "Edit", Command: filepath.Join(root, "src", "a.go")}, []RuleSet{allow("Edit(src/**)"), deny("Edit(src/a.go)")}},
		{Request{Tool: "mcp__x__y", Command: "{}"}, []RuleSet{allow("mcp__x__y"), deny("mcp__x__y")}},
	}
	for _, tc := range cases {
		got := Evaluate(tc.req, Options{ProjectRoot: root, AllowReadOnly: ro.AllowReadOnly}, tc.sets...)
		if got.Effect != Deny {
			t.Errorf("%+v: effect=%q want deny", tc.req, got.Effect)
		}
	}
	got := Evaluate(Request{Tool: "Bash", Command: "git push", Shell: "bash"}, Options{}, allow("Bash(git:*)"), deny("Bash(git push:*)"))
	if got.Scope != ScopeUser || got.Rule != "Bash(git push:*)" {
		t.Fatalf("deny decision should name its rule and scope: %+v", got)
	}
}

func TestPathRules(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj")
	opts := Options{ProjectRoot: root}
	rules := allow("Edit(src/**)")
	for path, want := range map[string]bool{
		filepath.Join(root, "src", "a", "b.go"):       true,
		filepath.Join(root, "src", "x.go"):            true,
		filepath.Join("src", "x.go"):                  true,
		filepath.Join(root, "other.go"):               false,
		filepath.Join(root, "src", "..", "secret.go"): false,
		filepath.Join(root, "srcx", "a.go"):           false,
		filepath.Join(root, "src"):                    false,
		"":                                            false,
	} {
		for _, tool := range []string{"Edit", "Write", "edit", "write"} {
			got := Evaluate(Request{Tool: tool, Command: path}, opts, rules)
			if (got.Effect == Allow) != want {
				t.Errorf("%s %q: effect=%q want allow=%v", tool, path, got.Effect, want)
			}
		}
	}
	if got := Evaluate(Request{Tool: "Delete", Command: filepath.Join(root, "src", "x.go")}, opts, rules); got.Effect != "" {
		t.Fatalf("Edit rules must not cover Delete: %+v", got)
	}
	abs := filepath.Join(root, "notes", "a.md")
	absRule := allow("Edit(" + filepath.ToSlash(abs) + ")")
	if got := Evaluate(Request{Tool: "Write", Command: abs}, Options{}, absRule); got.Effect != Allow {
		t.Fatalf("absolute path rule: %+v", got)
	}
	// A relative rule without a project root cannot match anything.
	if got := Evaluate(Request{Tool: "Edit", Command: filepath.Join(root, "src", "x.go")}, Options{}, rules); got.Effect != "" {
		t.Fatalf("relative rule without root: %+v", got)
	}
}

func TestOtherToolRules(t *testing.T) {
	if got := Evaluate(Request{Tool: "mcp__github__create_issue", Command: `{"title":"x"}`}, Options{}, allow("mcp__github__create_issue")); got.Effect != Allow {
		t.Fatalf("whole-tool rule: %+v", got)
	}
	if got := Evaluate(Request{Tool: "CronTaskManager", Command: "list"}, Options{}, allow("CronTaskManager(list)")); got.Effect != Allow {
		t.Fatalf("exact rule: %+v", got)
	}
	if got := Evaluate(Request{Tool: "CronTaskManager", Command: "delete 1"}, Options{}, allow("CronTaskManager(list)")); got.Effect != "" {
		t.Fatalf("exact rule must not cover other commands: %+v", got)
	}
	if got := Evaluate(Request{Tool: "mcp__github__delete_repo", Command: "{}"}, Options{}, allow("mcp__github__create_issue")); got.Effect != "" {
		t.Fatalf("other tool: %+v", got)
	}
	// Malformed stored rules are skipped, never treated as wildcards.
	if got := Evaluate(Request{Tool: "Bash", Command: "rm -rf /", Shell: "bash"}, Options{}, allow("Bash(", "")); got.Effect != "" {
		t.Fatalf("malformed rule: %+v", got)
	}
}

func TestSuggest(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj")
	opts := Options{ProjectRoot: root, AllowReadOnly: true}
	for _, tc := range []struct {
		req  Request
		want []string
	}{
		{Request{Tool: "Bash", Command: "npm test -- --watch", Shell: "bash"}, []string{"Bash(npm test:*)"}},
		{Request{Tool: "Bash", Command: "npm test && git status", Shell: "bash"}, []string{"Bash(npm test:*)"}},
		{Request{Tool: "Bash", Command: "go test ./... && go vet ./...", Shell: "bash"}, []string{"Bash(go test:*)", "Bash(go vet:*)"}},
		{Request{Tool: "Bash", Command: "go test ./a && go test ./b", Shell: "bash"}, []string{"Bash(go test:*)"}},
		{Request{Tool: "bash", Command: "npm run dev", Shell: "bash"}, []string{"Bash(npm run dev:*)"}},
		{Request{Tool: "Bash", Command: "rm -rf build", Shell: "bash"}, nil},
		{Request{Tool: "Bash", Command: "npm test && del x", Shell: "cmd"}, nil},
		{Request{Tool: "Bash", Command: "Remove-Item x", Shell: "pwsh"}, nil},
		{Request{Tool: "Bash", Command: "sudo apt install x", Shell: "bash"}, nil},
		{Request{Tool: "Bash", Command: "FOO=1 go test", Shell: "bash"}, nil},
		{Request{Tool: "Bash", Command: "git status", Shell: "bash"}, nil},
		{Request{Tool: "Edit", Command: filepath.Join(root, "src", "a", "b.go")}, []string{"Edit(src/a/**)"}},
		{Request{Tool: "Write", Command: filepath.Join(root, "a.go")}, []string{"Edit(a.go)"}},
		{Request{Tool: "Delete", Command: filepath.Join(root, "a.go")}, nil},
		{Request{Tool: "computer_exec", Command: "call:x"}, nil},
		{Request{Tool: "mcp__x__y", Command: "{}"}, []string{"mcp__x__y"}},
		{Request{Tool: "ManagedProcess", Command: "npm run dev", Shell: "bash"}, []string{"ManagedProcess(npm run dev:*)"}},
	} {
		if got := Suggest(tc.req, opts); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Suggest(%+v) = %q, want %q", tc.req, got, tc.want)
		}
	}
	// Without read-only access (ACP), read-only commands need a rule of their own.
	strict := Options{ProjectRoot: root}
	if got := Suggest(Request{Tool: "Bash", Command: "npm test && git status", Shell: "bash"}, strict); !reflect.DeepEqual(got, []string{"Bash(npm test:*)", "Bash(git status:*)"}) {
		t.Errorf("strict suggestion = %q", got)
	}
	// cmd looks up programs in the working directory, so read-only words are not trusted.
	if got := Suggest(Request{Tool: "Bash", Command: "git status", Shell: "cmd"}, opts); !reflect.DeepEqual(got, []string{"Bash(git status:*)"}) {
		t.Errorf("cmd suggestion = %q", got)
	}
	// Interpreters are remembered as the exact command only.
	if got := Suggest(Request{Tool: "Bash", Command: "node build.js --prod", Shell: "bash"}, opts); !reflect.DeepEqual(got, []string{"Bash(node build.js --prod)"}) {
		t.Errorf("interpreter suggestion = %q", got)
	}
	outside := filepath.Join(t.TempDir(), "elsewhere", "x.go")
	got := Suggest(Request{Tool: "Edit", Command: outside}, opts)
	if len(got) != 1 || got[0] != "Edit("+filepath.ToSlash(outside)+")" {
		t.Fatalf("outside the project the suggestion is the exact file: %q", got)
	}
	// Every suggestion must cover the request it came from.
	for _, req := range []Request{
		{Tool: "Bash", Command: "go test ./... && go vet ./...", Shell: "bash"},
		{Tool: "Edit", Command: filepath.Join(root, "src", "a", "b.go")},
		{Tool: "Edit", Command: outside},
	} {
		set := allow(Suggest(req, opts)...)
		if d := Evaluate(req, Options{ProjectRoot: root, AllowReadOnly: true}, set); d.Effect != Allow {
			t.Errorf("suggestion for %+v does not cover it: %+v", req, d)
		}
	}
}

func TestCommandPrefix(t *testing.T) {
	for in, want := range map[string]string{
		"git checkout main":     "git checkout",
		"npm run dev -- --port": "npm run dev",
		"npm install lodash":    "npm install",
		"ls -la /tmp":           "ls",
		"docker compose up -d":  "docker compose up",
		"Git.exe Status":        "git Status",
		"somescript.sh --flag":  "somescript.sh",
	} {
		tokens, ok := SplitCommand(in, "bash")
		if !ok || len(tokens) != 1 {
			t.Fatalf("SplitCommand(%q) = %v %v", in, tokens, ok)
		}
		if got := joinTokens(CommandPrefix(tokens[0])); got != want {
			t.Errorf("CommandPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDefaultScope(t *testing.T) {
	for rule, want := range map[string]Scope{
		"Edit(sub/**)":        ScopeProject,
		"Edit(a.go)":          ScopeProject,
		"Edit(/tmp/a.txt)":    ScopeUser,
		"Edit(C:/work/a.txt)": ScopeUser,
		"Bash(go version:*)":  ScopeUser,
		"mcp__x__y":           ScopeUser,
		"Edit":                ScopeUser,
		"not a rule(":         ScopeUser,
	} {
		if got := DefaultScope(rule); got != want {
			t.Errorf("DefaultScope(%q) = %q, want %q", rule, got, want)
		}
	}
}
