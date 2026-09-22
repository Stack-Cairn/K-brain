package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/mcp"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// reportText is everything startupReport appended, i.e. the per-item problems.
// Summary counts live on the startup card instead and are asserted through
// bannerRows.
func reportText(m *model) string {
	var b strings.Builder
	for _, blk := range m.blocks {
		if blk.kind == blockBanner {
			continue
		}
		b.WriteString(ansi.Strip(blk.text) + "\n")
	}
	return b.String()
}

func cardText(m *model) string {
	return ansi.Strip(strings.Join(m.bannerRows(), "\n"))
}

func TestStartupReportSkillsAndWarnings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	dir := t.TempDir()
	mkSkill := func(name, desc string) {
		d := filepath.Join(dir, ".agents", "skills", name)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: "+desc+"\n---\n"), 0o644)
	}
	mkSkill("good", "fine")
	mkSkill("wordy", strings.Repeat("x", 1100))

	bad := filepath.Join(dir, ".agents", "skills", "broken")
	os.MkdirAll(bad, 0o755)
	os.WriteFile(filepath.Join(bad, "SKILL.md"), []byte("no frontmatter here"), 0o644)

	t.Chdir(dir)

	m := tasksModel("http://unused")
	m.startupReport()

	if m.stats.skills != 2 {
		t.Errorf("loaded count = %d, want 2", m.stats.skills)
	}
	if m.stats.skillWarn != 2 {
		t.Errorf("warning count = %d, want 2 (one truncation, one parse problem)", m.stats.skillWarn)
	}
	if card := cardText(m); !strings.Contains(card, "2 skills") || !strings.Contains(card, "(2 ⚠)") {
		t.Errorf("card should summarise skills and flag the warnings:\n%s", card)
	}

	out := reportText(m)
	if !strings.Contains(out, "wordy") || !strings.Contains(out, "exceeds 1024") {
		t.Errorf("missing truncation warning:\n%s", out)
	}
	if !strings.Contains(out, "broken") {
		t.Errorf("missing parse problem:\n%s", out)
	}
}

func TestStartupReportMCP(t *testing.T) {
	m := tasksModel("http://unused")
	disabled := false
	m.mcpMgr = mcp.NewManager(map[string]mcp.ServerConfig{
		"off":     {Command: []string{"true"}, Enabled: &disabled},
		"invalid": {},
	})
	m.startupReport()

	if m.stats.mcpFailed != 1 {
		t.Errorf("failed count = %d, want 1", m.stats.mcpFailed)
	}
	if card := cardText(m); !strings.Contains(card, "MCP ✗") {
		t.Errorf("a failed server should be flagged on the card:\n%s", card)
	}
	if out := reportText(m); !strings.Contains(out, "mcp invalid") {
		t.Errorf("the failing server should be named in the report:\n%s", out)
	}
}

func TestStartupReportMCPReadyAndPending(t *testing.T) {
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "ok"}, nil)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        "ping",
		Description: "pong",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in struct{}) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "pong"}}}, nil, nil
	})
	hs := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return srv }, nil))
	defer hs.Close()

	mgr := mcp.NewManager(map[string]mcp.ServerConfig{
		"ok":      {URL: hs.URL},
		"invalid": {},
	})
	mgr.Start(context.Background())
	defer mgr.Close()
	deadline := time.Now().Add(10 * time.Second)
	for {
		sts := mgr.Statuses()
		if len(sts) == 2 && sts[1].Status == mcp.StatusReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never became ready: %+v", sts)
		}
		time.Sleep(10 * time.Millisecond)
	}

	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))

	m := tasksModel("http://unused")
	m.mcpMgr = mgr
	m.startupReport()

	if m.stats.mcpReady != 1 || m.stats.mcpFailed != 1 || m.stats.mcpTools != 1 {
		t.Errorf("stats = %+v, want 1 ready / 1 failed / 1 tool", m.stats)
	}
	if card := cardText(m); !strings.Contains(card, "1/2 MCP ✗") {
		t.Errorf("a partly broken fleet should read as a failure on the card:\n%s", card)
	}

	// A healthy fleet reports its tool count instead of a failure.
	mgr2 := mcp.NewManager(map[string]mcp.ServerConfig{"ok": {URL: hs.URL}})
	mgr2.Start(context.Background())
	defer mgr2.Close()
	for {
		sts := mgr2.Statuses()
		if len(sts) == 1 && sts[0].Status == mcp.StatusReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("second server never became ready: %+v", sts)
		}
		time.Sleep(10 * time.Millisecond)
	}
	m3 := tasksModel("http://unused")
	m3.mcpMgr = mgr2
	m3.startupReport()
	if card := cardText(m3); !strings.Contains(card, "1 MCP (1 tools)") {
		t.Errorf("a healthy fleet should show its tool count:\n%s", card)
	}
}

func TestStartupReportSilent(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))

	m := tasksModel("http://unused")
	m.startupReport()
	if out := strings.TrimSpace(reportText(m)); out != "" {
		t.Errorf("expected silence, got %q", out)
	}
}

func TestStartupReportUpdateNotice(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))

	m := tasksModel("http://unused")
	m.updateLatest = "v0.4.0"
	m.startupReport()

	card := cardText(m)
	if !strings.Contains(card, "v0.4.0") || !strings.Contains(card, "kn update") {
		t.Errorf("the card should carry the update notice:\n%s", card)
	}
}
