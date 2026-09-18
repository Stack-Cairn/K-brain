package uilock

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	testdata := t.TempDir()
	if err := os.CopyFS(testdata, os.DirFS(analysistest.TestData())); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(testdata, "src", "internal", "tui", "a.go")
	src, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	annotated := strings.Replace(string(src), "p.Send(m)", "p.Send(m) // want \"synchronous\"", 1)
	annotated = strings.Replace(annotated,
		"func whitelistedSend(p *tea.Program, m tea.Msg) {",
		"func whitelistedSend(p *tea.Program, m tea.Msg) {\n//nolint:uilock", 1)
	if err := os.WriteFile(fixture, []byte(annotated), 0o600); err != nil {
		t.Fatal(err)
	}
	analysistest.Run(t, testdata, Analyzer, "internal/tui", "other")
}
