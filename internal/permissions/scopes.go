package permissions

import (
	"path/filepath"
	"slices"
	"strings"
)

// ScopeLevel is one breadth "allow and remember" can store a request at.
type ScopeLevel struct {
	// Kind names the breadth: "exact" (this command or file), "prefix" (the
	// command's leading words), "command" (any use of the program), "dir" (the
	// file's folder), "project" (every file of the project), "host" or "tool".
	Kind string
	// Rules are the rules this level stores, in Suggest's format.
	Rules []string
}

// SuggestScopes lists the breadths "allow and remember" can store a request at,
// narrowest first, and which of them Suggest picks. A client lets the user
// widen or narrow what is remembered from there: a shell command goes from the
// exact command line to its prefix to the program, a file from itself to its
// folder to the project. Every level allows the request; destructive and
// interpreter commands keep their narrow forms only, as Suggest does. It is
// nil, -1 when the request cannot be remembered at all.
func SuggestScopes(req Request, opts Options) ([]ScopeLevel, int) {
	def := Suggest(req, opts)
	if len(def) == 0 {
		return nil, -1
	}
	var levels []ScopeLevel
	switch kindOfTool(req.Tool) {
	case kindShell:
		levels = shellScopes(req, opts)
	case kindPath:
		levels = pathScopes(req, opts)
	}
	at := slices.IndexFunc(levels, func(l ScopeLevel) bool { return slices.Equal(l.Rules, def) })
	if at < 0 {
		// Only the suggestion itself; name its kind for the client.
		kind := "tool"
		switch kindOfTool(req.Tool) {
		case kindShell:
			kind = "prefix"
		case kindPath:
			kind = "dir"
		case kindURL:
			kind = "host"
		}
		return []ScopeLevel{{Kind: kind, Rules: def}}, 0
	}
	// Keep only levels that really allow the request: a rule that does not
	// cover what the user approved would ask again next time.
	out := make([]ScopeLevel, 0, len(levels))
	for i, l := range levels {
		if i == at || scopeAllows(req, opts, l.Rules) {
			if i == at {
				at = len(out)
			}
			out = append(out, l)
		}
	}
	return out, at
}

// shellScopes builds the exact, prefix and command levels of a Bash request,
// segment by segment, skipping the segments Suggest skips.
func shellScopes(req Request, opts Options) []ScopeLevel {
	if family(req.Tool) != "Bash" {
		return nil
	}
	segments, ok := SplitCommand(req.Command, req.Shell)
	if !ok || movedReadOnly(segments, req.OtherDir) {
		return nil
	}
	readOnly := opts.AllowReadOnly && readOnlyShell(req.Shell)
	readOnlyAt := readOnlySegments(segments, req.OtherDir)
	tool := family(req.Tool)
	// forms[i] is segment i's rules from narrowest to widest.
	var forms [][]string
	for i, seg := range segments {
		if readOnly && readOnlyAt[i] {
			continue
		}
		prefix := CommandPrefix(seg)
		if destructive[prefix[0]] || strings.ContainsAny(prefix[0], `/\`) {
			return nil
		}
		exact := append([]string{prefix[0]}, seg[1:]...)
		cands := []Rule{{Tool: tool, tokens: exact, Pattern: joinTokens(exact)}}
		if !interpreters[prefix[0]] {
			cands = append(cands, Rule{Tool: tool, tokens: prefix, Pattern: joinTokens(prefix), Prefix: true})
			if len(prefix) > 1 {
				cands = append(cands, Rule{Tool: tool, tokens: prefix[:1], Pattern: joinTokens(prefix[:1]), Prefix: true})
			}
		}
		var texts []string
		for _, r := range cands {
			s := r.String()
			if parsed, err := ParseRule(s); err != nil || parsed.String() != s {
				return nil
			}
			if !contains(texts, s) {
				texts = append(texts, s)
			}
		}
		forms = append(forms, texts)
	}
	if len(forms) == 0 {
		return nil
	}
	kinds := []string{"exact", "prefix", "command"}
	var levels []ScopeLevel
	for level := range kinds {
		var rules []string
		for _, f := range forms {
			if r := f[min(level, len(f)-1)]; !contains(rules, r) {
				rules = append(rules, r)
			}
		}
		if n := len(levels); n > 0 && slices.Equal(levels[n-1].Rules, rules) {
			continue
		}
		levels = append(levels, ScopeLevel{Kind: kinds[level], Rules: rules})
	}
	return levels
}

// pathScopes builds the file, folder and project levels of an edit.
func pathScopes(req Request, opts Options) []ScopeLevel {
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
			levels := []ScopeLevel{{Kind: "exact", Rules: []string{"Edit(" + escapeGlob(rel) + ")"}}}
			if dir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel))); dir != "." {
				levels = append(levels, ScopeLevel{Kind: "dir", Rules: []string{"Edit(" + escapeGlob(dir) + "/**)"}})
			}
			return append(levels, ScopeLevel{Kind: "project", Rules: []string{"Edit(**)"}})
		}
	}
	abs := filepath.ToSlash(target)
	levels := []ScopeLevel{{Kind: "exact", Rules: []string{"Edit(" + escapeGlob(abs) + ")"}}}
	if dir := filepath.ToSlash(filepath.Dir(target)); dir != abs && dir != "/" && !strings.HasSuffix(dir, ":/") {
		levels = append(levels, ScopeLevel{Kind: "dir", Rules: []string{"Edit(" + escapeGlob(dir) + "/**)"}})
	}
	return levels
}

// scopeAllows reports whether storing rules (each in its default scope)
// allows req.
func scopeAllows(req Request, opts Options, rules []string) bool {
	user, project := RuleSet{Scope: ScopeUser}, RuleSet{Scope: ScopeProject}
	for _, r := range rules {
		stored := StoredRule{Rule: r, Effect: Allow}
		if DefaultScope(r) == ScopeProject {
			project.Rules = append(project.Rules, stored)
		} else {
			user.Rules = append(user.Rules, stored)
		}
	}
	return Evaluate(req, opts, user, project).Effect == Allow
}
