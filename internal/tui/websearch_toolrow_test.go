package tui

import "testing"

func TestWebSearchToolRow(t *testing.T) {
	if got := toolHeaderName("web_search"); got != "Web search" {
		t.Fatalf("header = %q", got)
	}
	if got := toolSubject("web_search", `{"query":"  go   1.30 release "}`); got != `"go 1.30 release"` {
		t.Fatalf("subject = %q", got)
	}
	if !quietResultTool("web_search") || !groupableTool("web_search") || toolKindOf("web_search") != tkFetch {
		t.Fatal("web_search is not shown like web_fetch")
	}
	for text, want := range map[string]string{
		"provider=tavily\nquery=x\nresults=3\n\n1. a":        "3 results",
		"provider=tavily\nquery=x\nresults=1\n\n1. a":        "1 result",
		"provider=tavily\nquery=x\nresults=0\n\nNo results.": "0 results",
	} {
		if got := quietResultSummary("web_search", text, 5); got != want {
			t.Fatalf("summary = %q, want %q", got, want)
		}
	}
	if key, v := groupVerbOf("web_search"); key != "websearch" || v.verb != "searched the web for" {
		t.Fatalf("group verb = %q %+v", key, v)
	}
}
