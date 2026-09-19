package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestExportTranscript(t *testing.T) {
	msgs := []ai.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi", ToolCalls: []ai.ToolCall{{Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "bash", Arguments: `{"cmd":"ls"}`}}}},
		{Role: "tool", Name: "bash", Content: "file.go"},
	}
	path := filepath.Join(t.TempDir(), "out.md")
	if err := exportTranscript(path, msgs); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"# Session transcript", "## User", "hello", "## Assistant", "`bash`", "#### Tool result", "file.go"} {
		if !strings.Contains(s, want) {
			t.Fatalf("export missing %q:\n%s", want, s)
		}
	}
}

func TestExportNothingToExport(t *testing.T) {
	m := &model{width: 80, height: 24}
	before := len(m.blocks)
	if _, cmd := m.command("/export"); cmd != nil {
		t.Error("/export should not return a tea.Cmd")
	}
	if len(m.blocks) != before+1 {
		t.Fatalf("blocks grew by %d, want 1", len(m.blocks)-before)
	}
	if !strings.Contains(m.blocks[before].text, "nothing to export") {
		t.Errorf("unexpected empty-transcript notice: %q", m.blocks[before].text)
	}
}

func TestDisplayRole(t *testing.T) {
	for in, want := range map[string]string{
		"user":      "User",
		"assistant": "Assistant",
		"system":    "System",
		"tool":      "Tool",
		"custom":    "Custom",
		"":          "",
	} {
		if got := displayRole(in); got != want {
			t.Errorf("displayRole(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExportFilePerms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.md")
	if err := exportTranscript(path, []ai.Message{{Role: "user", Content: "x"}}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("transcript perms = %o, want 600", perm)
	}
}

func TestExportJSONLRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl")
	want := []ai.Message{{Role: "user", Content: "hello"}, {Role: "assistant", Content: "<answer>"}}
	if err := exportJSONL(path, "abc", "title", "model1", "demo", want); err != nil {
		t.Fatal(err)
	}
	got, err := importJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) || got[0].TextContent() != want[0].TextContent() || got[1].TextContent() != want[1].TextContent() {
		t.Fatalf("round trip: %#v", got)
	}
}

func TestExportHTMLEscaping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.html")
	if err := exportHTML(path, "<title>", []ai.Message{{Role: "user", Content: "<script>alert(1)</script>"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "<script>alert") || !strings.Contains(string(b), "&lt;script&gt;") {
		t.Fatalf("HTML escaping failed: %s", b)
	}
}
