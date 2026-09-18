package computer

import (
	"errors"
	"runtime"
	"strings"
	"testing"
)

func TestQuote(t *testing.T) {

	if got := quote(`a"b`); got != `"a\"b"` {
		t.Errorf("quote: %q", got)
	}
	if got := quote(`back\slash`); got != `"back\\slash"` {
		t.Errorf("quote backslash: %q", got)
	}
	if got := quote("plain"); got != `"plain"` {
		t.Errorf("quote plain: %q", got)
	}
}

func TestPolicyCheck(t *testing.T) {
	p := NewPolicy([]string{"Google Chrome"}, []string{"Finder"}, true)
	if err := p.Check("Google Chrome"); err != nil {
		t.Errorf("allowed app blocked: %v", err)
	}
	if err := p.Check("google chrome"); err != nil {
		t.Errorf("case-normalized allow blocked: %v", err)
	}
	if err := p.Check("Finder"); err == nil || !strings.Contains(err.Error(), "policy") {
		t.Errorf("denied app must fail with policy error, got %v", err)
	}
	err := p.Check("Safari")
	if err == nil {
		t.Fatal("unlisted app under default-deny must need approval")
	}
	approvalNeeded := &ApprovalNeeded{}
	if !errors.As(err, &approvalNeeded) {
		t.Fatalf("want ApprovalNeeded, got %T", err)
	}
	p.Approve("Safari")
	if err := p.Check("Safari"); err != nil {
		t.Errorf("session approval must unblock: %v", err)
	}
}

func TestPolicyDefaultAllow(t *testing.T) {
	p := NewPolicy(nil, []string{"Finder"}, false)
	if err := p.Check("Safari"); err != nil {
		t.Errorf("default-allow must pass unlisted apps: %v", err)
	}
	if err := p.Check("Finder"); err == nil {
		t.Error("deny list wins even under default-allow")
	}
}

func TestChromeTabsParse(t *testing.T) {

	p := &Policy{}
	_ = p

	line := "1￨2￨https://example.com￨a title with | pipe"
	f := strings.SplitN(line, "￨", 4)
	if len(f) != 4 || f[3] != "a title with | pipe" {
		t.Fatalf("separator parse: %v", f)
	}
}

func TestUnsupportedPlatform(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin: osascript tier would drive the real desktop")
	}
	if _, err := osascript(`return "x"`); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Errorf("osascript: %v", err)
	}
	if _, err := Tell("Finder", "activate"); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Errorf("Tell: %v", err)
	}
	calls := map[string]error{}
	_, _, calls["ChromeActive"] = ChromeActive()
	_, calls["ChromeTabs"] = ChromeTabs()
	calls["ChromeGoto"] = ChromeGoto("https://example.com")
	calls["ChromeNewTab"] = ChromeNewTab("https://example.com")
	calls["ChromeActivateTab"] = ChromeActivateTab(1, 2)
	calls["ChromeCloseTab"] = ChromeCloseTab(1, 2)
	calls["ChromeBack"] = ChromeBack()
	calls["ChromeReload"] = ChromeReload()
	_, calls["ChromeFindTab"] = ChromeFindTab("example")
	_, calls["ChromeState"] = ChromeState()
	for name, err := range calls {
		if !errors.Is(err, ErrUnsupportedPlatform) {
			t.Errorf("%s: want ErrUnsupportedPlatform, got %v", name, err)
		}
	}

	if _, err := ChromeJS("1+1"); !errors.Is(err, ErrUnsupportedPlatform) || errors.Is(err, ErrJSFromAppleEvents) {
		t.Errorf("ChromeJS: %v", err)
	}
}

func TestPolicyDenyAndSummary(t *testing.T) {
	p := NewPolicy([]string{"Google Chrome"}, nil, false)
	if got := p.Summary(); got != "google chrome" {
		t.Errorf("Summary: %q", got)
	}
	p.Approve("Safari")
	if got := p.Summary(); !strings.Contains(got, "google chrome") || !strings.Contains(got, "safari (session)") {
		t.Errorf("Summary with session approval: %q", got)
	}

	p.Deny("Safari")
	if err := p.Check("Safari"); err == nil || !strings.Contains(err.Error(), "policy") {
		t.Errorf("session-denied app must fail: %v", err)
	}

	p.Approve("Safari")
	if err := p.Check("Safari"); err != nil {
		t.Errorf("re-approval must unblock: %v", err)
	}
	if got := NewPolicy(nil, nil, true).Summary(); got != "none" {
		t.Errorf("empty Summary: %q", got)
	}
}

func TestApprovalNeededError(t *testing.T) {
	e := &ApprovalNeeded{App: "Safari"}
	if !strings.Contains(e.Error(), `"Safari"`) || !strings.Contains(e.Error(), "computer.allow") {
		t.Errorf("ApprovalNeeded.Error: %q", e.Error())
	}
}
