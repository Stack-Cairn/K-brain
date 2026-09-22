package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

type permRequest struct {
	req   tools.GateRequest
	reply chan permAnswer
}

type permAnswer struct {
	decision tools.GateDecision
	redirect string
}

type permDialog struct {
	req       tools.GateRequest
	reply     chan permAnswer
	sel       int
	rejecting bool
	rejectIn  string
}

type permRules map[string]bool

func loadPermRules() permRules {
	out := permRules{}
	var list []string
	if err := config.ReadJSON("permissions.json", &list); err == nil {
		for _, r := range list {
			out[r] = true
		}
	}
	return out
}

func (r permRules) save() {
	list := make([]string, 0, len(r))
	for k := range r {
		list = append(list, k)
	}
	_ = config.WriteJSON("permissions.json", list)
}

func ruleKey(tool, rule string) string { return tool + ":" + rule }

func (r permRules) coveredBy(req tools.GateRequest) bool {
	rule := req.Rule
	if req.Tool != "bash" {
		rule = req.Command
	}
	return r[ruleKey(req.Tool, rule)]
}

func (m *model) installPermGate() {
	m.perms = loadPermRules()
	prog := func() *tea.Program { return m.prog }
	var promptMu sync.Mutex
	tools.Gate = func(req tools.GateRequest) (tools.GateDecision, string) {
		promptMu.Lock()
		defer promptMu.Unlock()
		ctx := req.Context
		if ctx == nil {
			ctx = context.Background()
		}
		if ctx.Err() != nil {
			return tools.GateReject, "cancelled"
		}
		p := prog()
		if p == nil {
			return tools.GateReject, "permission interface unavailable"
		}
		reply := make(chan permAnswer, 1)
		p.Send(permRequest{req: req, reply: reply})
		select {
		case ans := <-reply:
			return ans.decision, ans.redirect
		case <-ctx.Done():
			p.Send(permClose{reply: reply})
			return tools.GateReject, "cancelled"
		case <-m.permissionDone:
			return tools.GateReject, "session closed"
		}
	}
}

type permClose struct{ reply chan permAnswer }

func (m *model) receivePermission(msg permRequest) {
	switch {
	case msg.req.Context != nil && msg.req.Context.Err() != nil:
		msg.reply <- permAnswer{tools.GateReject, "cancelled"}
	case m.permissionMode == "plan":
		msg.reply <- permAnswer{tools.GateReject, "Plan mode only allows read-only inspection"}
	case m.permissionMode == "always" || m.perms.coveredBy(msg.req):
		msg.reply <- permAnswer{decision: tools.GateAllowOnce}
	default:
		m.permDialog = &permDialog{req: msg.req, reply: msg.reply}
	}
}

func (m *model) permKey(msg tea.KeyMsg) bool {
	d := m.permDialog
	if d == nil {
		return false
	}
	answer := func(a permAnswer) {
		d.reply <- a
		m.permDialog = nil
	}
	if d.rejecting {
		switch msg.Type {
		case tea.KeyEnter:
			answer(permAnswer{tools.GateReject, strings.TrimSpace(d.rejectIn)})
		case tea.KeyEsc:
			d.rejecting, d.rejectIn = false, ""
		case tea.KeyBackspace:
			if len(d.rejectIn) > 0 {
				d.rejectIn = d.rejectIn[:len(d.rejectIn)-1]
			}
		case tea.KeyRunes, tea.KeySpace:
			d.rejectIn += string(msg.Runes)
		}
		return true
	}
	switch msg.Type {
	case tea.KeyLeft, tea.KeyUp:
		d.sel = (d.sel + 2) % 3
	case tea.KeyRight, tea.KeyDown:
		d.sel = (d.sel + 1) % 3
	case tea.KeyEnter:
		switch d.sel {
		case 0:
			answer(permAnswer{decision: tools.GateAllowOnce})
		case 1:
			rule := d.req.Rule
			if d.req.Tool != "bash" {
				rule = d.req.Command
			}
			m.perms[ruleKey(d.req.Tool, rule)] = true
			m.perms.save()
			answer(permAnswer{decision: tools.GateAllowAlways})
		case 2:
			d.rejecting = true
		}
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "a":
			answer(permAnswer{decision: tools.GateAllowOnce})
		case "A":
			rule := d.req.Rule
			if d.req.Tool != "bash" {
				rule = d.req.Command
			}
			m.perms[ruleKey(d.req.Tool, rule)] = true
			m.perms.save()
			answer(permAnswer{decision: tools.GateAllowAlways})
		case "r":
			d.rejecting = true
		}
	case tea.KeyEsc:
		answer(permAnswer{tools.GateReject, "rejected without a reason"})
	}
	return true
}

func (m *model) permView() string {
	d := m.permDialog
	if d == nil {
		return ""
	}
	var b strings.Builder
	title := "Allow " + d.req.Tool + "?"
	if d.req.Tool == "bash" {
		title = "Run this command?"
	}
	b.WriteString(youStyle.Render("⚠ " + title))
	b.WriteString("\n  " + ansi_Truncate(d.req.Command, m.width-4))
	rule := d.req.Rule
	if d.req.Tool != "bash" {
		rule = d.req.Command
	}
	b.WriteString(dimStyle.Render("\n  always allows: " + ruleKey(d.req.Tool, rule)))
	if d.rejecting {
		b.WriteString("\n" + youStyle.Render("  reject with message: ") + d.rejectIn + "█")
		b.WriteString(dimStyle.Render("\n  Enter sends · Esc Back"))
		return b.String()
	}
	opts := []string{"allow once (a)", "allow always (A)", "reject (r)"}
	b.WriteString("\n  ")
	for i, o := range opts {
		if i == d.sel {
			b.WriteString(youStyle.Render(glyphUser + o + "  "))
		} else {
			b.WriteString(dimStyle.Render("  " + o + "  "))
		}
	}
	return b.String()
}

func ansi_Truncate(s string, width int) string {
	if width <= 0 {
		width = 80
	}
	if len(s) <= width {
		return s
	}
	return s[:width-1] + "…"
}

var _ = fmt.Sprint
