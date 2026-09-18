package tui

import (
	"os"
	"path/filepath"
	"sort"
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
	"/clear": true, "/compact": true, "/computer-use": true, "/computer": true, "/context-doctor": true, "/effort": true, "/goal": true, "/goal-from-context": true, "/help": true,
	"/mcp": true, "/model": true, "/mouse": true, "/pwd": true, "/quit": true, "/report": true, "/resume": true, "/subagents": true, "/tasks": true,
	"/rewind": true, "/language": true,
}

func completions(val string, models, providers, skillCands, efforts []cand) (head string, cands []cand) {
	if efforts == nil {
		efforts = effortCands
	}
	i := strings.LastIndexAny(val, " \n")
	head, token := val[:i+1], val[i+1:]
	fields := strings.Fields(head)
	switch {
	case strings.HasPrefix(val, "/") && len(fields) == 0:
		cands = filterPrefix(commands, token)
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
		cands = filterPrefix([]cand{{"normal", "Confirm commands and file changes"}, {"plan", "Read-only planning"}, {"always", "Always allow tool execution"}}, token)
	case len(fields) == 1 && fields[0] == "/language":
		cands = filterPrefix([]cand{{"zh_cn", "简体中文"}, {"en", "English"}}, token)
	case len(fields) == 1 && fields[0] == "/effort":
		cands = filterPrefix(efforts, token)
	case len(fields) == 1 && fields[0] == "/compact":
		cands = filterPrefix(append([]cand{{"off", "compact with the current model"}}, models...), token)
	case len(fields) == 2 && fields[0] == "/compact":
		cands = filterPrefix(providers, token)
	case strings.HasPrefix(token, "$"):
		cands = filterPrefix(skillCands, token)
	case strings.HasPrefix(token, "@"):

		if q := token[1:]; isPathQuery(q) {
			for _, c := range mentionPathMatches(q) {
				cands = append(cands, cand{"@" + c.Text, c.Desc})
			}
		} else {
			for _, f := range fuzzyFiles(q, menuRows) {
				cands = append(cands, cand{"@" + f, ""})
			}
		}
	case strings.HasPrefix(val, "/"):
	default:
		cands = pathMatches(token)
	}
	sort.Slice(cands, func(a, b int) bool { return cands[a].Text < cands[b].Text })
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

func isPathQuery(q string) bool {
	return q == "" || strings.ContainsAny(q, "/\\") || strings.HasPrefix(q, "~") || strings.HasPrefix(q, ".")
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
