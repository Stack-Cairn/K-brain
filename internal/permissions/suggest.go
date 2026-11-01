package permissions

import (
	"path/filepath"
	"strings"
)

// destructive commands are never offered as remembered rules: approving one removal
// must not approve every later one.
var destructive = map[string]bool{
	"rm": true, "rmdir": true, "del": true, "erase": true, "rd": true, "remove-item": true,
	"ri": true, "mkfs": true, "dd": true, "format": true, "shutdown": true, "reboot": true,
}

// interpreters run whatever code their arguments or stdin give them, so a prefix
// rule such as `Bash(node:*)` would allow `node -e <anything>`. They are remembered
// only as the exact command.
var interpreters = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true,
	"pwsh": true, "powershell": true, "powershell7": true, "cmd": true, "wsl": true,
	"node": true, "deno": true, "bun": true, "python": true, "python3": true, "py": true,
	"ruby": true, "perl": true, "php": true, "lua": true, "osascript": true,
	"cscript": true, "wscript": true, "mshta": true, "rundll32": true, "regsvr32": true,
}

// Suggest returns the rules that "allow and remember" would store for a request, or
// nil when it should only be allowed once. Shell commands get one prefix rule per
// simple command (read-only ones are skipped when opts lets them through anyway);
// edits get their directory.
func Suggest(req Request, opts Options) []string {
	if req.Poll {
		return nil // a poll is read-only; there is nothing to remember
	}
	switch kindOfTool(req.Tool) {
	case kindShell:
		// ManagedProcess and TerminalSession also gate actions such as status, stop
		// or terminal input whose Command is not a command line; only Bash is.
		if family(req.Tool) != "Bash" {
			return nil
		}
		segments, ok := SplitCommand(req.Command, req.Shell)
		if !ok {
			return nil
		}
		// A rule matches a command, not the directory a cd put it in: remembering the
		// git status after a cd would allow git status after any cd, in every project.
		if movedReadOnly(segments, req.OtherDir) {
			return nil
		}
		readOnly := opts.AllowReadOnly && readOnlyShell(req.Shell)
		readOnlyAt := readOnlySegments(segments, req.OtherDir)
		var out []string
		for i, seg := range segments {
			if readOnly && readOnlyAt[i] {
				continue
			}
			prefix := CommandPrefix(seg)
			if destructive[prefix[0]] || strings.ContainsAny(prefix[0], `/\`) {
				return nil
			}
			rule := Rule{Tool: family(req.Tool), tokens: prefix, Pattern: joinTokens(prefix), Prefix: true}
			if interpreters[prefix[0]] {
				exact := append([]string{prefix[0]}, seg[1:]...)
				rule = Rule{Tool: rule.Tool, tokens: exact, Pattern: joinTokens(exact)}
			}
			r := rule.String()
			if parsed, err := ParseRule(r); err != nil || parsed.String() != r {
				return nil // a word joinTokens cannot quote back unchanged
			}
			if !contains(out, r) {
				out = append(out, r)
			}
		}
		return out
	case kindPath:
		if family(req.Tool) != "Edit" {
			return nil
		}
		target := resolvePath(req.Command, opts.ProjectRoot)
		if target == "" {
			return nil
		}
		if opts.ProjectRoot != "" {
			if rel, err := filepath.Rel(filepath.Clean(opts.ProjectRoot), target); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				rel = filepath.ToSlash(rel)
				if dir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel))); dir != "." {
					return []string{"Edit(" + escapeGlob(dir) + "/**)"}
				}
				return []string{"Edit(" + escapeGlob(rel) + ")"}
			}
		}
		return []string{"Edit(" + escapeGlob(filepath.ToSlash(target)) + ")"}
	}
	// Whole-tool rules only for MCP tools: built-in tools gate actions of very
	// different weight under one name (cron create vs delete, process stop ids).
	if strings.HasPrefix(req.Tool, "mcp__") {
		return []string{req.Tool}
	}
	return nil
}

// escapeGlob keeps literal path characters such as Next.js `[id]` from acting as globs.
func escapeGlob(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r == '*' || r == '?' || r == '[' || r == ']' {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
