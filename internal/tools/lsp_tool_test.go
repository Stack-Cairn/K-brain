package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/lsp"
)

type stubQuerier struct {
	stubLSP
	locs    []lsp.Location
	symbols []lsp.Symbol
	hover   string
	paths   []string
}

func (s *stubQuerier) Definition(ctx context.Context, path string, line, col int) ([]lsp.Location, error) {
	s.paths = append(s.paths, path)
	return s.locs, nil
}
func (s *stubQuerier) References(ctx context.Context, path string, line, col int) ([]lsp.Location, error) {
	s.paths = append(s.paths, path)
	return s.locs, nil
}
func (s *stubQuerier) Hover(ctx context.Context, path string, line, col int) (string, error) {
	return s.hover, nil
}
func (s *stubQuerier) DocumentSymbols(ctx context.Context, path string) ([]lsp.Symbol, error) {
	return s.symbols, nil
}
func (s *stubQuerier) WorkspaceSymbols(ctx context.Context, path, dir, query string) ([]lsp.Symbol, error) {
	return s.symbols, nil
}

func runLSPTool(ctx context.Context, args map[string]any) Result {
	raw, _ := json.Marshal(args)
	return ExecuteResult(ctx, []Tool{LSPTool()}, "lsp", raw, false)
}

func TestLSPToolUnavailableWithoutQuerier(t *testing.T) {
	saved := LSP
	defer func() { LSP = saved }()
	LSP = &stubLSP{}
	if LSPToolAvailable() {
		t.Fatal("a diagnostics-only manager cannot answer queries")
	}
	if res := runLSPTool(context.Background(), map[string]any{"operation": "hover", "path": "x.go", "line": 1, "character": 1}); !res.Failed || !strings.Contains(res.Text, "not available") {
		t.Fatalf("res = %+v", res)
	}
}

func TestLSPToolConfinedToWorkspaceRootsAndQuotesOnlyReadableResults(t *testing.T) {
	dir := t.TempDir()
	outsideDir := t.TempDir()
	main := filepath.Join(dir, "main.go")
	if err := os.WriteFile(main, []byte("package main\n\nfunc run() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dep := filepath.Join(outsideDir, "dep.go")
	if err := os.WriteFile(dep, []byte("package dep\nfunc Secret() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := &stubQuerier{locs: []lsp.Location{{Path: main, Line: 3, Col: 6}, {Path: dep, Line: 2, Col: 6}}}
	saved := LSP
	defer func() { LSP = saved }()
	LSP = stub
	if !LSPToolAvailable() {
		t.Fatal("querier not detected")
	}
	ctx := WithWorkspaceRoots(WithWorkingDir(context.Background(), dir), []WorkspaceRoot{{Path: dir, Access: "read"}})

	res := runLSPTool(ctx, map[string]any{"operation": "references", "path": "main.go", "line": 3, "character": 6})
	if res.Failed {
		t.Fatal(res.Text)
	}
	if !strings.Contains(res.Text, "2 reference(s):") || !strings.Contains(res.Text, "main.go:3:6\tfunc run() {}") {
		t.Fatalf("missing in-root reference with snippet: %q", res.Text)
	}
	if !strings.Contains(res.Text, dep+":2:6") || strings.Contains(res.Text, "Secret") {
		t.Fatalf("out-of-root result must be listed without its source: %q", res.Text)
	}
	if len(stub.paths) != 1 || stub.paths[0] != main {
		t.Fatalf("queried paths = %v", stub.paths)
	}

	if res := runLSPTool(ctx, map[string]any{"operation": "definition", "path": dep, "line": 2, "character": 6}); !res.Failed || !strings.Contains(res.Text, "outside the authorized workspace roots") {
		t.Fatalf("query outside roots must be refused: %+v", res)
	}
	var gated []GateRequest
	gatedCtx := WithGate(ctx, func(req GateRequest) (GateDecision, string) {
		gated = append(gated, req)
		return GateReject, "no"
	})
	if res := runLSPTool(gatedCtx, map[string]any{"operation": "hover", "path": "main.go", "line": 1, "character": 1}); !res.Failed || len(gated) != 1 || gated[0].Tool != "lsp" || gated[0].Command != main {
		t.Fatalf("gate not consulted as a read of the path: %+v %+v", res, gated)
	}
}

func TestLSPToolOperations(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main.go")
	if err := os.WriteFile(main, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := &stubQuerier{
		hover: "func main()",
		symbols: []lsp.Symbol{
			{Name: "T", Kind: "Struct", Location: lsp.Location{Path: main, Line: 3, Col: 6}},
			{Name: "F", Kind: "Field", Detail: "int", Container: "T", Depth: 1, Location: lsp.Location{Path: main, Line: 4, Col: 2}},
		},
	}
	saved := LSP
	defer func() { LSP = saved }()
	LSP = stub
	ctx := WithWorkingDir(context.Background(), dir)

	if res := runLSPTool(ctx, map[string]any{"operation": "hover", "path": "main.go", "line": 1, "character": 1}); res.Text != "func main()" {
		t.Fatalf("hover = %+v", res)
	}
	if res := runLSPTool(ctx, map[string]any{"operation": "documentSymbol", "path": "main.go"}); res.Text != "Struct T — line 3\n  Field F int — line 4" {
		t.Fatalf("documentSymbol = %q", res.Text)
	}
	if res := runLSPTool(ctx, map[string]any{"operation": "workspaceSymbol", "query": "F"}); !strings.Contains(res.Text, "Field F int (T) — main.go:4:2") {
		t.Fatalf("workspaceSymbol = %q", res.Text)
	}
	if res := runLSPTool(ctx, map[string]any{"operation": "workspaceSymbol"}); !res.Failed || !strings.Contains(res.Text, "requires query") {
		t.Fatalf("missing query = %+v", res)
	}
	if res := runLSPTool(ctx, map[string]any{"operation": "definition"}); !res.Failed || !strings.Contains(res.Text, "requires path") {
		t.Fatalf("missing path = %+v", res)
	}
	stub.locs = nil
	if res := runLSPTool(ctx, map[string]any{"operation": "definition", "path": "main.go", "line": 1, "character": 1}); res.Text != "No definition found." {
		t.Fatalf("empty definition = %q", res.Text)
	}
}
