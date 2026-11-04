package permissions

import "testing"

// web_fetch is network egress: it is never waved through as read-only, and
// remembering a fetch allows its host.
func TestWebFetchRulesMatchByHost(t *testing.T) {
	ro := Options{ProjectRoot: "/p", AllowReadOnly: true}
	req := func(u string) Request { return Request{Tool: "web_fetch", Command: u} }
	if IsReadOnlyTool("web_fetch") {
		t.Error("web_fetch must not count as read-only")
	}
	if d := Evaluate(req("https://example.com/a"), ro); d.Effect != "" {
		t.Errorf("an unruled fetch = %+v, want undecided (ask)", d)
	}
	got := Suggest(req("https://Docs.Example.com:8443/guide?q=1"), ro)
	if len(got) != 1 || got[0] != "WebFetch(domain:docs.example.com)" {
		t.Fatalf("suggestion = %v", got)
	}
	if DefaultScope(got[0]) != ScopeUser {
		t.Errorf("a domain rule should be remembered for the user, got %s", DefaultScope(got[0]))
	}
	rules := allow(got[0])
	for u, want := range map[string]Effect{
		"https://docs.example.com/other":          Allow,
		"http://DOCS.example.com/":                Allow,
		"https://example.com/":                    "",
		"https://evil.docs.example.com/":          "",
		"https://docs.example.com.evil.test/":     "",
		"https://docs.example.com@evil.test/path": "",
		"ftp://docs.example.com/":                 "",
		"not a url":                               "",
	} {
		if d := Evaluate(req(u), ro, rules); d.Effect != want {
			t.Errorf("%s under %s = %+v, want %q", u, got[0], d, want)
		}
	}
	wild := allow("WebFetch(domain:*.example.com)")
	if d := Evaluate(req("https://a.b.example.com/x"), ro, wild); d.Effect != Allow {
		t.Errorf("subdomain under the wildcard = %+v", d)
	}
	if d := Evaluate(req("https://example.com/x"), ro, wild); d.Effect != "" {
		t.Errorf("the bare domain under *.example.com = %+v, want undecided", d)
	}
	if d := Evaluate(req("https://docs.example.com/x"), ro, rules, deny("WebFetch(domain:docs.example.com)")); d.Effect != Deny {
		t.Errorf("deny should win over allow, got %+v", d)
	}
	if d := Evaluate(req("https://any.test/"), ro, allow("WebFetch")); d.Effect != Allow {
		t.Errorf("a whole-tool rule should allow every fetch, got %+v", d)
	}
	for _, bad := range []string{"WebFetch(domain:)", "WebFetch(domain:a.com:*)", "WebFetch(domain:a.com/x)", "WebFetch(domain:*)", "WebFetch(domain:a*.com)"} {
		if _, err := ParseRule(bad); err == nil {
			t.Errorf("%s should not parse", bad)
		}
	}
	if r, err := ParseRule("web_fetch(domain:Example.COM)"); err != nil || r.String() != "WebFetch(domain:example.com)" {
		t.Errorf("lowercase tool rule = %v, %v", r, err)
	}
}
