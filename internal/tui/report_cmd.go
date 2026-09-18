package tui

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

var Version = "dev"

const issueBase = "https://github.com/Stack-Cairn/K-brain/issues/new"

type envRow struct {
	key, val string
}

type envReport struct {
	rows    []envRow
	link    string
	snippet string
}

func (m *model) envReport() envReport {
	var r envReport
	add := func(k, v string) {
		if v != "" {
			r.rows = append(r.rows, envRow{k, v})
		}
	}

	add("k-brain", Version)
	add("model", m.modelName)
	add("provider", m.provName)
	theme := CurrentTheme()
	if m.themeHow != "" {
		theme += " (" + m.themeHow + ")"
	}
	add("theme", theme)
	add("mouse", onOff(m.mouseOn))
	add("session", m.sessionID)

	add("TERM", os.Getenv("TERM"))
	if tp := os.Getenv("TERM_PROGRAM"); tp != "" {
		if v := os.Getenv("TERM_PROGRAM_VERSION"); v != "" {
			tp += " " + v
		}
		add("TERM_PROGRAM", tp)
	}
	add("COLORTERM", os.Getenv("COLORTERM"))
	add("COLORFGBG", os.Getenv("COLORFGBG"))
	if tm := os.Getenv("TMUX"); tm != "" {
		v := tm
		if out, err := exec.CommandContext(context.Background(), "tmux", "-V").Output(); err == nil {
			v = strings.TrimSpace(string(out))
		}
		add("tmux", v)
	}
	add("SHELL", os.Getenv("SHELL"))
	if lc := os.Getenv("LC_ALL"); lc != "" {
		add("locale", lc)
	} else {
		add("locale", os.Getenv("LANG"))
	}
	if m.width > 0 {
		add("size", fmt.Sprintf("%dx%d", m.width, m.height))
	}
	if os.Getenv("SSH_TTY") != "" || os.Getenv("SSH_CONNECTION") != "" {
		add("ssh", "yes")
	}

	add("os", runtime.GOOS+"/"+runtime.GOARCH)
	if out, err := exec.CommandContext(context.Background(), "uname", "-srm").Output(); err == nil {
		add("uname", strings.TrimSpace(string(out)))
	}
	if runtime.GOOS == "darwin" {
		if out, err := exec.CommandContext(context.Background(), "sw_vers", "-productVersion").Output(); err == nil {
			add("macOS", strings.TrimSpace(string(out)))
		}
	}
	add("go", runtime.Version())

	r.snippet = r.snippetText()
	r.link = issueURL(r.snippet)
	return r
}

func (r envReport) snippetText() string {
	w := 0
	for _, row := range r.rows {
		if len(row.key) > w {
			w = len(row.key)
		}
	}
	var b strings.Builder
	b.WriteString("```\n")
	for _, row := range r.rows {
		fmt.Fprintf(&b, "%-*s %s\n", w, row.key, row.val)
	}
	b.WriteString("```")
	return b.String()
}

func issueURL(snippet string) string {
	body := "### What happened\n\n\n\n### Expected\n\n\n\n### Environment\n\n" + snippet + "\n"
	v := url.Values{}
	v.Set("title", "")
	v.Set("body", body)
	return issueBase + "?" + v.Encode()
}

func (m *model) reportBlock() string {
	r := m.envReport()
	return "Bug report — " + hyperlink(r.link, "open a prefilled GitHub issue") +
		" (or paste the snippet below into an existing issue):\n\n" + r.snippet
}
