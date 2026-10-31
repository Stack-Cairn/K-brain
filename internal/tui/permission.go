package tui

import (
	"context"
	"fmt"
	"github.com/Stack-Cairn/K-brain/internal/approval"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/permissions"
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

// permRules holds the remembered rules shared with the backend: user-scope rules,
// and project-scope rules loaded per project root as the TUI moves between folders.
// Rules are matched by the permissions engine, which requires every segment of a
// compound command to be covered.
type permRules struct {
	user     permissions.RuleSet
	projects map[string]permissions.RuleSet
	// saveErr is the last failure to write a remembered rule; the rule still
	// applies for this session and the next dialog says it was not saved.
	saveErr string
}

func loadPermRules() permRules {
	set, _ := permissions.LoadUserRules()
	return permRules{user: set, projects: map[string]permissions.RuleSet{}}
}

func permOptions(readOnly bool) permissions.Options {
	root, _ := currentRoot()
	return permissions.Options{ProjectRoot: root, AllowReadOnly: readOnly}
}

func permRequestOf(req tools.GateRequest) permissions.Request {
	return permissions.Request{Tool: req.Tool, Command: req.Command, Shell: req.Shell}
}

// project returns the project-scope rules for root, reading them on first use.
func (r *permRules) project(root string) permissions.RuleSet {
	if r.projects == nil {
		r.projects = map[string]permissions.RuleSet{}
	}
	if set, ok := r.projects[root]; ok {
		return set
	}
	set := permissions.RuleSet{Scope: permissions.ScopeProject}
	if path, err := permissions.ProjectRulesPath(root); err == nil {
		if loaded, err := permissions.LoadRuleSet(path, permissions.ScopeProject); err == nil {
			set = loaded
		}
	}
	r.projects[root] = set
	return set
}

func (r *permRules) decide(req tools.GateRequest, readOnly bool) permissions.Decision {
	opts := permOptions(readOnly)
	return permissions.Evaluate(permRequestOf(req), opts, r.user, r.project(opts.ProjectRoot))
}

func (r *permRules) suggestions(req tools.GateRequest) []string {
	return permissions.Suggest(permRequestOf(req), permOptions(false))
}

// remember stores the suggested rules for req and reports whether there were any.
// Relative path rules go to the current project, the rest to the user scope.
func (r *permRules) remember(req tools.GateRequest) bool {
	rules := r.suggestions(req)
	if len(rules) == 0 {
		return false
	}
	root := permOptions(false).ProjectRoot
	r.saveErr = ""
	for _, rule := range rules {
		stored := permissions.StoredRule{Rule: rule, Effect: permissions.Allow}
		project := permissions.DefaultScope(rule) == permissions.ScopeProject
		var path, projectRoot string
		var err error
		if project {
			projectRoot = root
			path, err = permissions.ProjectRulesPath(root)
		} else {
			path, err = permissions.UserRulesPath()
		}
		if err == nil {
			var saved permissions.StoredRule
			if saved, err = permissions.AddRule(path, projectRoot, stored); err == nil {
				stored = saved
			}
		}
		if err != nil {
			r.saveErr = err.Error()
		}
		if project {
			set := r.project(root)
			set.Rules = append(set.Rules, stored)
			r.projects[root] = set
		} else {
			r.user.Scope = permissions.ScopeUser
			r.user.Rules = append(r.user.Rules, stored)
		}
	}
	return true
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
	report, reviewed := approval.Report{}, false
	if msg.req.Context != nil {
		report, reviewed = msg.req.Context.Value(approval.ActionReportKey{}).(approval.Report)
		if reviewed {
			msg.req.Description = report.Description()
		}
	}
	auto := m.permissionMode == "auto_review" || (m.cfg != nil && m.cfg.AIApproval.Mode == "auto")
	alreadyApproved := false
	if msg.req.Context != nil {
		name, _ := msg.req.Context.Value(approval.ReviewedActionKey{}).(string)
		alreadyApproved = name == msg.req.Tool
	}
	// A review report means the reviewer wants a say, so read-only commands are not
	// waved through on their own; remembered rules still apply as before.
	decision := m.perms.decide(msg.req, !reviewed)
	switch {
	case msg.req.Context != nil && msg.req.Context.Err() != nil:
		msg.reply <- permAnswer{tools.GateReject, "cancelled"}
	case m.permissionMode == "plan":
		msg.reply <- permAnswer{tools.GateReject, "Plan mode only allows read-only inspection"}
	case decision.Effect == permissions.Deny:
		msg.reply <- permAnswer{tools.GateReject, "denied by permission rule " + decision.Rule}
	case alreadyApproved:
		// The review gate already decided this call, asking the user when needed.
		msg.reply <- permAnswer{decision: tools.GateAllowOnce}
	case decision.Effect == permissions.Ask:
		// The command cannot be checked against the deny rules, so even an
		// allowing mode asks.
		m.permDialog = &permDialog{req: msg.req, reply: msg.reply}
	case m.permissionMode == "always":
		msg.reply <- permAnswer{decision: tools.GateAllowOnce}
	case auto && reviewed && report.Safe():
		msg.reply <- permAnswer{decision: tools.GateAllowOnce}
	case auto && reviewed && !report.Safe():
		m.permDialog = &permDialog{req: msg.req, reply: msg.reply}
	case m.permissionMode == "auto_review":
		m.permDialog = &permDialog{req: msg.req, reply: msg.reply}
	case m.permissionMode == "full_access" || (m.permissionMode == "auto_edit" && permissions.IsEdit(msg.req.Tool)) || decision.Effect == permissions.Allow:
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
			answer(m.allowAlways(d.req))
		case 2:
			d.rejecting = true
		}
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "a":
			answer(permAnswer{decision: tools.GateAllowOnce})
		case "A":
			answer(m.allowAlways(d.req))
		case "r":
			d.rejecting = true
		}
	case tea.KeyEsc:
		answer(permAnswer{tools.GateReject, "rejected without a reason"})
	}
	return true
}

// allowAlways remembers the request's suggested rules. Actions that cannot be
// remembered, such as deletions, are allowed once.
func (m *model) allowAlways(req tools.GateRequest) permAnswer {
	if m.perms.remember(req) {
		return permAnswer{decision: tools.GateAllowAlways}
	}
	return permAnswer{decision: tools.GateAllowOnce}
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
	if d.req.Description != "" {
		b.WriteString("\n" + wrap(d.req.Description, max(20, m.width-4)))
	}
	if m.perms.saveErr != "" {
		b.WriteString(youStyle.Render("\n  ⚠ a remembered rule was not saved: " + m.perms.saveErr))
	}
	if rules := m.perms.suggestions(d.req); len(rules) > 0 {
		b.WriteString(dimStyle.Render("\n  always allows: " + strings.Join(rules, ", ")))
	} else {
		b.WriteString(dimStyle.Render("\n  always allow is not available for this action; it allows once"))
	}
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
