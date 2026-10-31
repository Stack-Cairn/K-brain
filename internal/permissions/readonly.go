package permissions

import "strings"

// IsReadOnlyTool reports tools that only read inside the run's authorized roots; the
// root policy, not this list, keeps them from reading elsewhere.
func IsReadOnlyTool(tool string) bool {
	switch tool {
	case "Read", "read", "Image", "List", "Glob", "Grep":
		return true
	}
	return false
}

// IsReadOnlyCommand reports a shell command whose every simple command only reads.
// The list is an allow list on purpose: unknown commands and aliases are not read-only.
func IsReadOnlyCommand(command, shell string) bool {
	segments, ok := SplitCommand(command, shell)
	if !ok {
		return false
	}
	for _, seg := range segments {
		if !readOnlySegment(seg) {
			return false
		}
	}
	return true
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
	switch normCommand(seg[0]) {
	case "ls", "dir", "pwd", "cat", "head", "tail", "wc", "grep", "which", "where", "echo", "type",
		"get-childitem", "gci", "get-content", "gc", "select-string", "sls", "get-location", "gl",
		"test-path", "get-item", "gi", "resolve-path", "rvpa":
		return true
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
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return false // global options such as -c and -C change what runs
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "status", "blame", "rev-parse", "ls-files":
		return true
	case "diff", "log", "show":
		return !anyArg(rest, func(a string) bool {
			return a == "--output" || strings.HasPrefix(a, "--output=") || a == "--ext-diff"
		})
	case "branch":
		return onlyArgs(rest, "-v", "-vv", "-a", "-r", "--list", "--all", "--remotes", "--verbose", "--show-current", "--no-color", "--color")
	case "remote":
		return onlyArgs(rest, "-v", "--verbose")
	}
	return false
}
