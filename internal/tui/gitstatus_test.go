package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestParseGitNumstat(t *testing.T) {
	added, removed := parseGitNumstat("12\t3\tfile.go\n-\t5\tbin.dat\n1\t1\trenamed.go\n")
	if added != 13 || removed != 9 {
		t.Fatalf("numstat added=%d removed=%d, want 13/9", added, removed)
	}
}

func TestCountUntracked(t *testing.T) {
	out := " M tracked.go\n?? new1.go\n?? new2.go\nA staged.go\n"
	if got := countUntracked(out); got != 2 {
		t.Fatalf("untracked=%d, want 2", got)
	}
}

func TestLoadGitStatusInThisRepo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := loadGitStatus(ctx, ".")
	if err != nil {
		t.Fatalf("load git status: %v", err)
	}
	root, err := gitRawAt(ctx, ".", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatalf("resolve git root: %v", err)
	}
	wantRepo := filepath.Base(filepath.Clean(strings.TrimSpace(root)))
	if s.Repo != wantRepo {
		t.Fatalf("repo=%q, want %q", s.Repo, wantRepo)
	}
	if strings.TrimSpace(s.Branch) == "" {
		t.Fatal("branch should resolve in this repository")
	}
}

func TestGitStatusRender(t *testing.T) {
	s := gitStatus{Repo: "K-brain", Branch: "main", Added: 3, Removed: 1, Untracked: 2}
	out := s.render()
	plain := ansi.Strip(out)
	if !strings.HasPrefix(plain, "K-brain@main") {
		t.Fatalf("render should start with repo@branch, got %q", plain)
	}
	for _, want := range []string{"+3", "-1", "?2"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("render missing %s: %q", want, plain)
		}
	}
	empty := (gitStatus{}).render()
	if empty != "" {
		t.Fatalf("empty status should render empty, got %q", empty)
	}
}

func TestInfoRowShowsGit(t *testing.T) {
	m := compactCmdModel()
	m.width = 140
	m.git = gitStatus{Repo: "K-brain", Branch: "main", Added: 2}
	v := ansi.Strip(m.footerHints())
	if !strings.Contains(v, "K-brain@main") || !strings.Contains(v, "+2") {
		t.Fatalf("status row should carry the git readout, got %q", v)
	}
	m.git = gitStatus{}
	v = ansi.Strip(m.footerHints())
	if strings.Contains(v, "@") {
		t.Fatalf("without git data the status row should not show an empty @, got %q", v)
	}
}
