package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func searchFixture(t *testing.T, files map[string]string) (context.Context, string) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return WithWorkingDir(context.Background(), dir), dir
}

func runSearch(t *testing.T, ctx context.Context, name, args string) Result {
	t.Helper()
	return ExecuteResult(ctx, All(), name, json.RawMessage(args), false)
}

func TestAllOffersSearchToolsAndBackendDropsThem(t *testing.T) {
	names := map[string]bool{}
	for _, tool := range All() {
		names[tool.Def.Function.Name] = true
	}
	for _, want := range []string{"bash", "read", "write", "edit", "grep", "glob", "list", "web_fetch"} {
		if !names[want] {
			t.Errorf("All() lacks %s", want)
		}
	}
	for _, tool := range WithoutCLIOnlyTools(All()) {
		switch tool.Def.Function.Name {
		case "grep", "glob", "list", "web_fetch":
			t.Errorf("WithoutCLIOnlyTools kept %s", tool.Def.Function.Name)
		}
	}
	if got := len(WithoutCLIOnlyTools(All())); got != 4 {
		t.Errorf("WithoutCLIOnlyTools left %d tools, want the 4 core tools", got)
	}
}

func TestCLIGrepModesAndFilters(t *testing.T) {
	ctx, _ := searchFixture(t, map[string]string{
		".git/HEAD":       "needle in git metadata\n",
		".gitignore":      "build/\n",
		"main.go":         "package main\n// Needle upper\nfunc needle() {}\n",
		"pkg/deep/x.go":   "var needle = 1\n",
		"pkg/notes.md":    "needle in markdown\n",
		"build/out.go":    "needle in ignored build output\n",
		"bin/tool.binary": "needle\x00binary",
	})
	content := runSearch(t, ctx, "grep", `{"pattern":"needle"}`)
	if content.Failed {
		t.Fatal(content.Text)
	}
	for _, want := range []string{"main.go:3: func needle() {}", "pkg/deep/x.go:1: var needle = 1", "pkg/notes.md:1: needle in markdown"} {
		if !strings.Contains(content.Text, want) {
			t.Errorf("content output lacks %q:\n%s", want, content.Text)
		}
	}
	for _, unwanted := range []string{"Needle upper", "build/out.go", ".git/HEAD", "tool.binary"} {
		if strings.Contains(content.Text, unwanted) {
			t.Errorf("content output has %q (case-sensitive default, ignore rules, binary skip):\n%s", unwanted, content.Text)
		}
	}

	// A glob without a slash matches by name at any depth.
	filtered := runSearch(t, ctx, "grep", `{"pattern":"needle","glob":"*.go","output_mode":"files_with_matches","ignore_case":true}`)
	if filtered.Failed {
		t.Fatal(filtered.Text)
	}
	if !strings.Contains(filtered.Text, "main.go (2, firstLine=2)") || !strings.Contains(filtered.Text, "pkg/deep/x.go (1, firstLine=1)") || strings.Contains(filtered.Text, "notes.md") {
		t.Errorf("files_with_matches with glob *.go:\n%s", filtered.Text)
	}

	count := runSearch(t, ctx, "grep", `{"pattern":"needle","output_mode":"count"}`)
	if !strings.Contains(count.Text, "matches=3") || !strings.Contains(count.Text, "files=3") {
		t.Errorf("count output:\n%s", count.Text)
	}

	withContext := runSearch(t, ctx, "grep", `{"pattern":"func needle","path":"main.go","context":1}`)
	if !strings.Contains(withContext.Text, "main.go:2: // Needle upper") {
		t.Errorf("context line missing:\n%s", withContext.Text)
	}
}

func TestCLIGrepBoundsOutput(t *testing.T) {
	var many strings.Builder
	for i := range 450 {
		fmt.Fprintf(&many, "hit %d\n", i)
	}
	files := map[string]string{
		"many.txt": many.String(),
		"long.txt": "hit " + strings.Repeat("x", 5000) + "\n",
	}
	for i := range 600 {
		files[fmt.Sprintf("f/%03d.txt", i)] = "hit\n"
	}
	ctx, _ := searchFixture(t, files)

	content := runSearch(t, ctx, "grep", `{"pattern":"hit","path":"many.txt"}`)
	if !strings.Contains(content.Text, "total=450 hasMore=true") || strings.Count(content.Text, "many.txt:") != 200 {
		t.Errorf("content mode should default to 200 rows:\n%.300s", content.Text)
	}
	long := runSearch(t, ctx, "grep", `{"pattern":"hit","path":"long.txt"}`)
	if !strings.Contains(long.Text, "… [4004 more bytes]") || len(long.Text) > 1300 {
		t.Errorf("long line should be clipped at 1000 bytes (%d bytes):\n%.200s", len(long.Text), long.Text)
	}
	files500 := runSearch(t, ctx, "grep", `{"pattern":"hit","path":"f","output_mode":"files_with_matches"}`)
	if !strings.Contains(files500.Text, "total=600 hasMore=true") || strings.Count(files500.Text, "firstLine=") != 500 {
		t.Errorf("files_with_matches should default to 500 rows:\n%.300s", files500.Text)
	}
	paged := runSearch(t, ctx, "grep", `{"pattern":"hit","path":"many.txt","head_limit":5,"offset":445}`)
	if !strings.Contains(paged.Text, "many.txt:450: hit 449") || strings.Count(paged.Text, "many.txt:") != 5 {
		t.Errorf("head_limit/offset paging:\n%s", paged.Text)
	}
}

func TestCLIGlobAndList(t *testing.T) {
	ctx, _ := searchFixture(t, map[string]string{
		"a.go":           "",
		"cmd/kn/main.go": "",
		"docs/x.md":      "",
	})
	top := runSearch(t, ctx, "glob", `{"pattern":"*.go"}`)
	if !strings.Contains(top.Text, "a.go") || strings.Contains(top.Text, "main.go") {
		t.Errorf("* should not cross directories:\n%s", top.Text)
	}
	deep := runSearch(t, ctx, "glob", `{"pattern":"**/*.go"}`)
	if !strings.Contains(deep.Text, "a.go") || !strings.Contains(deep.Text, "cmd/kn/main.go") || strings.Contains(deep.Text, "x.md") {
		t.Errorf("**/*.go:\n%s", deep.Text)
	}
	list := runSearch(t, ctx, "list", `{"depth":1}`)
	for _, want := range []string{"[FILE] a.go", "[DIR] cmd", "[DIR] docs"} {
		if !strings.Contains(list.Text, want) {
			t.Errorf("list lacks %q:\n%s", want, list.Text)
		}
	}
	if strings.Contains(list.Text, "kn") {
		t.Errorf("depth 1 descended:\n%s", list.Text)
	}
}

func TestCLIListStaysWithinBudgetAndSummarizesTheRest(t *testing.T) {
	files := map[string]string{}
	for i := range 400 {
		files[fmt.Sprintf("src/component_with_a_long_name_%03d.tsx", i)] = ""
	}
	for i := range 50 {
		files[fmt.Sprintf("src/z_note_%03d.md", i)] = ""
	}
	ctx, _ := searchFixture(t, files)
	out := runSearch(t, ctx, "list", `{"path":"src","max_results":1000}`)
	if out.Failed {
		t.Fatal(out.Text)
	}
	if len(out.Text) > cliSearchCaps.listChars+400 {
		t.Errorf("list output is %d bytes, budget %d", len(out.Text), cliSearchCaps.listChars)
	}
	if !strings.Contains(out.Text, "hasMore=true") || !strings.Contains(out.Text, "more entries not shown (") || !strings.Contains(out.Text, "50 .md") || !strings.Contains(out.Text, ".tsx") {
		t.Errorf("list should summarize the entries past its budget by extension:\n...%s", out.Text[max(0, len(out.Text)-300):])
	}
}

func TestCLISearchToolsSkipThePermissionGate(t *testing.T) {
	ctx, _ := searchFixture(t, map[string]string{"a.txt": "needle\n"})
	asked := false
	ctx = WithGate(ctx, func(req GateRequest) (GateDecision, string) {
		asked = true
		return GateReject, "should not be asked"
	})
	for name, args := range map[string]string{"grep": `{"pattern":"needle"}`, "glob": `{"pattern":"*"}`, "list": `{}`} {
		if res := runSearch(t, ctx, name, args); res.Failed || !strings.Contains(res.Text, "a.txt") {
			t.Errorf("%s: %s", name, res.Text)
		}
	}
	if asked {
		t.Error("a read-only search tool asked the permission gate")
	}
}

func TestCLISearchArgumentErrors(t *testing.T) {
	ctx, _ := searchFixture(t, map[string]string{"a.txt": "x\n"})
	for args, want := range map[string]string{
		`{"pattern":"x","bogus":1}`:           `unsupported argument "bogus"`,
		`{"pattern":"x","output_mode":"all"}`: "grep.output_mode is not one of the allowed values",
		`{}`:                                  "grep.pattern is required",
		`{"pattern":"("}`:                     "missing closing )",
	} {
		res := runSearch(t, ctx, "grep", args)
		if !res.Failed || !strings.Contains(res.Text, want) {
			t.Errorf("grep %s = %q, want an error with %q", args, res.Text, want)
		}
	}
}

func TestSearchTreeTimeoutReturnsPartialResults(t *testing.T) {
	_, dir := searchFixture(t, map[string]string{"a.txt": "needle\n"})
	caps := searchCaps{timeout: time.Nanosecond}
	out, err := searchTree(context.Background(), "Grep", dir, map[string]any{"pattern": "needle"}, searchOptions{caps: caps})
	if err != nil || !strings.Contains(out, "results are partial") {
		t.Errorf("own timeout should return partial results, got %q, %v", out, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := searchTree(cancelled, "Grep", dir, map[string]any{"pattern": "needle"}, searchOptions{caps: caps}); err == nil {
		t.Error("a cancelled turn should fail the search, not report partial results")
	}
}

func TestGateRequestForSearchToolsNamesTheSearchedPath(t *testing.T) {
	dir := t.TempDir()
	ctx := WithWorkingDir(context.Background(), dir)
	if got := GateRequestFor(ctx, "grep", json.RawMessage(`{"pattern":"x","path":"sub"}`)).Command; got != filepath.Join(dir, "sub") {
		t.Errorf("grep path = %q", got)
	}
	if got := GateRequestFor(ctx, "list", json.RawMessage(`{}`)).Command; got != dir {
		t.Errorf("list without path = %q, want the working directory", got)
	}
}
