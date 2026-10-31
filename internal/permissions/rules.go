package permissions

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/commandline"
)

// Effect is what a matching rule does to a gated tool call.
type Effect string

const (
	Allow Effect = "allow"
	Deny  Effect = "deny"
	// Ask is never stored. Evaluate returns it when a command cannot be checked
	// against the deny rules that exist for its tool, so that the caller asks even
	// in a mode that would otherwise allow it.
	Ask Effect = "ask"
)

// Scope says where a rule is stored and how far it reaches.
type Scope string

const (
	ScopeSession Scope = "session"
	ScopeProject Scope = "project"
	ScopeUser    Scope = "user"
)

// Source explains why Evaluate allowed a call without asking.
type Source string

const (
	SourceRule     Source = "rule"
	SourceReadOnly Source = "read_only"
)

// Rule is a parsed permission rule, written like Claude Code's:
//
//	Bash(npm test:*)   commands starting with `npm test`
//	Bash(git status)   exactly `git status`
//	Edit(src/**)       edits and writes under src/ of the project root
//	mcp__srv__tool     every call of that tool
type Rule struct {
	Tool    string
	Pattern string
	Prefix  bool
	tokens  []string
}

var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.\-]+$`)

type toolKind int

const (
	kindOther toolKind = iota
	kindShell
	kindPath
)

// family maps tool names that share rules onto one rule name: the TUI's lowercase
// tools and LiveAgent's PascalCase tools, and Write onto Edit.
func family(tool string) string {
	switch tool {
	case "bash", "Bash":
		return "Bash"
	case "write", "edit", "Write", "Edit":
		return "Edit"
	case "read", "Read":
		return "Read"
	}
	return tool
}

func kindOfTool(tool string) toolKind {
	switch family(tool) {
	case "Bash", "ManagedProcess", "TerminalSession":
		return kindShell
	case "Edit", "Read", "Image", "Delete", "List", "Glob", "Grep":
		return kindPath
	}
	return kindOther
}

// ParseRule parses and normalizes a rule string.
func ParseRule(text string) (Rule, error) {
	text = strings.TrimSpace(text)
	tool, inner, hasPattern := text, "", false
	if open := strings.IndexByte(text, '('); open >= 0 {
		if !strings.HasSuffix(text, ")") {
			return Rule{}, fmt.Errorf("permission rule %q: missing closing parenthesis", text)
		}
		tool, inner, hasPattern = strings.TrimSpace(text[:open]), strings.TrimSpace(text[open+1:len(text)-1]), true
	}
	if !toolNamePattern.MatchString(tool) {
		return Rule{}, fmt.Errorf("permission rule %q: invalid tool name", text)
	}
	r := Rule{Tool: family(tool)}
	if !hasPattern {
		return r, nil
	}
	if strings.HasSuffix(inner, ":*") {
		r.Prefix = true
		inner = strings.TrimSpace(strings.TrimSuffix(inner, ":*"))
	}
	if inner == "" {
		return Rule{}, fmt.Errorf("permission rule %q: empty pattern", text)
	}
	switch kindOfTool(r.Tool) {
	case kindShell:
		tokens, err := commandline.Split(inner)
		if err != nil || len(tokens) == 0 {
			return Rule{}, fmt.Errorf("permission rule %q: invalid command pattern", text)
		}
		tokens[0] = normCommand(tokens[0])
		r.tokens = tokens
		r.Pattern = joinTokens(tokens)
	case kindPath:
		if r.Prefix {
			return Rule{}, fmt.Errorf("permission rule %q: path patterns use globs, not :*", text)
		}
		r.Pattern = slashPath(inner)
	default:
		r.Pattern = inner
	}
	return r, nil
}

func (r Rule) String() string {
	if r.Pattern == "" {
		return r.Tool
	}
	if r.Prefix {
		return r.Tool + "(" + r.Pattern + ":*)"
	}
	return r.Tool + "(" + r.Pattern + ")"
}

// matchesSegment reports whether a shell rule covers one command segment.
func (r Rule) matchesSegment(segment []string) bool {
	if len(r.tokens) == 0 {
		return r.Pattern == ""
	}
	if len(segment) < len(r.tokens) || (!r.Prefix && len(segment) != len(r.tokens)) {
		return false
	}
	if normCommand(segment[0]) != r.tokens[0] {
		return false
	}
	for i := 1; i < len(r.tokens); i++ {
		if segment[i] != r.tokens[i] {
			return false
		}
	}
	return true
}

// deniesSegment is the looser match used for deny rules: the program may be named by
// a path or with .exe, options may sit between its words (`git -C dir push`), and
// exact rules count as prefixes. Over-matching here only causes a prompt or a
// refusal, never an unwanted run.
func (r Rule) deniesSegment(segment []string) bool {
	if len(r.tokens) == 0 {
		return r.Pattern == ""
	}
	if len(segment) == 0 || programName(segment[0]) != r.tokens[0] {
		return false
	}
	next := 1
	for _, word := range segment[1:] {
		if next < len(r.tokens) && word == r.tokens[next] {
			next++
		}
	}
	return next == len(r.tokens)
}

// deniesAnywhere applies deniesSegment from every word of words. It is used for
// commands that cannot be split safely, whose pieces may start with a wrapper.
func (r Rule) deniesAnywhere(words []string) bool {
	for start := range words {
		if r.deniesSegment(words[start:]) {
			return true
		}
	}
	return false
}

func programName(word string) string {
	if i := strings.LastIndexAny(word, `/\`); i >= 0 {
		word = word[i+1:]
	}
	return normCommand(word)
}

func (r Rule) matchesOther(command string) bool {
	if r.Pattern == "" {
		return true
	}
	command = strings.TrimSpace(command)
	if r.Prefix {
		return strings.HasPrefix(command, r.Pattern)
	}
	return command == r.Pattern
}

var drivePath = regexp.MustCompile(`^[A-Za-z]:/`)

// DefaultScope is where "allow and remember" stores a rule. A relative path rule such
// as Edit(sub/**) names a folder of the project it was approved in, so it stays with
// that project; command, tool and absolute path rules apply in every project.
func DefaultScope(text string) Scope {
	r, err := ParseRule(text)
	if err != nil || kindOfTool(r.Tool) != kindPath || r.Pattern == "" {
		return ScopeUser
	}
	if strings.HasPrefix(r.Pattern, "/") || drivePath.MatchString(r.Pattern) {
		return ScopeUser
	}
	return ScopeProject
}

// slashPath turns Windows separators into slashes but keeps a backslash that escapes
// a glob character, as Suggest writes for paths such as Next.js `[id]` routes.
func slashPath(p string) string {
	var b strings.Builder
	runes := []rune(p)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\\' {
			if i+1 < len(runes) && strings.ContainsRune("*?[]", runes[i+1]) {
				b.WriteRune('\\')
				b.WriteRune(runes[i+1])
				i++
				continue
			}
			b.WriteRune('/')
			continue
		}
		b.WriteRune(runes[i])
	}
	return b.String()
}

// normCommand normalizes the command word: case-insensitive and without .exe, since
// Windows resolves `Git.exe` and `git` to the same program.
func normCommand(word string) string {
	word = strings.ToLower(word)
	return strings.TrimSuffix(word, ".exe")
}

func joinTokens(tokens []string) string {
	parts := make([]string, len(tokens))
	for i, t := range tokens {
		switch {
		case t != "" && !strings.ContainsAny(t, " \t\n'\"\\"):
			parts[i] = t
		case !strings.Contains(t, "'") && !strings.Contains(t, `\`):
			parts[i] = "'" + t + "'"
		default:
			parts[i] = `"` + strings.ReplaceAll(t, `"`, `\"`) + `"`
		}
	}
	return strings.Join(parts, " ")
}

var arity = map[string]int{
	"ls": 1, "cat": 1, "pwd": 1, "grep": 1, "find": 1, "echo": 1,
	"rm": 1, "mv": 1, "cp": 1, "mkdir": 1, "touch": 1, "which": 1,

	"git": 2, "npm": 2, "pnpm": 2, "yarn": 2, "go": 2, "cargo": 2,
	"docker": 2, "kubectl": 2, "brew": 2, "apt": 2, "pip": 2,

	"npm run": 3, "pnpm run": 3, "go tool": 3, "docker compose": 3, "git submodule": 3,
}

// CommandPrefix returns the leading words of a command that identify what it does,
// e.g. `git checkout` for `git checkout main` and `npm run dev` for `npm run dev --x`.
func CommandPrefix(tokens []string) []string {
	if len(tokens) == 0 {
		return nil
	}
	norm := append([]string{normCommand(tokens[0])}, tokens[1:]...)
	for n := len(norm); n > 0; n-- {
		if a, ok := arity[strings.Join(norm[:n], " ")]; ok {
			return norm[:min(a, len(norm))]
		}
	}
	return norm[:1]
}
