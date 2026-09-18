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

	nodeFilter := []ast.Node{(*ast.CallExpr)(nil)}
	insp.WithStack(nodeFilter, func(n ast.Node, push bool, stack []ast.Node) bool {
		if !push {
			return true
		}
		call := n.(*ast.CallExpr)

		if strings.HasSuffix(pass.Fset.Position(call.Pos()).Filename, "_test.go") {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != sendMethod {
			return true
		}

		t := pass.TypesInfo.TypeOf(sel.X)
		if t == nil || !strings.HasSuffix(t.String(), "tea.Program") {
			return true
		}

		if detached(stack) {
			return true
		}

		if hasNolint(pass, stack) {
			return true
		}
		pass.Reportf(call.Pos(),
			"synchronous (*tea.Program).Send can deadlock the TUI event loop: detach it (go x.Send(...)) or, if this runs on a background goroutine, mark it //nolint:uilock")
		return true
	})
	return nil, nil
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
