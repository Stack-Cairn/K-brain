package permissions

import (
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

// Request is one gated tool call. Command is the command line for shell tools and
// the target path for file tools; Shell names the shell that will run the command,
// and an empty Shell parses strictly.
type Request struct {
	Tool    string
	Command string
	Shell   string
}

type Options struct {
	// ProjectRoot resolves relative path rules and relative request paths.
	ProjectRoot string
	// AllowReadOnly lets read-only tools and commands through without a rule.
	AllowReadOnly bool
}

// Decision is the outcome of Evaluate. An empty Effect means no rule decided and the
// caller should ask.
type Decision struct {
	Effect Effect
	Source Source
	// Rule lists the rules that decided, comma separated when several segments of
	// one command matched different rules.
	Rule  string
	Scope Scope
}

type compiled struct {
	rule   Rule
	effect Effect
	scope  Scope
}

func compile(sets []RuleSet, tool string) []compiled {
	fam := family(tool)
	var out []compiled
	for _, set := range sets {
		for _, stored := range set.Rules {
			if stored.Effect != Allow && stored.Effect != Deny {
				continue
			}
			r, err := ParseRule(stored.Rule)
			if err != nil || r.Tool != fam {
				continue
			}
			out = append(out, compiled{rule: r, effect: stored.Effect, scope: set.Scope})
		}
	}
	return out
}

// Evaluate decides a request from the rule sets. Deny rules win over allow rules
// and over read-only access in every scope. A shell command is allowed only when
// each of its simple commands is covered, by a rule or, with AllowReadOnly, by being
// read-only; a command that cannot be split safely is never decided by a rule.
func Evaluate(req Request, opts Options, sets ...RuleSet) Decision {
	rules := compile(sets, req.Tool)
	switch kindOfTool(req.Tool) {
	case kindShell:
		return evaluateShell(req, opts, rules)
	case kindPath:
		target := resolvePath(req.Command, opts.ProjectRoot)
		match := func(r Rule) bool { return r.Pattern == "" || matchPath(r.Pattern, target, opts.ProjectRoot) }
		if d, ok := first(rules, Deny, match); ok {
			return d
		}
		if d, ok := first(rules, Allow, match); ok {
			return d
		}
	default:
		match := func(r Rule) bool { return r.matchesOther(req.Command) }
		if d, ok := first(rules, Deny, match); ok {
			return d
		}
		if d, ok := first(rules, Allow, match); ok {
			return d
		}
	}
	if opts.AllowReadOnly && IsReadOnlyTool(req.Tool) {
		return Decision{Effect: Allow, Source: SourceReadOnly}
	}
	return Decision{}
}

func first(rules []compiled, effect Effect, match func(Rule) bool) (Decision, bool) {
	for _, c := range rules {
		if c.effect == effect && match(c.rule) {
			return Decision{Effect: effect, Source: SourceRule, Rule: c.rule.String(), Scope: c.scope}, true
		}
	}
	return Decision{}, false
}

// readOnlyShell reports shells in which a read-only command word names the program
// on PATH. cmd.exe looks in the working directory first, so a `git.bat` the agent
// just wrote would run as `git status`; on Windows an unknown shell may be cmd.
func readOnlyShell(shell string) bool {
	switch shellKindOf(shell) {
	case shellPosix, shellPwsh:
		return true
	case shellUnknown:
		return runtime.GOOS != "windows"
	}
	return false
}

func evaluateShell(req Request, opts Options, rules []compiled) Decision {
	segments, ok := SplitCommand(req.Command, req.Shell)
	if !ok {
		return evaluateUnsplittable(req.Command, rules)
	}
	for _, seg := range segments {
		if d, ok := first(rules, Deny, func(r Rule) bool { return r.deniesSegment(seg) }); ok {
			return d
		}
	}
	readOnly := opts.AllowReadOnly && readOnlyShell(req.Shell)
	var matched []string
	var scope Scope
	for _, seg := range segments {
		d, ok := first(rules, Allow, func(r Rule) bool { return r.matchesSegment(seg) })
		switch {
		case ok:
			if scope == "" {
				scope = d.Scope
			}
			if !contains(matched, d.Rule) {
				matched = append(matched, d.Rule)
			}
		case readOnly && readOnlySegment(seg):
		default:
			return Decision{}
		}
	}
	if len(matched) == 0 {
		return Decision{Effect: Allow, Source: SourceReadOnly}
	}
	return Decision{Effect: Allow, Source: SourceRule, Rule: strings.Join(matched, ", "), Scope: scope}
}

// evaluateUnsplittable handles a command SplitCommand rejected. Allow rules never
// apply. Deny rules are matched loosely: the command is cut at every character that
// could separate commands, quotes are dropped, and a deny rule matches its words
// anywhere in a piece, so `env git push > log` still hits `Bash(git push:*)`. When the
// tool has deny rules that do not match, the caller must ask rather than fall back to
// an allowing mode, since the command may still hide a denied one.
func evaluateUnsplittable(command string, rules []compiled) Decision {
	hasDeny := false
	for _, c := range rules {
		if c.effect != Deny {
			continue
		}
		hasDeny = true
		for _, piece := range looseSegments(command) {
			if c.rule.deniesAnywhere(piece) {
				return Decision{Effect: Deny, Source: SourceRule, Rule: c.rule.String(), Scope: c.scope}
			}
		}
	}
	if hasDeny {
		return Decision{Effect: Ask}
	}
	return Decision{}
}

func looseSegments(command string) [][]string {
	var out [][]string
	pieces := strings.FieldsFunc(command, func(r rune) bool {
		return strings.ContainsRune(";&|\n\r(){}<>`", r)
	})
	for _, piece := range pieces {
		words := strings.FieldsFunc(piece, func(r rune) bool {
			return unicode.IsSpace(r) || strings.ContainsRune("'\"^‘’‚‛“”„", r)
		})
		for i, w := range words {
			words[i] = strings.TrimLeft(w, "$@\\")
		}
		if len(words) > 0 {
			out = append(out, words)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// resolvePath makes a request path absolute against the project root. It returns ""
// when that is impossible, which only whole-tool rules can match.
func resolvePath(p, root string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		if root == "" {
			return ""
		}
		p = filepath.Join(root, p)
	}
	return filepath.Clean(p)
}

func foldPath(p string) string {
	p = filepath.ToSlash(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

// matchPath matches a slash-separated glob against an absolute path. Relative
// patterns are relative to the project root and never match outside it. `**` matches
// any number of directories; a trailing `**` needs at least one more path element.
func matchPath(pattern, target, root string) bool {
	if target == "" {
		return false
	}
	var subject string
	if path.IsAbs(pattern) || filepath.IsAbs(filepath.FromSlash(pattern)) {
		subject = foldPath(target)
	} else {
		if root == "" {
			return false
		}
		rel, err := filepath.Rel(filepath.Clean(root), target)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false
		}
		subject = foldPath(rel)
	}
	// The pattern is already slash separated; ToSlash would turn its `\[` escapes
	// into separators on Windows.
	if runtime.GOOS == "windows" {
		pattern = strings.ToLower(pattern)
	}
	return globMatch(strings.Split(pattern, "/"), strings.Split(subject, "/"))
}

func globMatch(pattern, parts []string) bool {
	if len(pattern) == 0 {
		return len(parts) == 0
	}
	if pattern[0] == "**" {
		if len(pattern) == 1 {
			return len(parts) > 0
		}
		for i := 0; i <= len(parts); i++ {
			if globMatch(pattern[1:], parts[i:]) {
				return true
			}
		}
		return false
	}
	if len(parts) == 0 {
		return false
	}
	ok, err := path.Match(pattern[0], parts[0])
	return err == nil && ok && globMatch(pattern[1:], parts[1:])
}
