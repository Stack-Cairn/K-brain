package tui

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/browser"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func browserStepLabel(argsJSON string) string {
	var a struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil || a.Code == "" {
		return ""
	}
	for line := range strings.SplitSeq(a.Code, "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "#"); ok {
			return strings.TrimSpace(after)
		}
		if line != "" {
			break
		}
	}
	return ""
}

func (m *model) switchBrowserDriver(d string) {
	if os.Getenv("K_BRAIN_BROWSER_DRIVER") != "" {
		m.append(dimStyle.Render("◎ browser driver pinned by K_BRAIN_BROWSER_DRIVER=" + os.Getenv("K_BRAIN_BROWSER_DRIVER") + " — unset it to switch"))
		return
	}
	if tools.Browser != nil {
		tools.Browser.SwitchDriver(d)
	} else {
		browser.SetDriver(d)
	}
	m.append(dimStyle.Render("◎ browser driver: " + browser.Driver + " (open browser sessions re-open on next use)"))
}
