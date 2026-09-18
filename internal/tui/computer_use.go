package tui

import (
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/computer"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func (m *model) computerUseCommand(args []string, text string) {
	if !computer.Available() {
		m.append(errStyle.Render("computer-use driver is unavailable — build or install k-brain-computer beside k-brain"))
		return
	}
	if len(args) > 0 && args[0] == "allow" && len(args) > 1 {
		app := strings.Join(args[1:], " ")
		if tools.ComputerPolicy == nil {
			m.append(errStyle.Render("no computer policy installed"))
			return
		}
		tools.ComputerPolicy.Approve(app)
		m.append(dimStyle.Render("◎ computer-use: " + app + " approved for this session"))
		return
	}
	if len(args) > 0 && args[0] == "deny" && len(args) > 1 {

		if tools.ComputerPolicy == nil {
			m.append(errStyle.Render("no computer policy installed"))
			return
		}
		tools.ComputerPolicy.Deny(strings.Join(args[1:], " "))
		m.append(dimStyle.Render("◎ computer-use: " + strings.Join(args[1:], " ") + " denied for this session"))
		return
	}
	if len(args) > 0 {

		m.submitTurn(computerUseInstruction(strings.Join(args, " ")), true)
		return
	}

	apps := "none"
	if tools.ComputerPolicy != nil {
		apps = tools.ComputerPolicy.Summary()
	}
	m.append(dimStyle.Render("◎ computer-use: Go driver · approved apps: " + apps + " · /computer-use <task> to drive the desktop, allow/deny <app> to manage consent"))
}

func computerUseInstruction(task string) string {
	return "The user asked for this to be done with computer-use. Use the computer_exec tool (drive supported desktop apps; AppleScript helpers are macOS-only) to accomplish it; do not fall back to browser_exec or shell unless computer_exec can't express the step.\n\nTask: " + task
}
