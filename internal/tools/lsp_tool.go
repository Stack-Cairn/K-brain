package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/lsp"
)

// LSPQuerier is the code-intelligence side of the LSP manager the lsp tool uses.
type LSPQuerier interface {
	Definition(ctx context.Context, path string, line, col int) ([]lsp.Location, error)
	References(ctx context.Context, path string, line, col int) ([]lsp.Location, error)
	Hover(ctx context.Context, path string, line, col int) (string, error)
	DocumentSymbols(ctx context.Context, path string) ([]lsp.Symbol, error)
	WorkspaceSymbols(ctx context.Context, path, dir, query string) ([]lsp.Symbol, error)
}

// LSPToolAvailable reports whether the installed LSP manager can answer lsp tool queries.
func LSPToolAvailable() bool {
	_, ok := LSP.(LSPQuerier)
	return ok
}

const (
	lspMaxLocations = 100
	lspMaxSymbols   = 300
)

// LSPTool is the read-only code-intelligence tool backed by the configured language
// servers. Permission-wise it is a read of path: Read rules and workspace roots apply.
func LSPTool() Tool {
	return Tool{
		Def: ai.NewTool("lsp",
			"Code intelligence from language servers (gopls is built in; others come from the \"lsp\" config block). Prefer it over grep/read for navigating code. Operations: definition (where the symbol at path:line:character is defined), references (all usages of that symbol), hover (its type and docs), documentSymbol (outline of path), workspaceSymbol (search symbols by query across the project; path optionally picks the language server). line and character are 1-based, matching read's line numbers.",
			`{"type":"object","properties":{"operation":{"type":"string","enum":["definition","references","hover","documentSymbol","workspaceSymbol"],"description":"Query to run"},"path":{"type":"string","description":"Source file. Required except for workspaceSymbol"},"line":{"type":"integer","minimum":1,"description":"1-based line of the symbol (definition, references, hover)"},"character":{"type":"integer","minimum":1,"description":"1-based column of any character of the symbol on that line"},"query":{"type":"string","description":"Symbol name or fragment (workspaceSymbol)"}},"required":["operation"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Operation string `json:"operation"`
				Path      string `json:"path"`
				Line      int    `json:"line"`
				Character int    `json:"character"`
				Query     string `json:"query"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			q, ok := LSP.(LSPQuerier)
			if !ok {
				return "", errors.New("language servers are not available in this session")
			}
			path := ""
			if a.Path != "" {
				var err error
				if path, err = lspAuthorizePath(ctx, a.Path); err != nil {
					return "", err
				}
			} else if a.Operation != "workspaceSymbol" {
				return "", errors.New("lsp." + a.Operation + " requires path")
			}
			switch a.Operation {
			case "definition", "references":
				find := q.Definition
				if a.Operation == "references" {
					find = q.References
				}
				locs, err := find(ctx, path, a.Line, a.Character)
				if err != nil {
					return "", err
				}
				return formatLSPLocations(ctx, a.Operation, locs), nil
			case "hover":
				text, err := q.Hover(ctx, path, a.Line, a.Character)
				if err != nil {
					return "", err
				}
				if text == "" {
					return "No hover information at this position.", nil
				}
				return truncate(text), nil
			case "documentSymbol":
				syms, err := q.DocumentSymbols(ctx, path)
				if err != nil {
					return "", err
				}
				return formatLSPSymbols(ctx, syms, false), nil
			case "workspaceSymbol":
				if strings.TrimSpace(a.Query) == "" {
					return "", errors.New("lsp.workspaceSymbol requires query")
				}
				syms, err := q.WorkspaceSymbols(ctx, path, WorkingDir(ctx), a.Query)
				if err != nil {
					return "", err
				}
				return formatLSPSymbols(ctx, syms, true), nil
			}
			return "", fmt.Errorf("unknown lsp operation %q", a.Operation)
		},
	}
}

// lspAuthorizePath gates the queried file like Read: confined to the run's workspace
// roots when it has them (the backend), otherwise resolved like the CLI read tool.
func lspAuthorizePath(ctx context.Context, raw string) (string, error) {
	if _, explicit := ctx.Value(workspaceRootsKey{}).([]WorkspaceRoot); explicit || WorkingDir(ctx) != "" {
		return liveagentAuthorizePath(ctx, "lsp", raw, "read", false)
	}
	path, err := resolveFilePath(ctx, raw)
	if err != nil {
		return "", err
	}
	if deny := checkGate(ctx, "lsp", path); deny != "" {
		return "", errors.New(deny)
	}
	return path, nil
}

// lspReadable reports whether a result location may be quoted: results can point into
// dependencies outside the authorized roots, which are listed but not read.
func lspReadable(ctx context.Context, path string) bool {
	if _, explicit := ctx.Value(workspaceRootsKey{}).([]WorkspaceRoot); explicit || WorkingDir(ctx) != "" {
		_, err := liveagentPath(ctx, path, "read", "lsp", false)
		return err == nil
	}
	return true
}

func lspDisplayPath(ctx context.Context, path string) string {
	if dir := WorkingDir(ctx); dir != "" {
		if rel, err := filepath.Rel(dir, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return filepath.ToSlash(rel)
		}
	}
	return path
}

func formatLSPLocations(ctx context.Context, op string, locs []lsp.Location) string {
	if len(locs) == 0 {
		if op == "definition" {
			return "No definition found."
		}
		return "No references found."
	}
	var b strings.Builder
	if op == "references" {
		fmt.Fprintf(&b, "%d reference(s):\n", len(locs))
	}
	lines := map[string][]string{}
	for i, l := range locs {
		if i == lspMaxLocations {
			fmt.Fprintf(&b, "... and %d more\n", len(locs)-i)
			break
		}
		fmt.Fprintf(&b, "%s:%d:%d", lspDisplayPath(ctx, l.Path), l.Line, l.Col)
		if src, ok := lines[l.Path]; ok || lspReadable(ctx, l.Path) {
			if !ok {
				if data, err := os.ReadFile(l.Path); err == nil && !isBinary(data) {
					src = strings.Split(string(data), "\n")
				}
				lines[l.Path] = src
			}
			if l.Line >= 1 && l.Line <= len(src) {
				text := strings.TrimSpace(src[l.Line-1])
				if len(text) > 200 {
					text = text[:200] + "…"
				}
				b.WriteString("\t" + text)
			}
		}
		b.WriteByte('\n')
	}
	return truncate(strings.TrimRight(b.String(), "\n"))
}

func formatLSPSymbols(ctx context.Context, syms []lsp.Symbol, withPath bool) string {
	if len(syms) == 0 {
		return "No symbols found."
	}
	var b strings.Builder
	for i, s := range syms {
		if i == lspMaxSymbols {
			fmt.Fprintf(&b, "... and %d more\n", len(syms)-i)
			break
		}
		b.WriteString(strings.Repeat("  ", s.Depth))
		b.WriteString(s.Kind + " " + s.Name)
		if s.Detail != "" {
			b.WriteString(" " + s.Detail)
		}
		if withPath {
			if s.Container != "" {
				b.WriteString(" (" + s.Container + ")")
			}
			fmt.Fprintf(&b, " — %s:%d:%d", lspDisplayPath(ctx, s.Path), s.Line, s.Col)
		} else {
			fmt.Fprintf(&b, " — line %d", s.Line)
		}
		b.WriteByte('\n')
	}
	return truncate(strings.TrimRight(b.String(), "\n"))
}
