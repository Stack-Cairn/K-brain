package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeText(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestFileIndexUsesGitAndGitignore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := chdir(t)
	writeText(t, dir, ".gitignore", "build/\n*.log\n")
	writeText(t, dir, "main.go", "package main\n")
	writeText(t, dir, "internal/app/app.go", "package app\n")
	writeText(t, dir, "notes/todo.md", "untracked but not ignored\n")
	writeText(t, dir, "build/out.bin", "ignored dir\n")
	writeText(t, dir, "debug.log", "ignored file\n")
	writeText(t, dir, ".github/workflows/ci.yml", "tracked dot dir\n")
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "add", ".gitignore", "main.go", "internal/app/app.go", ".github/workflows/ci.yml")

	files, dirs, ok := fileIndexGet(5e9, true)
	if !ok {
		t.Fatal("index not built")
	}
	for _, want := range []string{"main.go", "internal/app/app.go", "notes/todo.md", ".github/workflows/ci.yml", ".gitignore"} {
		if !slices.Contains(files, want) {
			t.Errorf("index lacks %s: %v", want, files)
		}
	}
	for _, bad := range []string{"build/out.bin", "debug.log"} {
		if slices.Contains(files, bad) {
			t.Errorf("index has ignored %s", bad)
		}
	}
	for _, want := range []string{"internal/", "internal/app/", "notes/"} {
		if !slices.Contains(dirs, want) {
			t.Errorf("dirs lack %s: %v", want, dirs)
		}
	}
	if slices.Contains(dirs, "build/") {
		t.Error("an ignored directory is listed")
	}
}

func TestFileIndexWalkHonoursGitignore(t *testing.T) {
	dir := t.TempDir()
	writeText(t, dir, ".gitignore", "# comment\n*.tmp\n/dist\n!keep.tmp\ndocs/**/draft-*.md\n")
	writeText(t, dir, "a.go", "")
	writeText(t, dir, "x.tmp", "")
	writeText(t, dir, "keep.tmp", "")
	writeText(t, dir, "dist/bundle.js", "")
	writeText(t, dir, "src/dist/kept.js", "")
	writeText(t, dir, "docs/guide/draft-1.md", "")
	writeText(t, dir, "docs/guide/final.md", "")
	writeText(t, dir, "pkg/.gitignore", "gen/\n")
	writeText(t, dir, "pkg/gen/z.go", "")
	writeText(t, dir, "pkg/lib.go", "")
	writeText(t, dir, "node_modules/x/index.js", "")
	writeText(t, dir, ".cache/blob", "")

	got := walkListFiles(dir)
	want := []string{".gitignore", "a.go", "docs/guide/final.md", "keep.tmp", "pkg/.gitignore", "pkg/lib.go", "src/dist/kept.js"}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("walk = %v\nwant  %v", got, want)
	}
}

func TestFileIndexBuildsInBackground(t *testing.T) {
	dir := chdir(t)
	writeText(t, dir, "slow.go", "")
	// A zero wait never blocks: the first call starts the build and answers
	// without an index.
	if _, _, ok := fileIndexSnapshot(0); ok {
		t.Skip("the build finished before the first look; nothing to check")
	}
	done := make(chan struct{})
	setFileIndexNotify(func() { close(done) })
	t.Cleanup(func() { setFileIndexNotify(nil) })
	if files, _, ok := fileIndexSnapshot(5e9); !ok || !slices.Contains(files, "slow.go") {
		t.Fatalf("index after waiting: %v %v", files, ok)
	}
	<-done
}

func TestMentionCompletionDrillsIntoDirectories(t *testing.T) {
	dir := chdir(t)
	writeText(t, dir, "README.md", "")
	writeText(t, dir, "internal/tui/view.go", "")
	writeText(t, dir, "internal/tui/keys.go", "")
	writeText(t, dir, "internal/agent/agent.go", "")
	fileIndexWait = 5e9
	t.Cleanup(func() { fileIndexWait = 150e6 })

	_, cs := completions("@", nil, nil, nil, nil)
	if got := texts(cs); !slices.Equal(got, []string{"@internal/", "@README.md"}) {
		t.Fatalf("@ lists the top level, directories first: %v", got)
	}
	if cs[0].Desc != "dir" {
		t.Fatalf("a directory is marked: %+v", cs[0])
	}
	_, cs = completions("look at @internal/", nil, nil, nil, nil)
	if got := texts(cs); !slices.Equal(got, []string{"@internal/agent/", "@internal/tui/"}) {
		t.Fatalf("@internal/ lists its children: %v", got)
	}
	_, cs = completions("@internal/tui/k", nil, nil, nil, nil)
	if got := texts(cs); !slices.Equal(got, []string{"@internal/tui/keys.go"}) {
		t.Fatalf("prefix inside a directory: %v", got)
	}
	_, cs = completions("@viw", nil, nil, nil, nil)
	if got := texts(cs); len(got) == 0 || got[0] != "@internal/tui/view.go" {
		t.Fatalf("a bare name is fuzzy over the whole tree: %v", got)
	}
	_, cs = completions("@agent", nil, nil, nil, nil)
	if got := texts(cs); len(got) < 2 || got[0] != "@internal/agent/" {
		t.Fatalf("directories match by name too: %v", got)
	}
}

func TestMentionLineRange(t *testing.T) {
	dir := chdir(t)
	writeText(t, dir, "src/a.go", "one\ntwo\nthree\n")
	t.Chdir(dir)

	_, cs := completions("@src/a.go:", nil, nil, nil, nil)
	if len(cs) != 1 || cs[0].Text != "@src/a.go:1-3" || cs[0].Desc != "3 lines" {
		t.Fatalf("path: offers the line range: %+v", cs)
	}
	_, cs = completions("@src/a.go:2-", nil, nil, nil, nil)
	if len(cs) != 1 || cs[0].Text != "@src/a.go:2-3" {
		t.Fatalf("path:N- completes the end: %+v", cs)
	}
	out := expandMentions("see @src/a.go:2-3")
	if !strings.Contains(out, filepath.Join(dir, "src", "a.go")+" (lines 2-3)") {
		t.Fatalf("path:N-M is a line range: %q", out)
	}
	out = expandMentions("see @src/a.go#2")
	if !strings.Contains(out, "(lines 2)") {
		t.Fatalf("path#N still works: %q", out)
	}
}

func TestRankCommandsFuzzyAndRecent(t *testing.T) {
	all := []cand{{Text: "/model"}, {Text: "/memory"}, {Text: "/new-context"}, {Text: "/context"}, {Text: "/compact"}, {Text: "/theme"}}
	got := texts(rankCommands(all, "/con", nil))
	if !slices.Equal(got, []string{"/context", "/new-context"}) {
		t.Fatalf("/con: %v", got)
	}
	got = texts(rankCommands(all, "/mdl", nil))
	if !slices.Equal(got, []string{"/model"}) {
		t.Fatalf("/mdl is a subsequence of /model: %v", got)
	}
	got = texts(rankCommands(all, "/m", map[string]int{"/model": 0}))
	if !slices.Equal(got, []string{"/model", "/memory"}) {
		t.Fatalf("recent first within a tier: %v", got)
	}
	got = texts(rankCommands(all, "/", map[string]int{"/theme": 0, "/compact": 1}))
	if len(got) != len(all) || got[0] != "/theme" || got[1] != "/compact" {
		t.Fatalf("a bare / lists recent commands first: %v", got)
	}
}

func TestSlashMenuRemembersUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slash_recent.json")
	old := slashMRUPath
	slashMRUPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { slashMRUPath = old })

	m := compactCmdModel()
	m.noteSlashUse("/mouse on")
	m.noteSlashUse("/not-a-command")
	_, cs := m.promptCompletions("/m")
	if len(cs) == 0 || cs[0].Text != "/mouse" {
		t.Fatalf("the recently used command comes first: %v", texts(cs))
	}

	// A new model reads the list back from disk.
	m2 := compactCmdModel()
	if r := m2.slashRecent().rank(); len(r) != 1 || r["/mouse"] != 0 {
		t.Fatalf("persisted recent commands = %v", r)
	}
}

func TestSlashArgumentCompletion(t *testing.T) {
	_, cs := completions("/theme ", nil, nil, nil, nil)
	if got := texts(cs); !slices.Equal(got, []string{"auto", "dark", "light", "mono"}) {
		t.Fatalf("/theme arguments: %v", got)
	}
	_, cs = completions("/theme d", nil, nil, nil, nil)
	if got := texts(cs); !slices.Equal(got, []string{"dark"}) {
		t.Fatalf("/theme d: %v", got)
	}
	_, cs = completions("/plugins ", nil, nil, nil, nil)
	if got := texts(cs); !slices.Equal(got, []string{"disable", "enable", "list", "reload"}) {
		t.Fatalf("/plugins arguments: %v", got)
	}
	for _, in := range []string{"/btw ", "/resume ", "/goal "} {
		if _, cs = completions(in, nil, nil, nil, nil); len(cs) != 0 {
			t.Fatalf("%q takes free text, got %v", in, texts(cs))
		}
	}
	if got := argChoices("/schedule"); got != nil {
		t.Fatalf("/schedule has no fixed first word: %v", got)
	}
}
