package backend

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

const (
	mentionDefaultResults = 50
	mentionMaxResults     = 500
	mentionMaxDepth       = 32
)

// mentionScanLimit bounds the entries one request visits, so a huge tree
// answers quickly with truncated set instead of walking all of it.
var mentionScanLimit = 50000

// mentionDefaultIgnores are skipped at any depth whether or not an ignore file
// names them: version control metadata, dependency trees and caches.
var mentionDefaultIgnores = map[string]bool{
	".git": true, ".hg": true, ".svn": true, ".jj": true,
	"node_modules": true, "__pycache__": true, ".venv": true, ".DS_Store": true,
}

type mentionIgnoreRule struct {
	base   string // slash-separated workspace directory holding the ignore file
	re     *regexp.Regexp
	negate bool
	dir    bool
}

type mentionHit struct {
	path, kind string
	tier       int
}

// mentionList walks the workspace below rel for @-mention completion: files
// and directories that are not ignored (defaults, .gitignore and .ignore at
// every level from the workspace root down), ranked against the query.
func mentionList(ctx context.Context, workspace *os.Root, rel string, in filesystemInput) (any, error) {
	limit := int(in.MaxResults)
	if limit == 0 {
		limit = mentionDefaultResults
	}
	if limit > mentionMaxResults {
		return nil, errors.New("mention_list max_results exceeds 500")
	}
	start := filepath.ToSlash(rel)
	if start == "" {
		start = "."
	}
	info, err := workspace.Stat(filepath.FromSlash(start))
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("mention_list path must be a directory")
	}
	fsys := workspace.FS()
	// Ignore files above the start directory still apply below it.
	var rules []mentionIgnoreRule
	if start != "." {
		dir := "."
		rules = append(rules, mentionIgnoreRules(fsys, dir)...)
		for _, part := range strings.Split(start, "/") {
			dir = path.Join(dir, part)
			if mentionIgnored(rules, dir, true) || mentionDefaultIgnores[part] {
				return map[string]any{"path": in.Path, "query": in.Query, "entries": []map[string]string{}, "truncated": false}, nil
			}
			if dir != start {
				rules = append(rules, mentionIgnoreRules(fsys, dir)...)
			}
		}
	}
	query := strings.ToLower(strings.TrimSpace(in.Query))
	var hits []mentionHit
	scanned, truncated := 0, false
	var walk func(dir string, depth int, rules []mentionIgnoreRule) error
	walk = func(dir string, depth int, rules []mentionIgnoreRule) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			if dir == start {
				return err
			}
			return nil // an unreadable subdirectory is left out
		}
		rules = append(rules[:len(rules):len(rules)], mentionIgnoreRules(fsys, dir)...)
		for _, entry := range entries {
			if scanned >= mentionScanLimit {
				truncated = true
				return nil
			}
			scanned++
			name := entry.Name()
			isDir := entry.IsDir()
			if mentionDefaultIgnores[name] || (!isDir && !entry.Type().IsRegular()) {
				continue
			}
			full := path.Join(dir, name)
			if mentionIgnored(rules, full, isDir) {
				continue
			}
			kind := "file"
			if isDir {
				kind = "dir"
			}
			if tier := mentionTier(full, query); tier >= 0 {
				hits = append(hits, mentionHit{path: full, kind: kind, tier: tier})
			}
			if isDir && depth < mentionMaxDepth {
				if err := walk(full, depth+1, rules); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(start, 1, rules); err != nil {
		return nil, err
	}
	sort.Slice(hits, func(a, b int) bool {
		if hits[a].tier != hits[b].tier {
			return hits[a].tier < hits[b].tier
		}
		// Without a query, shallow entries come first in name order; with
		// one, shorter paths win within a tier.
		if query == "" {
			if da, db := strings.Count(hits[a].path, "/"), strings.Count(hits[b].path, "/"); da != db {
				return da < db
			}
		} else if la, lb := len(hits[a].path), len(hits[b].path); la != lb {
			return la < lb
		}
		return hits[a].path < hits[b].path
	})
	if len(hits) > limit {
		hits, truncated = hits[:limit], true
	}
	out := make([]map[string]string, 0, len(hits))
	for _, hit := range hits {
		out = append(out, map[string]string{"path": hit.path, "kind": hit.kind})
	}
	return map[string]any{"path": in.Path, "query": in.Query, "entries": out, "truncated": truncated}, nil
}

// mentionTier ranks p (workspace-relative, slash-separated) against a
// lower-case query: 0 name prefix, 1 name contains, 2 path contains, 3 name
// subsequence, 4 path subsequence, -1 no match. Every path matches an empty
// query.
func mentionTier(p, query string) int {
	if query == "" {
		return 0
	}
	lower := strings.ToLower(p)
	name := lower[strings.LastIndexByte(lower, '/')+1:]
	switch {
	case strings.HasPrefix(name, query):
		return 0
	case strings.Contains(name, query):
		return 1
	case strings.Contains(lower, query):
		return 2
	case mentionSubsequence(name, query):
		return 3
	case mentionSubsequence(lower, query):
		return 4
	}
	return -1
}

func mentionSubsequence(s, query string) bool {
	for _, r := range query {
		i := strings.IndexRune(s, r)
		if i < 0 {
			return false
		}
		s = s[i+len(string(r)):]
	}
	return true
}

// mentionIgnoreRules reads dir's .gitignore and .ignore. Patterns follow
// gitignore: "!" negates, a trailing "/" matches only directories, and a
// pattern without an inner "/" matches at any depth below dir.
func mentionIgnoreRules(fsys fs.FS, dir string) []mentionIgnoreRule {
	var out []mentionIgnoreRule
	for _, name := range []string{".gitignore", ".ignore"} {
		file := path.Join(dir, name)
		info, err := fs.Stat(fsys, file)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			continue
		}
		data, err := fs.ReadFile(fsys, file)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			negate := strings.HasPrefix(line, "!")
			line = strings.TrimPrefix(line, "!")
			onlyDir := strings.HasSuffix(line, "/")
			line = strings.TrimSuffix(line, "/")
			anchored := strings.HasPrefix(line, "/")
			line = strings.TrimPrefix(line, "/")
			if line == "" {
				continue
			}
			if !anchored && !strings.Contains(line, "/") {
				line = "**/" + line
			}
			if re, err := tools.IgnoreGlob(line); err == nil {
				out = append(out, mentionIgnoreRule{base: dir, re: re, negate: negate, dir: onlyDir})
			}
		}
	}
	return out
}

// mentionIgnored applies rules in order; the last matching rule wins.
func mentionIgnored(rules []mentionIgnoreRule, p string, isDir bool) bool {
	ignored := false
	for _, rule := range rules {
		rel := p
		if rule.base != "." {
			if !strings.HasPrefix(p, rule.base+"/") {
				continue
			}
			rel = p[len(rule.base)+1:]
		}
		if rule.re.MatchString(rel) && (!rule.dir || isDir) {
			ignored = !rule.negate
		}
	}
	return ignored
}
