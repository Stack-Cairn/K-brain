package permissions

import "testing"

// KB-3: read-only commands models use all the time, and reads that would hand
// secrets to the model.
func TestReadOnlyCommandAdditions(t *testing.T) {
	for _, c := range []struct {
		command, shell string
		want           bool
	}{
		{"hostname", "bash", true},
		{"hostname newname", "bash", false},
		{"whoami", "pwsh", true},
		{"whoami /all", "pwsh", true},
		{"date", "bash", true},
		{"date +%F", "bash", true},
		{"date -s 2020-01-01", "bash", false},
		{"date --set=2020-01-01", "bash", false},
		{"Get-Date", "pwsh", true},
		{"Get-Date -Format o", "pwsh", true},
		{"Get-Process", "pwsh", true},
		{"ps aux", "bash", true},
		{"ps -ef", "bash", true},
		{"ps e", "bash", false}, // BSD "e" prints each process environment
		{"ps -o env", "bash", false},
		{"Get-FileHash package.json", "pwsh", true},
		{"git --version", "pwsh", true},
		{"node --version", "pwsh", true},
		{"node --version --eval x", "pwsh", false},
		{"node -e x", "pwsh", false},
		{"./tool --version", "bash", false},
		{"New-Item --version", "pwsh", false}, // a cmdlet takes --version as a file name
		{"unknown-tool --version", "bash", false},
		{"git stash list", "bash", true},
		{"git stash show", "bash", true},
		{"git stash", "bash", false},
		{"git stash pop", "bash", false},
		{"git tag", "bash", true},
		{"git tag -l", "bash", true},
		{"git tag --list v1*", "bash", true},
		{"git tag v1", "bash", false},
		{"git tag -d v1", "bash", false},
		{"git config --get user.name", "pwsh", true},
		{"git config --get-all remote.origin.url", "pwsh", true},
		{"git config --list", "pwsh", true},
		{"git config -l", "pwsh", true},
		{"git config user.name", "pwsh", false},
		{"git config user.name x", "pwsh", false},
		{"git config --unset user.name", "pwsh", false},
		// git reads the config of the repository it runs in, which can name programs
		// to run (core.fsmonitor, diff.external): another directory is not read-only.
		{"git -C crates status --short", "pwsh", false},
		{"git -C a -C b log --oneline", "bash", false},
		{"git -C crates push", "bash", false},
		{"git -c core.pager=x status", "bash", false},
		{"cd crates; git status --short", "pwsh", false},
		{"cd crates && git status", "bash", false},
		{"git status && cd crates && ls", "bash", true},
		{"cd crates; ls; Get-Content README.md", "pwsh", true},
		{"Set-Location crates; ls", "pwsh", true},
		{"pushd crates && ls && popd", "bash", true},
		{"cd crates", "bash", true},
		{"cd crates && rm x", "bash", false},
		{"ls crates | Sort-Object", "pwsh", true},
		{"Get-Process | Select-Object -First 5", "pwsh", true},
		{"Get-Content README.md | Measure-Object -Line", "pwsh", true},
		{"Get-ChildItem | Format-Table", "pwsh", true},
		{"Get-Process | Format-List | Out-String", "pwsh", true},
		{"Get-Content C:\\Windows\\win.ini", "pwsh", true},
		{"cat D:/notes.txt", "bash", true},
		{"rg std::io src", "bash", true},
		{"rg os.environ src", "bash", true},
		{"cat environ", "bash", false},
		{"Select-String -Pattern Foo::Bar -Path a.rs", "pwsh", true},
	} {
		if got := IsReadOnlyCommand(c.command, c.shell); got != c.want {
			t.Errorf("IsReadOnlyCommand(%q, %s) = %v, want %v", c.command, c.shell, got, c.want)
		}
	}
}

func TestReadOnlyCommandsDoNotReadVariablesOrProviderDrives(t *testing.T) {
	for _, c := range []struct{ command, shell string }{
		{"echo $HOME", "bash"},
		{"echo $OPENAI_API_KEY", "bash"},
		{"cat ${HOME}/notes", "bash"},
		{"echo $env:OPENAI_API_KEY", "pwsh"},
		{"Get-ChildItem Env:", "pwsh"},
		{"gci env:", "pwsh"},
		{"Get-Content Env:PATH", "pwsh"},
		{"Get-Content -Path Env:PATH", "pwsh"},
		{"Get-Item HKLM:\\Software", "pwsh"},
		{"Get-ChildItem Cert:\\", "pwsh"},
		{"Test-Path Variable:x", "pwsh"},
		{"ls Function:", "pwsh"},
		{"Get-Content Registry::HKEY_CURRENT_USER\\Software", "pwsh"},
		{"Get-ChildItem Alias:", "pwsh"},
		{"Get-Content -Path:Env:X", "pwsh"},
		{"Get-Content Environment::X", "pwsh"},
		{`Get-Content Microsoft.PowerShell.Core\Variable::x`, "pwsh"},
		{"cat /proc/self/environ", "bash"},
		{"git stash show -p --output=x", "bash"},
		{"git stash list --ext-diff", "bash"},
	} {
		if IsReadOnlyCommand(c.command, c.shell) {
			t.Errorf("IsReadOnlyCommand(%q, %s) = true, want false: it can hand secrets to the model", c.command, c.shell)
		}
	}
}

// Polls read the output of a process or terminal the session started; they carry no
// command to judge, so the tool says so.
func TestEvaluateOwnedOutputPoll(t *testing.T) {
	poll := Request{Tool: "ProcessWait", Command: "bash-1", Poll: true}
	if d := Evaluate(poll, Options{AllowReadOnly: true}); d.Effect != Allow || d.Source != SourceReadOnly {
		t.Fatalf("poll with read-only access = %+v, want read-only allow", d)
	}
	if d := Evaluate(poll, Options{}); d.Effect != "" {
		t.Fatalf("poll without read-only access = %+v, want ask", d)
	}
	for _, tool := range []string{"ManagedProcess", "ReadTerminal", "ProcessWait"} {
		req := Request{Tool: tool, Command: "id-1", Poll: true}
		if d := Evaluate(req, Options{AllowReadOnly: true}); d.Effect != Allow {
			t.Fatalf("%s poll = %+v, want allow", tool, d)
		}
		deny := RuleSet{Scope: ScopeUser, Rules: []StoredRule{{Rule: tool, Effect: Deny}}}
		if d := Evaluate(req, Options{AllowReadOnly: true}, deny); d.Effect != Deny {
			t.Fatalf("%s poll with a deny rule = %+v, want deny", tool, d)
		}
	}
	// The same tool without the poll flag (start, stop, input) still asks.
	if d := Evaluate(Request{Tool: "ManagedProcess", Command: "mp-1"}, Options{AllowReadOnly: true}); d.Effect != "" {
		t.Fatalf("non-poll ManagedProcess = %+v, want ask", d)
	}
	if rules := Suggest(poll, Options{AllowReadOnly: true}); rules != nil {
		t.Fatalf("Suggest(poll) = %v, want nothing to remember", rules)
	}
}

func TestReadOnlyAfterCdAndProcEnviron(t *testing.T) {
	for _, c := range []struct {
		command, shell string
		want           bool
	}{
		{"cd crates && node --version", "bash", false}, // toolchains pick a binary per directory
		{"node --version", "bash", true},
		{"cat /proc/self/env*", "bash", false},
		{"cat /proc/self/e?viron", "bash", false},
		{"cd /proc/self && cat environ", "bash", false},
		{"cd / && cat proc/self/environ", "bash", false},
		{"grep -a KEY /proc/self/env*", "bash", false},
		{"cat /proc/cpuinfo", "bash", true},
		{"ps -E", "bash", false},
		{"ps -axE", "bash", false},
		{"ps -ef", "bash", true},
	} {
		if got := IsReadOnlyCommand(c.command, c.shell); got != c.want {
			t.Errorf("IsReadOnlyCommand(%q, %s) = %v, want %v", c.command, c.shell, got, c.want)
		}
	}
}

// A rule cannot say which directory a cd went to, so a command that is read-only only
// where it is not after a cd is allowed once, never remembered.
func TestSuggestDoesNotRememberGitAfterCd(t *testing.T) {
	for _, command := range []string{"cd vendor-a && git status", "cd vendor-a && npm test && git log", "cd x && node --version"} {
		if rules := Suggest(Request{Tool: "Bash", Command: command, Shell: "bash"}, Options{AllowReadOnly: true}); rules != nil {
			t.Errorf("Suggest(%q) = %v, want nothing to remember", command, rules)
		}
	}
	if rules := Suggest(Request{Tool: "Bash", Command: "cd crates && npm test", Shell: "bash"}, Options{AllowReadOnly: true}); len(rules) != 1 || rules[0] != "Bash(npm test:*)" {
		t.Errorf("Suggest(cd && npm test) = %v, want [Bash(npm test:*)]", rules)
	}
}

// A cwd argument runs the command elsewhere, as a cd in front of it would.
func TestEvaluateOtherDir(t *testing.T) {
	for _, c := range []struct {
		command string
		want    Effect
	}{
		{"git status --short", ""},
		{"node --version", ""},
		{"ls", Allow},
		{"cat README.md", Allow},
	} {
		d := Evaluate(Request{Tool: "Bash", Command: c.command, Shell: "bash", OtherDir: true}, Options{AllowReadOnly: true})
		if d.Effect != c.want {
			t.Errorf("Evaluate(%q in another dir) = %+v, want %q", c.command, d, c.want)
		}
	}
	if rules := Suggest(Request{Tool: "Bash", Command: "git status", Shell: "bash", OtherDir: true}, Options{AllowReadOnly: true}); rules != nil {
		t.Errorf("Suggest(git status in another dir) = %v, want nothing to remember", rules)
	}
}
