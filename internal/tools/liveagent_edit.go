package tools

import (
	"errors"
	"strings"
)

var (
	errEditAmbiguous = errors.New("lenient edit has ambiguous whitespace matches; Read and use exact text")
	errEditNotFound  = errors.New("old_string not found; Read the file and use its exact contents")
)

func liveagentEditMatch(text, old, replacement string) (string, string, string, error) {
	if strings.Contains(text, old) {
		return old, replacement, "exact", nil
	}
	normalize := func(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }
	needle := normalize(old)
	normalized := normalize(text)
	if strings.Contains(normalized, needle) {
		actual := needle
		repl := normalize(replacement)
		if strings.Contains(text, "\r\n") {
			actual = strings.ReplaceAll(actual, "\n", "\r\n")
			repl = strings.ReplaceAll(repl, "\n", "\r\n")
		}
		if strings.Contains(text, actual) {
			return actual, repl, "line-endings", nil
		}
	}
	lines := strings.SplitAfter(text, "\n")
	wanted := strings.Split(needle, "\n")
	if strings.HasSuffix(needle, "\n") {
		wanted = wanted[:len(wanted)-1]
	}
	for _, strategy := range []string{"trailing-whitespace", "indentation"} {
		var matches []struct{ old, new string }
		for i := 0; i+len(wanted) <= len(lines); i++ {
			ok := true
			shift := ""
			remove := ""
			for j, w := range wanted {
				got := strings.TrimSuffix(strings.TrimSuffix(lines[i+j], "\n"), "\r")
				if strategy == "trailing-whitespace" {
					if strings.TrimRight(got, " \t") != strings.TrimRight(w, " \t") {
						ok = false
						break
					}
					continue
				}
				if strings.TrimSpace(got) != strings.TrimSpace(w) {
					ok = false
					break
				}
				if strings.TrimSpace(w) == "" {
					continue
				}
				gi := got[:len(got)-len(strings.TrimLeft(got, " \t"))]
				wi := w[:len(w)-len(strings.TrimLeft(w, " \t"))]
				if j == 0 {
					if strings.HasPrefix(gi, wi) {
						shift = strings.TrimPrefix(gi, wi)
					} else if strings.HasPrefix(wi, gi) {
						remove = strings.TrimPrefix(wi, gi)
					} else {
						ok = false
						break
					}
				}
				if gi != shift+strings.TrimPrefix(wi, remove) {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			actual := strings.Join(lines[i:i+len(wanted)], "")
			if !strings.HasSuffix(needle, "\n") {
				actual = strings.TrimSuffix(strings.TrimSuffix(actual, "\n"), "\r")
			}
			repl := normalize(replacement)
			if strategy == "indentation" {
				parts := strings.Split(repl, "\n")
				for k, p := range parts {
					if p != "" {
						parts[k] = shift + strings.TrimPrefix(p, remove)
					}
				}
				repl = strings.Join(parts, "\n")
			}
			if strings.Contains(actual, "\r\n") {
				repl = strings.ReplaceAll(repl, "\n", "\r\n")
			}
			matches = append(matches, struct{ old, new string }{actual, repl})
		}
		if len(matches) > 0 {
			first := matches[0]
			for _, m := range matches[1:] {
				if m != first {
					return "", "", "", errEditAmbiguous
				}
			}
			return first.old, first.new, strategy, nil
		}
	}
	return "", "", "", errEditNotFound
}
