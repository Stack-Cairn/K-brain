package uilock

import (
	"go/ast"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

var Analyzer = &analysis.Analyzer{
	Name:     "uilock",
	Doc:      "flags synchronous tea.Program.Send calls that can deadlock the TUI event loop",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

const sendMethod = "Send"

func run(pass *analysis.Pass) (any, error) {

	if !strings.Contains(pass.Pkg.Path(), "internal/tui") {
		return nil, nil
	}

	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	nodeFilter := []ast.Node{(*ast.CallExpr)(nil), (*ast.SelectorExpr)(nil)}
	insp.WithStack(nodeFilter, func(n ast.Node, push bool, stack []ast.Node) bool {
		if !push {
			return true
		}
		if strings.HasSuffix(pass.Fset.Position(n.Pos()).Filename, "_test.go") {
			return true
		}
		switch n := n.(type) {
		case *ast.CallExpr:
			sel, ok := n.Fun.(*ast.SelectorExpr)
			if !ok || !isProgramSend(pass, sel) {
				return true
			}
			if detached(stack) || hasNolint(pass, stack) {
				return true
			}
			pass.Reportf(n.Pos(),
				"synchronous (*tea.Program).Send can deadlock the TUI event loop: detach it (go x.Send(...)) or, if this runs on a background goroutine, mark it //nolint:uilock")
		case *ast.SelectorExpr:
			// p.Send as a value (a callback, an argument) hides a synchronous
			// Send from the check above.
			if !isProgramSend(pass, n) {
				return true
			}
			if len(stack) >= 2 {
				if call, ok := stack[len(stack)-2].(*ast.CallExpr); ok && call.Fun == n {
					return true
				}
			}
			if hasNolint(pass, stack) {
				return true
			}
			pass.Reportf(n.Pos(),
				"(*tea.Program).Send used as a function value runs synchronously wherever it is called: pass a non-blocking sender instead, or mark it //nolint:uilock")
		}
		return true
	})
	return nil, nil
}

func isProgramSend(pass *analysis.Pass, sel *ast.SelectorExpr) bool {
	if sel.Sel.Name != sendMethod {
		return false
	}
	t := pass.TypesInfo.TypeOf(sel.X)
	return t != nil && strings.HasSuffix(t.String(), "tea.Program")
}

func detached(stack []ast.Node) bool {

	if len(stack) >= 2 {
		if _, isGo := stack[len(stack)-2].(*ast.GoStmt); isGo {
			return true
		}
	}

	for i := len(stack) - 1; i >= 2; i-- {
		if _, isLit := stack[i].(*ast.FuncLit); isLit {
			_, inv := stack[i-1].(*ast.CallExpr)
			_, isGo := stack[i-2].(*ast.GoStmt)
			if inv && isGo {
				return true
			}
		}
		if _, isDecl := stack[i].(*ast.FuncDecl); isDecl {
			return false
		}
	}
	return false
}

func hasNolint(pass *analysis.Pass, stack []ast.Node) bool {
	if len(stack) == 0 {
		return false
	}
	call := stack[len(stack)-1]
	fset := pass.Fset
	line := fset.Position(call.Pos()).Line
	file := fset.File(call.Pos())
	if file == nil {
		return false
	}

	for _, f := range pass.Files {
		if fset.File(f.Pos()) != file {
			continue
		}
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				cl := fset.Position(c.Pos()).Line
				if (cl == line || cl == line-1) && strings.Contains(c.Text, "nolint:uilock") {
					return true
				}
			}
		}
	}
	_ = token.NoPos
	return false
}
