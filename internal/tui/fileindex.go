package tui

import (
	"bufio"
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The @file index lists the files under the working directory for @
// completion. It is built in the background, off the UI goroutine: with
// `git ls-files`, which honours every .gitignore, inside a Git work tree, and
// otherwise by walking the tree with the .gitignore files it finds. A stale
// index keeps answering while its replacement builds.
const (
	fileIndexTTL = 5 * time.Second
	fileIndexMax = 200000
)

type fileIndexState struct {
	sync.Mutex
	root    string
	files   []string // slash-separated, relative to root, sorted
	dirs    []string // the same for directories, each ending in "/"
	builtAt time.Time
	ready   bool
	// gen numbers builds so a build for a root nobody wants any more cannot
	// install its result.
	gen      int
	building string
	done     chan struct{}
	// notify is called after each build, off the UI goroutine.
	notify func()
}

var fileIndex fileIndexState

var currentRoot = os.Getwd

// fileIndexWait is how long completion waits for the first index of a
// directory before answering without it.
var fileIndexWait = 150 * time.Millisecond

// fileIndexResolveWait is how long resolving a bare @name on send waits.
var fileIndexResolveWait = 2 * time.Second

// fileIndexSnapshot returns the index for the current root, starting a build
// when there is none or it is stale. With no index for this root yet it waits
// up to wait for the build; ok is false when it still has none.
func fileIndexSnapshot(wait time.Duration) (files, dirs []string, ok bool) {
	return fileIndexGet(wait, false)
}

// fileIndexGet is fileIndexSnapshot; with fresh set a stale index is not
// served either, the caller waits for its replacement.
func fileIndexGet(wait time.Duration, fresh bool) (files, dirs []string, ok bool) {
	wd, err := currentRoot()
	if err != nil {
		return nil, nil, false
	}
	fileIndex.Lock()
	have := fileIndex.ready && fileIndex.root == wd
	stale := !have || time.Since(fileIndex.builtAt) > fileIndexTTL
	if stale {
		startFileIndexBuild(wd)
	}
	if have && !(fresh && stale) {
		files, dirs = fileIndex.files, fileIndex.dirs
		fileIndex.Unlock()
		return files, dirs, true
	}
	done := fileIndex.done
	fileIndex.Unlock()
	if wait > 0 && done != nil {
		select {
		case <-done:
		case <-time.After(wait):
		}
	}
	fileIndex.Lock()
	defer fileIndex.Unlock()
	if fileIndex.ready && fileIndex.root == wd && (!fresh || time.Since(fileIndex.builtAt) <= fileIndexTTL) {
		return fileIndex.files, fileIndex.dirs, true
	}
	if fresh && fileIndex.ready && fileIndex.root == wd {
		// The rebuild is slow; the old index is better than none.
		return fileIndex.files, fileIndex.dirs, true
	}
	return nil, nil, false
}

// startFileIndexBuild starts building the index for wd unless that build is
// already running. The caller holds fileIndex's lock.
func startFileIndexBuild(wd string) {
	if fileIndex.building == wd {
		return
	}
	fileIndex.gen++
	gen := fileIndex.gen
	fileIndex.building = wd
	done := make(chan struct{})
	fileIndex.done = done
	go func() {
		files, dirs := buildFileIndex(wd)
		fileIndex.Lock()
		installed := gen == fileIndex.gen
		if installed {
			fileIndex.root, fileIndex.files, fileIndex.dirs = wd, files, dirs
			fileIndex.builtAt, fileIndex.ready, fileIndex.building = time.Now(), true, ""
		}
		notify := fileIndex.notify
		fileIndex.Unlock()
		close(done)
		if installed && notify != nil {
			notify()
		}
	}()
}

// setFileIndexNotify sets the function called after each build.
func setFileIndexNotify(f func()) {
	fileIndex.Lock()
	fileIndex.notify = f
	fileIndex.Unlock()
}

// resetFileIndex forgets the index and any build in flight.
func resetFileIndex() {
	fileIndex.Lock()
	fileIndex.gen++
	fileIndex.root, fileIndex.files, fileIndex.dirs = "", nil, nil
	fileIndex.builtAt, fileIndex.ready, fileIndex.building, fileIndex.done = time.Time{}, false, "", nil
	fileIndex.Unlock()
}

// buildFileIndex lists root's files and the directories that hold them.
func buildFileIndex(root string) (files, dirs []string) {
	var ok bool
	if files, ok = gitListFiles(root); !ok {
		files = walkListFiles(root)
	}
	sort.Strings(files)
	seen := map[string]bool{}
	for _, f := range files {
		for d := pathDir(f); d != ""; d = pathDir(strings.TrimSuffix(d, "/")) {
			if seen[d] {
				break
			}
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	sort.Strings(dirs)
	return files, dirs
}

// pathDir is the directory part of a slash path with its trailing slash, or
// "" at the top.
func pathDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i+1]
	}
	return ""
}

// gitListFiles asks Git for the tracked and the untracked-but-not-ignored
// files under root. ok is false outside a work tree or without git.
func gitListFiles(root string) ([]string, bool) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var files []string
	seen := map[string]bool{}
	for p := range bytes.SplitSeq(out, []byte{0}) {
		if len(p) == 0 {
			continue
		}
		f := string(p)
		if seen[f] {
			continue
		}
		seen[f] = true
		files = append(files, f)
		if len(files) >= fileIndexMax {
			break
		}
	}
	return files, true
}

// walkListFiles walks root without Git. It skips hidden directories, vendor
// and node_modules, and whatever the .gitignore files on the way exclude.
func walkListFiles(root string) []string {
	var files []string
	rules := map[string][]ignoreRule{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || len(files) >= fileIndexMax {
			if d != nil && d.IsDir() && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if path != root {
				name := d.Name()
				if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" || ignored(rules, rel, true) {
					return filepath.SkipDir
				}
			} else {
				rel = ""
			}
			if r := readGitignore(filepath.Join(path, ".gitignore"), rel); len(r) > 0 {
				rules[rel] = r
			}
			return nil
		}
		if !ignored(rules, rel, false) {
			files = append(files, rel)
		}
		return nil
	})
	return files
}

// ignoreRule is one .gitignore line.
type ignoreRule struct {
	neg     bool
	dirOnly bool
	// base is the directory of the .gitignore, slash-separated with no
	// trailing slash, "" at the root.
	base string
	// anchored rules match the path below base; the rest match any name.
	anchored bool
	re       *regexp.Regexp
}

// readGitignore parses the .gitignore at path, which sits in directory base.
// It covers the common forms: comments, negation, a trailing slash for
// directories only, a leading or inner slash anchoring the pattern, and the
// *, ?, ** and [...] wildcards.
func readGitignore(path, base string) []ignoreRule {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var rules []ignoreRule
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r := ignoreRule{base: base}
		if strings.HasPrefix(line, "!") {
			r.neg, line = true, line[1:]
		}
		line = strings.TrimPrefix(line, `\`)
		if strings.HasSuffix(line, "/") {
			r.dirOnly, line = true, strings.TrimRight(line, "/")
		}
		if line == "" {
			continue
		}
		if strings.Contains(line, "/") {
			r.anchored = true
			line = strings.TrimPrefix(line, "/")
		}
		re, err := regexp.Compile("^" + globRegexp(line) + "$")
		if err != nil {
			continue
		}
		r.re = re
		rules = append(rules, r)
	}
	return rules
}

// globRegexp turns a gitignore glob into a regular expression body.
func globRegexp(glob string) string {
	var b strings.Builder
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case strings.HasPrefix(glob[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(glob[i:], "/**"):
			b.WriteString("(?:/.*)?")
			i += 2
		case strings.HasPrefix(glob[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			if j := strings.IndexByte(glob[i+1:], ']'); j >= 0 {
				class := glob[i+1 : i+1+j]
				if strings.HasPrefix(class, "!") {
					class = "^" + class[1:]
				}
				b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
				i += j + 1
				continue
			}
			b.WriteString(`\[`)
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}

// ignored reports whether rel (a file, or a directory when dir is set) is
// excluded by the rules of its own and its parents' .gitignore files. As in
// Git, the last matching rule wins.
func ignored(rules map[string][]ignoreRule, rel string, dir bool) bool {
	out := false
	name := rel[strings.LastIndexByte(rel, '/')+1:]
	bases := []string{""}
	for i := range len(rel) {
		if rel[i] == '/' {
			bases = append(bases, rel[:i])
		}
	}
	for _, base := range bases {
		for _, r := range rules[base] {
			if r.dirOnly && !dir {
				continue
			}
			sub := rel
			if base != "" {
				sub = strings.TrimPrefix(rel, base+"/")
			}
			target := name
			if r.anchored {
				target = sub
			}
			if r.re.MatchString(target) {
				out = !r.neg
			}
		}
	}
	return out
}

// fuzzyFiles ranks the index's files against query.
func fuzzyFiles(query string, limit int) []string {
	files, _, _ := fileIndexSnapshot(fileIndexWait)
	return rankPaths(files, query, limit)
}

// fuzzyPaths ranks files and directories together, for @ completion where a
// directory can be drilled into.
func fuzzyPaths(query string, limit int) []string {
	files, dirs, _ := fileIndexSnapshot(fileIndexWait)
	return rankPaths(append(append([]string(nil), files...), dirs...), query, limit)
}

func rankPaths(paths []string, query string, limit int) []string {
	q := strings.ToLower(query)
	if q == "" {
		out := append([]string(nil), paths...)
		sort.Strings(out)
		return out[:min(len(out), limit)]
	}
	type hit struct {
		f    string
		tier int
	}
	var hits []hit
	for _, f := range paths {
		if tier := matchTier(f, q); tier >= 0 {
			hits = append(hits, hit{f, tier})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].tier != hits[b].tier {
			return hits[a].tier < hits[b].tier
		}
		if la, lb := len(hits[a].f), len(hits[b].f); la != lb {
			return la < lb
		}
		return hits[a].f < hits[b].f
	})
	out := make([]string, 0, min(len(hits), limit))
	for _, h := range hits {
		out = append(out, h.f)
		if len(out) == limit {
			break
		}
	}
	return out
}

// childPaths lists the index entries directly inside dir ("" for the root,
// otherwise ending in "/") whose names start with prefix, directories first.
func childPaths(files, dirs []string, dir, prefix string) []string {
	var outDirs, outFiles []string
	lp := strings.ToLower(prefix)
	pick := func(list []string, into *[]string, isDir bool) {
		for _, p := range list {
			if !strings.HasPrefix(p, dir) {
				continue
			}
			rest := p[len(dir):]
			if isDir {
				rest = strings.TrimSuffix(rest, "/")
			}
			if rest == "" || strings.Contains(rest, "/") || !strings.HasPrefix(strings.ToLower(rest), lp) {
				continue
			}
			*into = append(*into, p)
		}
	}
	pick(dirs, &outDirs, true)
	pick(files, &outFiles, false)
	return append(outDirs, outFiles...)
}

func matchTier(f, q string) int {
	if q == "" {
		return 0
	}
	lf := strings.ToLower(f)
	trimmed := strings.TrimSuffix(lf, "/")
	base := trimmed[strings.LastIndexByte(trimmed, '/')+1:]
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
		s = s[i+len(string(r)):]
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
		files, _, _ := fileIndexGet(fileIndexResolveWait, true)
		if hits := rankPaths(files, p, 2); len(hits) == 1 {
			if wd, err := currentRoot(); err == nil {
				return filepath.Join(wd, filepath.FromSlash(hits[0])), true
			}
		}
	}
	return "", false
}
