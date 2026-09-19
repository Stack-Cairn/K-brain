package tui

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const fileIndexTTL = 2 * time.Second

var fileIndex struct {
	sync.Mutex
	builtAt time.Time
	root    string
	files   []string
}

var currentRoot = os.Getwd

func refreshFileIndex() {
	fileIndex.Lock()
	defer fileIndex.Unlock()
	wd, err := currentRoot()
	if err != nil {
		return
	}
	if wd == fileIndex.root && time.Since(fileIndex.builtAt) < fileIndexTTL {
		return
	}
	var files []string
	_ = filepath.WalkDir(wd, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == wd {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(wd, path)
		if err != nil {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	fileIndex.root, fileIndex.files, fileIndex.builtAt = wd, files, time.Now()
}

func fuzzyFiles(query string, limit int) []string {
	refreshFileIndex()
	fileIndex.Lock()
	files := append([]string(nil), fileIndex.files...)
	fileIndex.Unlock()

	q := strings.ToLower(query)
	type hit struct {
		f    string
		tier int
	}
	var hits []hit
	for _, f := range files {
		tier := matchTier(f, q)
		if tier < 0 {
			continue
		}
		hits = append(hits, hit{f, tier})
		if q != "" && len(hits) >= limit {
			break
		}
	}
	if q != "" {
		sort.SliceStable(hits, func(a, b int) bool {
			if hits[a].tier != hits[b].tier {
				return hits[a].tier < hits[b].tier
			}
			return hits[a].f < hits[b].f
		})
	} else {
		sort.Strings(files)
		hits = hits[:0]
		for _, f := range files {
			hits = append(hits, hit{f, 0})
		}
	}
	out := make([]string, 0, min(len(hits), limit))
	for _, h := range hits {
		out = append(out, h.f)
		if len(out) == limit {
			break
		}
	}
	return out
}

func matchTier(f, q string) int {
	if q == "" {
		return 0
	}
	lf := strings.ToLower(f)
	base := lf[strings.LastIndexByte(lf, '/')+1:]
	if strings.Contains(base, q) {
		return 0
	}
	if strings.Contains(lf, q) {
		return 1
	}
	if subseq(base, q) {
		return 2
	}
	if subseq(lf, q) {
		return 3
	}
	return -1
}

func subseq(s, q string) bool {
	for _, r := range q {
		i := strings.IndexRune(s, r)
		if i < 0 {
			return false
		}
		s = s[i+1:]
	}
	return true
}

func resolveMentionPath(p string) (string, bool) {
	abs := p
	if abs == "~" || strings.HasPrefix(abs, "~/") || strings.HasPrefix(abs, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			abs = filepath.Join(home, strings.TrimLeft(abs[1:], `/\`))
		}
	}
	if resolved, err := filepath.Abs(abs); err == nil {
		abs = resolved
	}
	if _, err := os.Stat(abs); err == nil {
		return abs, true
	}

	if !strings.ContainsAny(p, "/\\") {
		if hits := fuzzyFiles(p, 2); len(hits) == 1 {
			if wd, err := currentRoot(); err == nil {
				return filepath.Join(wd, filepath.FromSlash(hits[0])), true
			}
		}
	}
	return "", false
}
