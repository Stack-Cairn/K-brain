package lsp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

const queryTimeout = 15 * time.Second

// Location is a position in a file; Line and Col are 1-based.
type Location struct {
	Path string
	Line int
	Col  int
}

// Symbol is one entry of a document or workspace symbol listing. Depth is the nesting
// level in a hierarchical document outline (0 at top level).
type Symbol struct {
	Name      string
	Kind      string
	Detail    string
	Container string
	Location
	Depth int
}

// ErrNoServer reports a file no configured language server handles.
var ErrNoServer = errors.New("no language server is configured for this file type")

// Definition returns where the symbol at path:line:col is defined.
func (m *Manager) Definition(ctx context.Context, path string, line, col int) ([]Location, error) {
	var raw json.RawMessage
	if err := m.positionRequest(ctx, "textDocument/definition", path, line, col, nil, &raw); err != nil {
		return nil, err
	}
	return parseLocations(raw), nil
}

// References returns every usage of the symbol at path:line:col, its declaration included.
func (m *Manager) References(ctx context.Context, path string, line, col int) ([]Location, error) {
	var raw json.RawMessage
	extra := map[string]any{"context": map[string]any{"includeDeclaration": true}}
	if err := m.positionRequest(ctx, "textDocument/references", path, line, col, extra, &raw); err != nil {
		return nil, err
	}
	return parseLocations(raw), nil
}

// Hover returns the type information and documentation for the symbol at path:line:col.
func (m *Manager) Hover(ctx context.Context, path string, line, col int) (string, error) {
	var res struct {
		Contents json.RawMessage `json:"contents"`
	}
	if err := m.positionRequest(ctx, "textDocument/hover", path, line, col, nil, &res); err != nil {
		return "", err
	}
	return strings.TrimSpace(hoverText(res.Contents)), nil
}

// DocumentSymbols returns the outline of path.
func (m *Manager) DocumentSymbols(ctx context.Context, path string) ([]Symbol, error) {
	cs, abs, _, err := m.openDocument(ctx, path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	var raw []json.RawMessage
	if err := cs.cli.request(ctx, "textDocument/documentSymbol", map[string]any{"textDocument": map[string]any{"uri": fileURI(abs)}}, &raw); err != nil {
		return nil, err
	}
	var out []Symbol
	for _, item := range raw {
		appendDocumentSymbol(&out, abs, item, 0, "")
	}
	return out, nil
}

// WorkspaceSymbols searches symbols by name. path picks the language server (any file
// of the project); without it every running server whose root contains dir is asked.
func (m *Manager) WorkspaceSymbols(ctx context.Context, path, dir, query string) ([]Symbol, error) {
	var clients []*clientState
	if path != "" {
		cs, _, _, err := m.openDocument(ctx, path)
		if err != nil {
			return nil, err
		}
		clients = append(clients, cs)
	} else {
		m.mu.Lock()
		for _, cs := range m.clients {
			if dir == "" || within(cs.root, dir) || within(dir, cs.root) {
				clients = append(clients, cs)
			}
		}
		m.mu.Unlock()
		if len(clients) == 0 {
			return nil, errors.New("no language server is running for this workspace; pass path to a source file to start one")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	var out []Symbol
	var errs []error
	for _, cs := range clients {
		var raw []json.RawMessage
		if err := cs.cli.request(ctx, "workspace/symbol", map[string]any{"query": query}, &raw); err != nil {
			errs = append(errs, err)
			continue
		}
		for _, item := range raw {
			var s struct {
				Name          string `json:"name"`
				Kind          int    `json:"kind"`
				ContainerName string `json:"containerName"`
				Location      struct {
					URI   string   `json:"uri"`
					Range lspRange `json:"range"`
				} `json:"location"`
			}
			if json.Unmarshal(item, &s) != nil || s.Name == "" {
				continue
			}
			out = append(out, Symbol{Name: s.Name, Kind: symbolKind(s.Kind), Container: s.ContainerName, Location: s.Location.Range.location(s.Location.URI)})
		}
	}
	if len(out) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

func (m *Manager) positionRequest(ctx context.Context, method, path string, line, col int, extra map[string]any, result any) error {
	if line < 1 || col < 1 {
		return errors.New("line and character are 1-based and required")
	}
	cs, abs, data, err := m.openDocument(ctx, path)
	if err != nil {
		return err
	}
	params := map[string]any{
		"textDocument": map[string]any{"uri": fileURI(abs)},
		"position":     map[string]any{"line": line - 1, "character": utf16Col(data, line, col)},
	}
	for k, v := range extra {
		params[k] = v
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	return cs.cli.request(ctx, method, params, result)
}

// openDocument starts (or reuses) the server for path and makes sure it has the file's
// current content, so a query sees what is on disk rather than a stale buffer.
func (m *Manager) openDocument(ctx context.Context, path string) (*clientState, string, []byte, error) {
	if m == nil {
		return nil, "", nil, errors.New("language servers are not available")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, "", nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, "", nil, err
	}
	cs, err := m.clientFor(ctx, abs)
	if err != nil {
		return nil, "", nil, fmt.Errorf("language server unavailable: %w", err)
	}
	if cs == nil {
		return nil, "", nil, fmt.Errorf("%w (%s)", ErrNoServer, filepath.Ext(abs))
	}
	cs.syncMu.Lock()
	defer cs.syncMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, "", nil, errors.New("language servers are shut down")
	}
	if cs.docs[abs] > 0 && cs.sums[abs] == sha256.Sum256(data) {
		m.mu.Unlock()
		return cs, abs, data, nil
	}
	version := cs.nextVersion(abs, data)
	m.mu.Unlock()
	cs.syncDocument(abs, version, data)
	return cs, abs, data, nil
}

// nextVersion bumps the document version of abs and records the content about to be
// sent. The caller holds Manager.mu.
func (cs *clientState) nextVersion(abs string, data []byte) int {
	cs.docs[abs]++
	if cs.sums == nil {
		cs.sums = map[string][sha256.Size]byte{}
	}
	cs.sums[abs] = sha256.Sum256(data)
	return cs.docs[abs]
}

// syncDocument sends didOpen for the first version of abs and a full-text didChange
// afterwards.
func (cs *clientState) syncDocument(abs string, version int, data []byte) {
	uri := fileURI(abs)
	if version == 1 {
		cs.cli.notify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{
				"uri": uri, "languageId": strings.TrimPrefix(filepath.Ext(abs), "."), "version": version, "text": string(data),
			},
		})
	} else {
		cs.cli.notify("textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": version},
			"contentChanges": []map[string]any{{"text": string(data)}},
		})
	}
}

type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

func (r lspRange) location(uri string) Location {
	return Location{Path: uriPath(uri), Line: r.Start.Line + 1, Col: r.Start.Character + 1}
}

// parseLocations accepts Location, Location[], LocationLink[] or null.
func parseLocations(raw json.RawMessage) []Location {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var items []json.RawMessage
	if raw[0] == '[' {
		if json.Unmarshal(raw, &items) != nil {
			return nil
		}
	} else {
		items = []json.RawMessage{raw}
	}
	var out []Location
	for _, item := range items {
		var l struct {
			URI                  string    `json:"uri"`
			Range                lspRange  `json:"range"`
			TargetURI            string    `json:"targetUri"`
			TargetSelectionRange *lspRange `json:"targetSelectionRange"`
			TargetRange          lspRange  `json:"targetRange"`
		}
		if json.Unmarshal(item, &l) != nil {
			continue
		}
		switch {
		case l.URI != "":
			out = append(out, l.Range.location(l.URI))
		case l.TargetURI != "" && l.TargetSelectionRange != nil:
			out = append(out, l.TargetSelectionRange.location(l.TargetURI))
		case l.TargetURI != "":
			out = append(out, l.TargetRange.location(l.TargetURI))
		}
	}
	return out
}

// hoverText flattens MarkupContent, MarkedString or MarkedString[].
func hoverText(raw json.RawMessage) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	switch raw[0] {
	case '"':
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	case '[':
		var items []json.RawMessage
		_ = json.Unmarshal(raw, &items)
		parts := make([]string, 0, len(items))
		for _, item := range items {
			if s := strings.TrimSpace(hoverText(item)); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	var v struct {
		Kind     string `json:"kind"`
		Language string `json:"language"`
		Value    string `json:"value"`
	}
	_ = json.Unmarshal(raw, &v)
	if v.Language != "" {
		return "```" + v.Language + "\n" + v.Value + "\n```"
	}
	return v.Value
}

// appendDocumentSymbol flattens a DocumentSymbol tree or a SymbolInformation entry.
func appendDocumentSymbol(out *[]Symbol, abs string, raw json.RawMessage, depth int, container string) {
	var s struct {
		Name           string            `json:"name"`
		Detail         string            `json:"detail"`
		Kind           int               `json:"kind"`
		SelectionRange *lspRange         `json:"selectionRange"`
		Range          lspRange          `json:"range"`
		Children       []json.RawMessage `json:"children"`
		ContainerName  string            `json:"containerName"`
		Location       *struct {
			URI   string   `json:"uri"`
			Range lspRange `json:"range"`
		} `json:"location"`
	}
	if json.Unmarshal(raw, &s) != nil || s.Name == "" {
		return
	}
	sym := Symbol{Name: s.Name, Kind: symbolKind(s.Kind), Detail: s.Detail, Container: container, Depth: depth}
	switch {
	case s.Location != nil:
		sym.Location = s.Location.Range.location(s.Location.URI)
		sym.Container = s.ContainerName
	case s.SelectionRange != nil:
		sym.Location = s.SelectionRange.location(fileURI(abs))
	default:
		sym.Location = s.Range.location(fileURI(abs))
	}
	*out = append(*out, sym)
	for _, child := range s.Children {
		appendDocumentSymbol(out, abs, child, depth+1, s.Name)
	}
}

// utf16Col converts a 1-based character column on a 1-based line to the 0-based
// UTF-16 offset LSP positions use.
func utf16Col(data []byte, line, col int) int {
	lines := strings.SplitN(string(data), "\n", line+1)
	if line-1 >= len(lines) {
		return col - 1
	}
	text := lines[line-1]
	units := 0
	for i := 0; i < col-1; i++ {
		if text == "" {
			units += col - 1 - i
			break
		}
		r, size := utf8.DecodeRuneInString(text)
		text = text[size:]
		units += len(utf16.Encode([]rune{r}))
	}
	return units
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

var symbolKinds = [...]string{"", "File", "Module", "Namespace", "Package", "Class", "Method", "Property", "Field", "Constructor", "Enum", "Interface", "Function", "Variable", "Constant", "String", "Number", "Boolean", "Array", "Object", "Key", "Null", "EnumMember", "Struct", "Event", "Operator", "TypeParameter"}

func symbolKind(k int) string {
	if k > 0 && k < len(symbolKinds) {
		return symbolKinds[k]
	}
	return "Symbol"
}
