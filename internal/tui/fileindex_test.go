package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func chdir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := currentRoot
	currentRoot = func() (string, error) { return dir, nil }
	t.Cleanup(func() { currentRoot = old })
	fileIndex.Lock()
	fileIndex.root, fileIndex.files, fileIndex.builtAt = "", nil, time.Time{}
	fileIndex.Unlock()
	return dir
}

func write(t *testing.T, dir, rel string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFuzzyFiles(t *testing.T) {
	dir := chdir(t)
	write(t, dir, "docs/roadmap.md")
	write(t, dir, "internal/tui/roadmap_notes.txt")
	write(t, dir, "README.md")
	write(t, dir, "cmd/kn/main.go")

	hits := fuzzyFiles("roadmap", 8)
	if len(hits) != 2 || hits[0] != "docs/roadmap.md" {
		t.Fatalf("roadmap: %v", hits)
	}

	hits = fuzzyFiles("main", 8)
	if len(hits) != 1 || hits[0] != "cmd/kn/main.go" {
		t.Fatalf("main: %v", hits)
	}

	hits = fuzzyFiles("rdmp", 8)
	if len(hits) != 2 {
		t.Fatalf("rdmp subsequence: %v", hits)
	}

	hits = fuzzyFiles("", 8)
	if len(hits) != 4 || hits[0] != "README.md" {
		t.Fatalf("empty: %v", hits)
	}

	if hits = fuzzyFiles("zzz", 8); len(hits) != 0 {
		t.Fatalf("zzz: %v", hits)
	}

	write(t, dir, ".git/config")
	write(t, dir, "vendor/pkg/mod.go")
	fileIndex.Lock()
	fileIndex.builtAt = time.Time{}
	fileIndex.Unlock()
	if hits = fuzzyFiles("", 32); len(hits) != 4 {
		t.Fatalf("hidden/vendor should be skipped: %v", hits)
	}
}

func TestAtMentionFuzzyCompletion(t *testing.T) {
	dir := chdir(t)
	write(t, dir, "docs/roadmap.md")
	write(t, dir, "alpha.txt")

	_, cs := completions("fix @road", nil, nil, nil, nil)
	if len(cs) != 1 || cs[0].Text != "@docs/roadmap.md" {
		t.Fatalf("fuzzy @ completion: %v", texts(cs))
	}

	_, cs = completions("fix @al", nil, nil, nil, nil)
	if len(cs) != 1 || cs[0].Text != "@alpha.txt" {
		t.Fatalf("glob @ completion: %v", texts(cs))
	}
	_, cs = completions("fix @docs/r", nil, nil, nil, nil)
	if len(cs) != 1 || cs[0].Text != "@docs/roadmap.md" {
		t.Fatalf("slash query: %v", texts(cs))
	}
}

func TestExpandMentionsFuzzy(t *testing.T) {
	dir := chdir(t)
	write(t, dir, "docs/roadmap.md")

	t.Chdir(dir)

	out := expandMentions("see @roadmap")
	abs := filepath.Join(dir, "docs", "roadmap.md")
	if !strings.Contains(out, abs) {
		t.Fatalf("fuzzy mention should resolve to %q: %q", abs, out)
	}

	write(t, dir, "plans/roadmap.md")
	fileIndex.Lock()
	fileIndex.builtAt = time.Time{}
	fileIndex.Unlock()
	if got := expandMentions("see @roadmap"); got != "see @roadmap" {
		t.Fatalf("ambiguous should be unchanged: %q", got)
	}

	if got := expandMentions("see @docs/road"); got != "see @docs/road" {
		t.Fatalf("partial path should be unchanged: %q", got)
	}
}

func TestCompletionUsesRootNotTestCwd(t *testing.T) {
	dir := chdir(t)
	write(t, dir, "docs/roadmap.md")

	_, cs := completions("fix @roadmap", nil, nil, nil, nil)
	if len(cs) != 1 || cs[0].Text != "@docs/roadmap.md" {
		t.Fatalf("rooted completion: %v", texts(cs))
	}
}
