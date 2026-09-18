package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestLightThemeRendersDarkText(t *testing.T) {
	SetLightTheme(true)
	defer SetLightTheme(false)
	out := renderMarkdown("plain body text", 60)
	if !strings.Contains(out, "\x1b[38;5;234m") {
		t.Errorf("light theme should render body in color 234, got %q", out)
	}
	if strings.Contains(out, "\x1b[38;5;252m") {
		t.Errorf("light theme must not use dark-style color 252: %q", out)
	}

	for l := range strings.SplitSeq(out, "\n") {
		if ansi.StringWidth(l) > 60 {
			t.Errorf("light render overflow: %q", l)
		}
	}
}

func TestThemeOverride(t *testing.T) {
	t.Setenv("K_BRAIN_THEME", "light")
	detectColorScheme()
	mdMu.Lock()
	light := mdLight
	mdMu.Unlock()
	if !light {
		t.Fatal("K_BRAIN_THEME=light should select the light style")
	}
	t.Setenv("K_BRAIN_THEME", "dark")
	detectColorScheme()
	mdMu.Lock()
	light = mdLight
	mdMu.Unlock()
	if light {
		t.Fatal("K_BRAIN_THEME=dark should select the dark style")
	}
}

func TestColorFGBGDetection(t *testing.T) {
	t.Setenv("K_BRAIN_THEME", "")
	t.Setenv("COLORFGBG", "0;15")
	detectColorScheme()
	mdMu.Lock()
	light := mdLight
	mdMu.Unlock()
	if !light {
		t.Fatal("COLORFGBG=0;15 should select the light style")
	}
	t.Setenv("COLORFGBG", "15;0")
	detectColorScheme()
	mdMu.Lock()
	light = mdLight
	mdMu.Unlock()
	if light {
		t.Fatal("COLORFGBG=15;0 should select the dark style")
	}
}

func TestParseOSCBg(t *testing.T) {
	cases := []struct {
		payload string
		light   bool
	}{
		{"rgb:fafa/fafa/fafa", true},
		{"rgb:ffff/ffff/ffff", true},
		{"rgb:1212/3434/5656", false},
		{"rgb:0000/0000/0000", false},
		{"#ffffff", true},
		{"#000000", false},
		{"#f5f5f5", true},
		{"#1e1e2e", false},
		{"garbage", false},
		{"rgb:fafa/fafa", false},
	}
	for _, c := range cases {
		if got := parseOSCBg(c.payload); got != c.light {
			t.Errorf("parseOSCBg(%q) = %v, want %v", c.payload, got, c.light)
		}
	}
}

func TestUnknownThemeIsNeutral(t *testing.T) {
	SetUnknownTheme()
	defer SetLightTheme(false)
	out := renderMarkdown("plain body text", 60)

	if strings.Contains(out, "\x1b[38;5;252m") || strings.Contains(out, "\x1b[38;5;234m") {
		t.Errorf("unknown theme should not force a body color: %q", out)
	}
	if got := CurrentTheme(); got != "auto" {
		t.Errorf("CurrentTheme = %q, want auto", got)
	}
}

func TestUnknownThemeStillRendersMarkdown(t *testing.T) {
	SetUnknownTheme()
	defer SetLightTheme(false)
	src := "## Head\n\n**bold** text\n\n| A | B |\n|---|---|\n| 1 | 2 |"
	out := renderMarkdown(src, 60)
	if strings.Contains(ansi.Strip(out), "**") {
		t.Errorf("neutral style left literal ** markers (ASCII style?): %q", out)
	}
	if !strings.Contains(out, "\x1b[1m") {
		t.Errorf("neutral style should render bold: %q", out)
	}
	if !strings.Contains(out, "─") {
		t.Errorf("neutral style should draw the table header rule: %q", out)
	}
	if strings.Contains(out, "\x1b[38;5;") || strings.Contains(out, "\x1b[38;2;") {
		t.Errorf("neutral style must only use basic ANSI colors: %q", out)
	}
}

func TestRenderedLinesSelfTerminate(t *testing.T) {
	SetUnknownTheme()
	defer SetLightTheme(false)
	out := renderMarkdown("## Head\n\n| A | B |\n|---|---|\n| 1 | 2 |", 60)
	for l := range strings.SplitSeq(out, "\n") {
		if strings.Contains(l, "\x1b[") && !strings.HasSuffix(l, "\x1b[0m") {
			t.Errorf("styled line not reset-terminated: %q", l)
		}
	}
}

func TestThemeSwitchAfterUnknown(t *testing.T) {
	SetUnknownTheme()
	_ = renderMarkdown("plain body text", 60)
	SetLightTheme(true)
	defer SetLightTheme(false)
	out := renderMarkdown("plain body text", 60)
	if !strings.Contains(out, "\x1b[38;5;234m") {
		t.Errorf("switching unknown→light should re-render in light (234): %q", out)
	}
}
