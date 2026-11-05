package permissions

import "strings"

// IsReadOnlyTool reports tools that only read inside the run's authorized roots; the
// root policy, not this list, keeps them from reading elsewhere.
func IsReadOnlyTool(tool string) bool {
	switch tool {
	case "Read", "read", "Image", "List", "Glob", "Grep", "list", "glob", "grep", "lsp":
		return true
	}
	return false
}

// IsInteractionTool reports tools whose only effect is to put a question to the user
// and return the user's own answer: AskUserQuestion (backend) and question (TUI).
// They touch no file, process, network or credential, so an AI review of the call
// has nothing to judge and its output is the user's words, not untrusted content.
// Gates allow them in every permission mode; deny rules and explicit per-tool
// policies still apply. ExitPlanMode is not one: approving a plan changes the mode
// the next turn runs in.
func IsInteractionTool(tool string) bool {
	return tool == "AskUserQuestion" || tool == "question"
}

// IsReadOnlyCommand reports a shell command whose every simple command only reads.
// The list is an allow list on purpose: unknown commands and aliases are not read-only.
func IsReadOnlyCommand(command, shell string) bool {
	segments, ok := SplitCommand(command, shell)
	if !ok {
		return false
	}
	for _, readOnly := range readOnlySegments(segments, false) {
		if !readOnly {
			return false
		}
	}
	return true
}

// readOnlySegments reports, for each simple command of one command line, whether it
// only reads. It judges the sequence, not each command alone: git reads the
// repository config where it runs, and settings such as core.fsmonitor or
// diff.external run programs, so git after a cd may run a program from a repository
// the user did not set up (the same reason git -C is not read-only).
// moved says the command starts outside the session's working directory.
func readOnlySegments(segments [][]string, moved bool) []bool {
	out := make([]bool, len(segments))
	lost := false
	for i, seg := range segments {
		out[i] = readOnlySegment(seg) && !lost && !(moved && readsDirectoryConfig(seg))
		if changesDirectory(normCommand(seg[0])) {
			moved = true
			// After a cd into /proc (a process's environ is there) or to a directory
			// named by a glob, no later word says where it reads from.
			for _, arg := range seg[1:] {
				lower := strings.ToLower(arg)
				if strings.Contains(lower, "proc") || strings.ContainsAny(lower, "*?[") {
					lost = true
				}
			}
		}
	}
	return out
}

// movedReadOnly reports a command that is read-only on its own but not after the cd
// in front of it, such as git status in another repository.
func movedReadOnly(segments [][]string, moved bool) bool {
	for i, readOnly := range readOnlySegments(segments, moved) {
		if !readOnly && readOnlySegment(segments[i]) {
			return true
		}
	}
	return false
}

// readsDirectoryConfig reports commands that read configuration from the directory
// they run in and can be told to run a program by it: git (core.fsmonitor,
// diff.external), and version checks of toolchains that pick a binary per directory
// (rust-toolchain.toml, .yarnrc yarn-path).
func readsDirectoryConfig(seg []string) bool {
	name := normCommand(seg[0])
	return name == "git" || (len(seg) == 2 && seg[1] == "--version")
}

func changesDirectory(name string) bool {
	switch name {
	case "cd", "chdir", "set-location", "sl", "pushd", "popd", "push-location", "pop-location":
		return true
	}
	return false
}

func anyArg(args []string, bad func(string) bool) bool {
	for _, a := range args {
		if bad(a) {
			return true
		}
	}
	return false
}

func onlyArgs(args []string, allowed ...string) bool {
	for _, a := range args {
		ok := false
		for _, want := range allowed {
			if a == want {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func readOnlySegment(seg []string) bool {
	if strings.ContainsAny(seg[0], `/\`) {
		return false // a path can name any program
	}
	args := seg[1:]
	// Variables and PowerShell's non-file drives hold secrets such as API keys; reading
	// them is not something to hand to the model without asking.
	for _, word := range seg {
		if strings.Contains(word, "$") || providerPath(word) {
			return false
		}
	}
	if len(args) == 1 && args[0] == "--version" && versionPrograms[normCommand(seg[0])] {
		return true
	}
	switch normCommand(seg[0]) {
	case "ls", "dir", "pwd", "cat", "head", "tail", "wc", "grep", "which", "where", "echo", "type",
		"get-childitem", "gci", "get-content", "gc", "select-string", "sls", "get-location", "gl",
		"test-path", "get-item", "gi", "resolve-path", "rvpa",
		"whoami", "get-date", "get-process", "gps", "get-filehash",
		// Changing directory affects only the later segments, which are judged themselves.
		"cd", "chdir", "set-location", "sl", "pushd", "popd", "push-location", "pop-location",
		// PowerShell pipeline stages that only pick, sort, count or format objects.
		"select-object", "sort-object", "measure-object", "format-table", "ft", "format-list", "fl",
		"format-wide", "fw", "out-string":
		return true
	case "hostname":
		return len(args) == 0 // an argument sets the host name
	case "ps":
		// BSD-style "e" (no dash) and -E print each process's environment, API keys
		// included.
		return !anyArg(args, func(a string) bool {
			if strings.Contains(strings.ToLower(a), "env") {
				return true
			}
			if strings.HasPrefix(a, "-") {
				return !strings.HasPrefix(a, "--") && strings.ContainsRune(a, 'E')
			}
			return strings.ContainsRune(a, 'e')
		})
	case "date":
		return len(args) == 0 || (len(args) == 1 && strings.HasPrefix(args[0], "+"))
	case "tree":
		return !anyArg(args, func(a string) bool {
			return strings.HasPrefix(a, "--output") || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "o"))
		})
	case "rg":
		return !anyArg(args, func(a string) bool {
			return strings.HasPrefix(a, "--pre") || strings.HasPrefix(a, "--hostname-bin")
		})
	case "find":
		return !anyArg(args, func(a string) bool {
			switch a {
			case "-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls":
				return true
			}
			return false
		})
	case "git":
		return readOnlyGit(args)
	}
	return false
}

func readOnlyGit(args []string) bool {
	for len(args) > 0 && args[0] == "--no-pager" {
		args = args[1:]
	}
	if len(args) == 1 && args[0] == "--version" {
		return true
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return false // global options such as -c and -C change what runs
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "status", "blame", "rev-parse", "ls-files":
		return true
	case "diff", "log", "show":
		return !anyArg(rest, writesOrRunsDiff)
	case "branch":
		return onlyArgs(rest, "-v", "-vv", "-a", "-r", "--list", "--all", "--remotes", "--verbose", "--show-current", "--no-color", "--color")
	case "remote":
		return onlyArgs(rest, "-v", "--verbose")
	case "stash":
		return len(rest) > 0 && (rest[0] == "list" || rest[0] == "show") && !anyArg(rest[1:], writesOrRunsDiff)
	case "tag":
		return len(rest) == 0 || rest[0] == "-l" || rest[0] == "--list" // a name creates a tag
	case "config":
		if len(rest) == 0 {
			return false
		}
		switch rest[0] {
		case "--get", "--get-all", "--get-regexp", "--list", "-l":
			return true
		}
	}
	return false
}

// providerNames are PowerShell providers that are not file systems, as they appear in
// provider-qualified paths (Environment::X, Microsoft.PowerShell.Core\Registry::HKCU).
var providerNames = []string{"environment::", "variable::", "function::", "alias::", "certificate::", "registry::", "wsman::"}

// providerDrives are PowerShell drives that are not file systems.
var providerDrives = []string{"env:", "variable:", "function:", "alias:", "cert:", "hklm:", "hkcu:", "wsman:"}

// providerPath reports a word that names a PowerShell provider path such as Env:PATH,
// -Path:Env:X or Microsoft.PowerShell.Core\Environment::X, or a /proc environ file;
// ordinary drive letters (C:) are file paths.
func providerPath(word string) bool {
	lower := strings.ToLower(word)
	// A process environment: /proc/<pid>/environ named outright, through a glob
	// (/proc/self/env*, /pr?c/self/...) or through /dev/fd.
	if environPath(lower) || strings.Contains(lower, "/dev/fd") || globbedRoot(lower) ||
		(strings.Contains(lower, "proc") && strings.ContainsAny(lower, "*?[")) {
		return true
	}
	// Provider-qualified paths name the provider before "::"; other "::" (std::io in a
	// search pattern) is not a path.
	for _, provider := range providerNames {
		if strings.Contains(lower, provider) {
			return true
		}
	}
	// -Path:Env:X binds the value after the parameter name's colon.
	if strings.HasPrefix(lower, "-") {
		if i := strings.IndexByte(lower, ':'); i > 0 {
			lower = lower[i+1:]
		}
	}
	for _, drive := range providerDrives {
		if strings.HasPrefix(lower, drive) {
			return true
		}
	}
	return false
}

// environPath reports a path to an environ file; os.environ in a search pattern is
// not one. A bare "environ" after a cd is caught by the cd rules.
func environPath(word string) bool {
	return word == "environ" || (strings.Contains(word, "environ") && strings.ContainsAny(word, `/\`))
}

// globbedRoot reports a path whose first directory is a glob, such as /pr?c/self or
// p*/self: the shell picks the directory, so the word does not say what is read.
func globbedRoot(word string) bool {
	trimmed := strings.TrimLeft(word, "/")
	slash := strings.IndexByte(trimmed, '/')
	return slash > 0 && strings.ContainsAny(trimmed[:slash], "*?[")
}

// writesOrRunsDiff reports diff and log options that write a file or run a program.
func writesOrRunsDiff(a string) bool {
	return a == "--output" || strings.HasPrefix(a, "--output=") || a == "--ext-diff"
}

// versionPrograms print their version for a lone --version. The list is explicit:
// a PowerShell cmdlet binds --version as a positional argument (New-Item --version
// creates a file of that name).
var versionPrograms = map[string]bool{
	"git": true, "node": true, "npm": true, "npx": true, "pnpm": true, "yarn": true, "bun": true,
	"deno": true, "go": true, "python": true, "python3": true, "py": true, "pip": true, "pip3": true,
	"cargo": true, "rustc": true, "rustup": true, "java": true, "javac": true, "dotnet": true,
	"docker": true, "kubectl": true, "gh": true, "mise": true, "ruby": true, "gem": true,
	"perl": true, "php": true, "gcc": true, "clang": true, "make": true, "cmake": true,
	"tsc": true, "uv": true, "pwsh": true, "bash": true, "code": true, "rg": true,
}
