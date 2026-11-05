package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type cand struct {
	Text string
	Desc string
}

var commands = completionTable()

func completionTable() []cand {
	reg := slashRegistry()
	out := make([]cand, 0, len(reg))
	for _, e := range reg {
		out = append(out, cand{e.Name, e.Hint})
	}
	return out
}

var execNow = map[string]bool{
	"/archive": true, "/btw": true, "/clear": true, "/compact": true, "/computer-use": true, "/computer": true, "/context-doctor": true, "/effort": true, "/goal": true, "/goal-from-context": true, "/help": true,
	"/mcp": true, "/model": true, "/mouse": true, "/pwd": true, "/quit": true, "/report": true, "/resume": true, "/subagents": true, "/tasks": true, "/import": true,
	"/rewind": true, "/language": true, "/plugins": true, "/privacy": true, "/new-context": true, "/new_context": true,
}

func completions(val string, models, providers, skillCands, efforts []cand) (head string, cands []cand) {
	if efforts == nil {
		efforts = effortCands
	}
	i := strings.LastIndexAny(val, " \n")
	head, token := val[:i+1], val[i+1:]
	fields := strings.Fields(head)
	ranked := false
	switch {
	case strings.HasPrefix(val, "/") && len(fields) == 0:
		cands, ranked = rankCommands(commands, token, nil), true
	case len(fields) == 1 && (fields[0] == "/model" || fields[0] == "/model-for-session"):
		cands = filterFuzzy(append([]cand{{"refresh", "refetch provider model catalogs"}}, models...), token)
	case len(fields) == 2 && (fields[0] == "/model" || fields[0] == "/model-for-session") && fields[1] != "refresh":
		cands = filterFuzzy(providers, token)
	case len(fields) >= 1 && fields[0] == "/diff":
		options := []cand{{"--staged", "show staged changes"}, {"--stat", "show change statistics"}}
		for _, option := range options {
			used := false
			for _, field := range fields[1:] {
				if field == option.Text {
					used = true
					break
				}
			}
			if !used && strings.HasPrefix(option.Text, token) {
				cands = append(cands, option)
			}
		}
	case len(fields) == 1 && fields[0] == "/permissions":
		cands = filterPrefix([]cand{{"normal", "Confirm commands and file changes"}, {"plan", "Read-only planning"}, {"auto_review", "AI review with manual fallback"}, {"auto_edit", "Automatically allow file edits"}, {"full_access", "Allow tools with configured AI review"}, {"always", "Always allow tool execution"}}, token)
	case len(fields) == 1 && fields[0] == "/language":
		cands = filterPrefix([]cand{{"zh_cn", "简体中文"}, {"zh_Hant", "繁體中文"}, {"en", "English"}}, token)
	case len(fields) == 1 && fields[0] == "/effort":
		cands = filterPrefix(efforts, token)
	case len(fields) == 1 && fields[0] == "/compact":
		cands = filterPrefix(append([]cand{{"fresh", "start a new context window"}, {"off", "compact with the current model"}}, models...), token)
	case len(fields) == 2 && fields[0] == "/compact":
		cands = filterPrefix(providers, token)
	case strings.HasPrefix(token, "$"):
		cands = filterPrefix(skillCands, token)
	case strings.HasPrefix(token, "@"):
		cands, ranked = mentionCands(token[1:]), true
	case len(fields) == 1 && strings.HasPrefix(fields[0], "/") && argChoices(fields[0]) != nil:
		cands = filterPrefix(argChoices(fields[0]), token)
	case strings.HasPrefix(val, "/"):
	default:
		cands = pathMatches(token)
	}
	if ranked {
		return head, cands
	}
	sort.Slice(cands, func(a, b int) bool {
		left, right := strings.ToLower(cands[a].Text), strings.ToLower(cands[b].Text)
		if left == right {
			return cands[a].Text < cands[b].Text
		}
		return left < right
	})
	return head, cands
}

func filterPrefix(all []cand, prefix string) []cand {
	var out []cand
	for _, c := range all {
		if strings.HasPrefix(c.Text, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func filterFuzzy(all []cand, q string) []cand {
	if q == "" {
		return append([]cand(nil), all...)
	}
	type hit struct {
		c    cand
		tier int
	}
	var hits []hit
	for _, c := range all {
		if tier := matchTier(c.Text, q); tier >= 0 {
			hits = append(hits, hit{c, tier})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].tier < hits[b].tier })
	out := make([]cand, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.c)
	}
	return out
}

// mentionCands completes an @ mention. Files and directories come from the
// background index; a path with a slash lists that directory's entries, so a
// directory can be drilled into, and "path:" offers the file's line range.
func mentionCands(q string) []cand {
	var out []cand
	add := func(cs []cand) {
		for _, c := range cs {
			out = append(out, cand{"@" + c.Text, c.Desc})
		}
	}
	if c, ok := mentionRangeCand(q); ok {
		return []cand{c}
	}
	switch {
	case filepath.IsAbs(q) || strings.HasPrefix(q, "~") || strings.HasPrefix(q, "."):
		add(mentionPathMatches(q))
		return out
	case q == "" || strings.ContainsAny(q, "/\\"):
		dir, prefix := pathDir(filepath.ToSlash(q)), q[len(pathDir(filepath.ToSlash(q))):]
		files, dirs, ok := fileIndexSnapshot(fileIndexWait)
		if !ok {
			add(mentionPathMatches(q))
			return out
		}
		kids := childPaths(files, dirs, dir, prefix)
		if len(kids) == 0 && q != "" {
			kids = rankPaths(append(append([]string(nil), files...), dirs...), q, menuRows)
		}
		for _, p := range kids[:min(len(kids), mentionChildMax)] {
			out = append(out, pathCand(p))
		}
		return out
	}
	for _, p := range fuzzyPaths(q, menuRows) {
		out = append(out, pathCand(p))
	}
	return out
}

const mentionChildMax = 500

func pathCand(p string) cand {
	if strings.HasSuffix(p, "/") {
		return cand{"@" + p, "dir"}
	}
	return cand{"@" + p, ""}
}

var mentionRangeTail = regexp.MustCompile(`^\d*(?:-\d*)?$`)

// mentionRangeCand completes "path:" and "path:N-" for an existing file with
// the file's whole line range.
func mentionRangeCand(q string) (cand, bool) {
	i := strings.LastIndexByte(q, ':')
	if i <= 0 || !mentionRangeTail.MatchString(q[i+1:]) {
		return cand{}, false
	}
	p := q[:i]
	abs := p
	if !filepath.IsAbs(abs) {
		root, err := currentRoot()
		if err != nil {
			return cand{}, false
		}
		abs = filepath.Join(root, p)
	}
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() || fi.Size() > 8<<20 {
		return cand{}, false
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return cand{}, false
	}
	n := strings.Count(string(data), "\n")
	if len(data) > 0 && data[len(data)-1] != '\n' {
		n++
	}
	tail := q[i+1:]
	switch {
	case tail == "":
		tail = fmt.Sprintf("1-%d", max(n, 1))
	case strings.HasSuffix(tail, "-"):
		tail += strconv.Itoa(max(n, 1))
	}
	return cand{"@" + p + ":" + tail, fmt.Sprintf("%d lines", n)}, true
}

func mentionPathMatches(q string) []cand {
	if filepath.IsAbs(q) || q == "~" || strings.HasPrefix(q, "~/") {
		return pathMatches(q)
	}
	root, err := currentRoot()
	if err != nil {
		return nil
	}
	var out []cand
	for _, c := range pathMatches(filepath.Join(root, q)) {
		dir := strings.HasSuffix(c.Text, "/")
		if rel, err := filepath.Rel(root, strings.TrimSuffix(c.Text, "/")); err == nil {
			c.Text = filepath.ToSlash(rel)
			if dir {
				c.Text += "/"
			}
			out = append(out, c)
		}
	}
	return out
}

func pathMatches(prefix string) []cand {
	p := prefix
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = home + p[1:]
		}
	}
	matches, _ := filepath.Glob(p + "*")
	var out []cand
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.IsDir() {
			out = append(out, cand{m + "/", "dir"})
		} else {
			out = append(out, cand{m, ""})
		}
	}
	return out
}
