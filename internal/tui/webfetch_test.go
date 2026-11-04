package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestWebFetchRowNamesTheURLAndFoldsToChars(t *testing.T) {
	cases := []struct{ args, result, head, fold string }{
		{`{"url":"https://example.com/docs/guide/?q=1#top"}`, "url=https://example.com/docs/guide/?q=1\nstatus=200\ncontent_type=text/html\ntotal_chars=12\noffset=0\ntruncated=false\n\nHello\nworld!", "Fetch(example.com/docs/guide)", "⎿ 12 chars"},
		{`{"url":"https://example.com/long","max_chars":5}`, "url=https://example.com/long\nstatus=200\ncontent_type=text/plain\ntotal_chars=40\noffset=0\ntruncated=true\nnext_offset=5\n\nabcde", "Fetch(example.com/long)", "⎿ 5 of 40 chars"},
		{`{"url":"https://example.com/` + strings.Repeat("p", 80) + `"}`, "url=x\nstatus=200\ntotal_chars=1\n\nx", "Fetch(example.com/ppp", "⎿ 1 char"},
	}
	for _, c := range cases {
		m := compactCmdModel()
		m.Update(mkWinSize(120, 30))
		m.Update(toolStartMsg{id: "1", name: "web_fetch", args: c.args})
		m.Update(toolEndMsg{id: "1", name: "web_fetch", result: c.result})
		head := ansi.Strip(m.blocks[len(m.blocks)-2].render(m.width))
		fold := ansi.Strip(m.blocks[len(m.blocks)-1].render(m.width))
		if !strings.Contains(head, c.head) || strings.TrimSpace(fold) != c.fold {
			t.Errorf("header %q fold %q, want %q / %q", head, fold, c.head, c.fold)
		}
		if strings.Contains(fold, "Hello") {
			t.Error("the fetched text should stay folded")
		}
	}
	if s := toolSubject("web_fetch", `{"url":"https://example.com/`+strings.Repeat("p", 80)+`"}`); len([]rune(s)) > 60 {
		t.Errorf("long URLs should be truncated, got %q", s)
	}
	if toolVerb("web_fetch") != "Fetching" {
		t.Errorf("verb = %q", toolVerb("web_fetch"))
	}
}

// A failed fetch is not folded: its status and excerpt are the point.
func TestWebFetchErrorsStayVisible(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(120, 30))
	m.Update(toolStartMsg{id: "1", name: "web_fetch", args: `{"url":"https://example.com/missing"}`})
	m.Update(toolEndMsg{id: "1", name: "web_fetch", result: "Error: HTTP 404 Not Found from https://example.com/missing\nurl=https://example.com/missing\nstatus=404\nNot here"})
	if fold := ansi.Strip(m.blocks[len(m.blocks)-1].render(m.width)); !strings.Contains(fold, "HTTP 404") {
		t.Errorf("error result = %q", fold)
	}
}

// Plan mode refuses gated tools but asks before a fetch, and offers to remember its host.
func TestPlanModeAsksBeforeWebFetch(t *testing.T) {
	m := &model{perms: permRules{}, permissionMode: "plan"}
	reply := make(chan permAnswer, 1)
	m.receivePermission(permRequest{req: tools.GateRequest{Context: t.Context(), Tool: "bash", Command: "curl https://example.com"}, reply: reply})
	if ans := <-reply; ans.decision != tools.GateReject {
		t.Fatalf("plan mode should refuse bash, got %+v", ans)
	}
	m.receivePermission(permRequest{req: tools.GateRequest{Context: t.Context(), Tool: "web_fetch", Command: "https://example.com/docs", Rule: "domain:example.com"}, reply: reply})
	if m.permDialog == nil {
		t.Fatal("plan mode should ask before a fetch")
	}
	view := ansi.Strip(m.permView())
	if !strings.Contains(view, "Fetch this URL?") || !strings.Contains(view, "https://example.com/docs") || !strings.Contains(view, "always allows: WebFetch(domain:example.com)") {
		t.Errorf("dialog:\n%s", view)
	}
}
