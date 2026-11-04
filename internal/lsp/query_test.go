package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func drainChanges(f *fakeServer) int {
	n := 0
	for {
		select {
		case <-f.onChange:
			n++
		case <-time.After(100 * time.Millisecond):
			return n
		}
	}
}

func TestDefinitionOpensDocumentAndConvertsPositions(t *testing.T) {
	f := startFakeServer(t, nil)
	m := pipeManager(f)
	defer m.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	// "é" is one UTF-16 unit, "😀" two: column 4 on line 2 is UTF-16 offset 4.
	writeFile(t, path, "package main\n😀é x\n")
	other := filepath.Join(dir, "other.go")
	f.respond("textDocument/definition", `[{"targetUri":"`+fileURI(other)+`","targetRange":{"start":{"line":0,"character":0},"end":{"line":9,"character":0}},"targetSelectionRange":{"start":{"line":4,"character":5},"end":{"line":4,"character":8}}}]`)

	locs, err := m.Definition(context.Background(), path, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 1 || locs[0] != (Location{Path: other, Line: 5, Col: 6}) {
		t.Fatalf("locs = %+v", locs)
	}
	var p struct {
		TextDocument struct{ URI string } `json:"textDocument"`
		Position     lspPosition          `json:"position"`
	}
	if err := json.Unmarshal(f.lastParams("textDocument/definition"), &p); err != nil {
		t.Fatal(err)
	}
	if p.TextDocument.URI != fileURI(path) || p.Position != (lspPosition{Line: 1, Character: 4}) {
		t.Fatalf("params = %+v", p)
	}
	if drainChanges(f) != 1 {
		t.Fatal("want exactly one didOpen")
	}

	// An unchanged file is not re-sent; a changed one is.
	f.respond("textDocument/references", `[{"uri":"`+fileURI(path)+`","range":{"start":{"line":1,"character":3},"end":{"line":1,"character":4}}}]`)
	if locs, err := m.References(context.Background(), path, 2, 4); err != nil || len(locs) != 1 || locs[0].Line != 2 || locs[0].Col != 4 {
		t.Fatalf("references = %+v %v", locs, err)
	}
	if n := drainChanges(f); n != 0 {
		t.Fatalf("unchanged document re-sent %d times", n)
	}
	writeFile(t, path, "package main\n\nfunc x() {}\n")
	if _, err := m.References(context.Background(), path, 3, 6); err != nil {
		t.Fatal(err)
	}
	if n := drainChanges(f); n != 1 {
		t.Fatalf("changed document sent %d times", n)
	}
}

func TestHoverAndSymbols(t *testing.T) {
	f := startFakeServer(t, nil)
	m := pipeManager(f)
	defer m.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	writeFile(t, path, "package main\n")

	f.respond("textDocument/hover", `{"contents":{"kind":"markdown","value":"func main()"}}`)
	if text, err := m.Hover(context.Background(), path, 1, 1); err != nil || text != "func main()" {
		t.Fatalf("hover = %q %v", text, err)
	}
	f.respond("textDocument/hover", `{"contents":[{"language":"go","value":"var x int"},"doc"]}`)
	if text, _ := m.Hover(context.Background(), path, 1, 1); text != "```go\nvar x int\n```\n\ndoc" {
		t.Fatalf("marked hover = %q", text)
	}

	f.respond("textDocument/documentSymbol", `[{"name":"T","kind":23,"range":{"start":{"line":2,"character":0},"end":{"line":5,"character":1}},"selectionRange":{"start":{"line":2,"character":5},"end":{"line":2,"character":6}},"children":[{"name":"F","detail":"int","kind":8,"range":{"start":{"line":3,"character":1},"end":{"line":3,"character":6}},"selectionRange":{"start":{"line":3,"character":1},"end":{"line":3,"character":2}}}]}]`)
	syms, err := m.DocumentSymbols(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) != 2 || syms[0].Name != "T" || syms[0].Kind != "Struct" || syms[0].Line != 3 || syms[1].Depth != 1 || syms[1].Container != "T" || syms[1].Kind != "Field" || syms[1].Detail != "int" || syms[1].Path != path {
		t.Fatalf("symbols = %+v", syms)
	}

	f.respond("workspace/symbol", `[{"name":"Handler","kind":12,"containerName":"pkg","location":{"uri":"`+fileURI(path)+`","range":{"start":{"line":9,"character":5},"end":{"line":9,"character":12}}}}]`)
	// Without a path, running servers whose root relates to dir answer.
	if _, err := m.WorkspaceSymbols(context.Background(), "", "/elsewhere", "Hand"); err == nil {
		t.Fatal("unrelated directory should find no server")
	}
	ws, err := m.WorkspaceSymbols(context.Background(), "", "/froot/sub", "Hand")
	if err != nil || len(ws) != 1 || ws[0].Name != "Handler" || ws[0].Kind != "Function" || ws[0].Container != "pkg" || ws[0].Line != 10 {
		t.Fatalf("workspace symbols = %+v %v", ws, err)
	}
	var q struct{ Query string }
	_ = json.Unmarshal(f.lastParams("workspace/symbol"), &q)
	if q.Query != "Hand" {
		t.Fatalf("query = %q", q.Query)
	}
}

func TestQueryErrors(t *testing.T) {
	f := startFakeServer(t, nil)
	m := pipeManager(f)
	defer m.Close()
	dir := t.TempDir()
	txt := filepath.Join(dir, "notes.txt")
	writeFile(t, txt, "hi")
	if _, err := m.Hover(context.Background(), txt, 1, 1); !errors.Is(err, ErrNoServer) {
		t.Fatalf("unhandled extension: %v", err)
	}
	goFile := filepath.Join(dir, "a.go")
	writeFile(t, goFile, "package a\n")
	if _, err := m.Definition(context.Background(), goFile, 0, 1); err == nil {
		t.Fatal("line 0 must be rejected")
	}
	if _, err := m.Definition(context.Background(), filepath.Join(dir, "missing.go"), 1, 1); err == nil {
		t.Fatal("missing file must error")
	}
}

func TestParseLocationsShapes(t *testing.T) {
	uri := fileURI("/x/a.go")
	single := `{"uri":"` + uri + `","range":{"start":{"line":0,"character":2},"end":{"line":0,"character":3}}}`
	if got := parseLocations(json.RawMessage(single)); len(got) != 1 || got[0].Col != 3 {
		t.Fatalf("single = %+v", got)
	}
	if got := parseLocations(json.RawMessage(`null`)); got != nil {
		t.Fatalf("null = %+v", got)
	}
	if got := parseLocations(json.RawMessage(`[]`)); len(got) != 0 {
		t.Fatalf("empty = %+v", got)
	}
}
