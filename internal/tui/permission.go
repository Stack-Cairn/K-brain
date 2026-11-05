package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/approval"

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

	// why says what made this action ask: a rule, the reviewer or the mode.
	why string
	// levels are the breadths "always allow" can remember, narrowest first,
	// and scope the one chosen (permissions.SuggestScopes); read on first use.
	levels      []permissions.ScopeLevel
	scope       int
	levelsReady bool
	// expanded shows the whole edit preview instead of its first rows.
	expanded bool
	scroll   cardScroll
}

// permPreviewRows is how many diff rows a folded edit preview shows.
const permPreviewRows = 12

// scopes returns the dialog's remember levels, reading them on first use.
func (d *permDialog) scopes() []permissions.ScopeLevel {
	if !d.levelsReady {
		d.levelsReady = true
		d.levels, d.scope = permissions.SuggestScopes(permRequestOf(d.req), permOptions(false))
		d.scope = max(d.scope, 0)
	}
	return d.levels
}

// chosenRules are the rules "always allow" stores at the chosen scope.
func (d *permDialog) chosenRules() []string {
	levels := d.scopes()
	if len(levels) == 0 {
		return nil
	}
	return levels[min(d.scope, len(levels)-1)].Rules
}

// widen moves the chosen scope by delta levels, wrapping around.
func (d *permDialog) widen(delta int, cycle bool) {
	n := len(d.scopes())
	switch {
	case n < 2:
	case cycle:
		d.scope = (d.scope + delta + n) % n
	default:
		d.scope = min(max(d.scope+delta, 0), n-1)
	}
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
	return permissions.Request{Tool: req.Tool, Command: req.Command, Shell: req.Shell, Poll: req.Poll, OtherDir: req.OtherDir}
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

// remember stores rules (the suggested ones when nil) for req and reports
// whether there were any. Relative path rules go to the current project, the
// rest to the user scope.
func (r *permRules) remember(req tools.GateRequest, rules []string) bool {
	if rules == nil {
		rules = r.suggestions(req)
	}
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
	if decision.Effect == permissions.Allow && decision.Source == permissions.SourceRule && !approval.RulesUsable(msg.req.Context) {
		decision = permissions.Decision{}
	}
	switch {
	case msg.req.Context != nil && msg.req.Context.Err() != nil:
		msg.reply <- permAnswer{tools.GateReject, "cancelled"}
	case m.permissionMode == "plan" && !tools.AsksInPlanMode(msg.req.Tool) && !permissions.IsInteractionTool(msg.req.Tool):
		msg.reply <- permAnswer{tools.GateReject, "Plan mode only allows read-only inspection"}
	case decision.Effect == permissions.Deny:
		msg.reply <- permAnswer{tools.GateReject, "denied by permission rule " + decision.Rule}
	case alreadyApproved:
		// The review gate already decided this call, asking the user when needed.
		msg.reply <- permAnswer{decision: tools.GateAllowOnce}
	case decision.Effect == permissions.Ask:
		// The command cannot be checked against the deny rules, so even an
		// allowing mode asks.
		why := m.tr("an ask rule requires confirmation: ") + decision.Rule
		if decision.Rule == "" {
			why = m.tr("this command cannot be checked against the deny rules")
		}
		m.openPermDialog(msg, why)
	case permissions.IsInteractionTool(msg.req.Tool):
		// Asking the user a question needs no approval in any mode.
		msg.reply <- permAnswer{decision: tools.GateAllowOnce}
	case m.permissionMode == "always":
		msg.reply <- permAnswer{decision: tools.GateAllowOnce}
	case auto && reviewed && report.Safe():
		msg.reply <- permAnswer{decision: tools.GateAllowOnce}
	case auto && reviewed && approval.RuleStandsInForReview(msg.req.Context, decision.Effect == permissions.Allow && decision.Source == permissions.SourceRule):
		// The review only urged caution and the user already remembered this action.
		msg.reply <- permAnswer{decision: tools.GateAllowOnce}
	case auto && reviewed && !report.Safe():
		m.openPermDialog(msg, m.tr("the AI reviewer flagged this action"))
	case m.permissionMode == "auto_review":
		m.openPermDialog(msg, m.tr("Auto review asks for actions it did not review"))
	case m.permissionMode == "full_access" || (m.permissionMode == "auto_edit" && permissions.IsEdit(msg.req.Tool)) || decision.Effect == permissions.Allow:
		msg.reply <- permAnswer{decision: tools.GateAllowOnce}
	default:
		m.openPermDialog(msg, m.modeWhy())
	}
}

// openPermDialog shows the approval card for msg; why says what made it ask.
func (m *model) openPermDialog(msg permRequest, why string) {
	if msg.req.OtherDir {
		why += m.tr(" · runs outside the working directory")
	}
	m.permDialog = &permDialog{req: msg.req, reply: msg.reply, why: why}
}

// modeWhy is why the current mode asks.
func (m *model) modeWhy() string {
	switch m.permissionMode {
	case "plan":
		return m.tr("Plan mode asks before reaching the network")
	case "auto_edit":
		return m.tr("Auto edit allows file edits; other actions ask")
	}
	return m.tr("Normal mode confirms commands and file changes")
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
	if !d.rejecting && cardRunes(msg, func(k tea.KeyMsg) bool { m.permKey(k); return m.permDialog == d }) {
		return true
	}
	if d.rejecting {
		switch msg.Type {
		case tea.KeyEnter:
			answer(permAnswer{tools.GateReject, strings.TrimSpace(d.rejectIn)})
		case tea.KeyEsc:
			d.rejecting, d.rejectIn = false, ""
		case tea.KeyBackspace:
			if r := []rune(d.rejectIn); len(r) > 0 {
				d.rejectIn = string(r[:len(r)-1])
			}
		case tea.KeyRunes, tea.KeySpace:
			d.rejectIn += string(msg.Runes)
		}
		return true
	}
	if cardScrollKey(&d.scroll, msg) {
		return true
	}
	scoped := len(d.scopes()) > 1
	switch msg.Type {
	case tea.KeyUp:
		d.sel = (d.sel + 2) % 3
	case tea.KeyDown:
		d.sel = (d.sel + 1) % 3
	case tea.KeyLeft, tea.KeyRight:
		delta := 1
		if msg.Type == tea.KeyLeft {
			delta = -1
		}
		if d.sel == 1 && scoped {
			d.widen(delta, false)
		} else {
			d.sel = (d.sel + 3 + delta) % 3
		}
	case tea.KeyTab, tea.KeyShiftTab:
		if scoped {
			if d.sel == 1 {
				if msg.Type == tea.KeyTab {
					d.widen(1, true)
				} else {
					d.widen(-1, true)
				}
			}
			d.sel = 1
		}
	case tea.KeyCtrlE:
		d.toggleExpanded()
	case tea.KeyEnter:
		switch d.sel {
		case 0:
			answer(permAnswer{decision: tools.GateAllowOnce})
		case 1:
			answer(m.allowAlways(d))
		case 2:
			d.rejecting = true
		}
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "a", "1":
			answer(permAnswer{decision: tools.GateAllowOnce})
		case "A", "2":
			answer(m.allowAlways(d))
		case "r", "3":
			d.rejecting = true
		case "e":
			d.toggleExpanded()
		}
	case tea.KeyEsc:
		answer(permAnswer{tools.GateReject, "rejected without a reason"})
	}
	return true
}

// toggleExpanded unfolds or folds the edit preview.
func (d *permDialog) toggleExpanded() {
	if d.req.Preview != nil && d.req.Preview.Diff != "" {
		d.expanded = !d.expanded
		d.scroll.off = 0
	}
}

// allowAlways remembers the rules at the dialog's chosen scope. Actions that
// cannot be remembered, such as deletions, are allowed once.
func (m *model) allowAlways(d *permDialog) permAnswer {
	if rules := d.chosenRules(); len(rules) > 0 && m.perms.remember(d.req, rules) {
		return permAnswer{decision: tools.GateAllowAlways}
	}
	return permAnswer{decision: tools.GateAllowOnce}
}

// permTitle is the card's question, by what the action does.
func (m *model) permTitle(req tools.GateRequest) string {
	switch {
	case req.Tool == "bash" || req.Tool == "Bash":
		return m.tr("Run this command?")
	case req.Tool == "web_fetch" || req.Tool == "WebFetch":
		return m.tr("Fetch this URL?")
	case req.Preview != nil && req.Preview.NewFile:
		return m.tr("Create this file?")
	case req.Tool == "edit" || req.Tool == "Edit":
		return m.tr("Apply this edit?")
	case req.Tool == "write" || req.Tool == "Write":
		return m.tr("Write this file?")
	}
	return m.tr("Allow") + " " + req.Tool + "?"
}

// permBody is what the action will do, in full: the highlighted command, or
// the file and the diff of the change.
func (m *model) permBody(d *permDialog, w int) []string {
	req := d.req
	if p := req.Preview; p != nil {
		return m.permPreview(d, p, w)
	}
	var lexer chroma.Lexer
	if req.Tool == "bash" || req.Tool == "Bash" {
		lexer = codeLexer("bash", "")
	}
	return cardCode(req.Command, lexer, w)
}

// permPreview is an edit's path line and its diff, folded unless expanded.
func (m *model) permPreview(d *permDialog, p *tools.EditPreview, w int) []string {
	inner := cardInner(w)
	path := p.Path
	if path == "" {
		path = d.req.Command
	}
	head := cardTitleStyle.Render(displayPath(path))
	switch {
	case p.NewFile:
		head += "  " + accentFgStyle.Render(m.tr("new file"))
	case p.Diff != "":
		head += "  " + diffCountsCell(diffCounts(p.Diff))
	}
	if p.Truncated {
		head += "  " + warnStyle.Render(m.tr("preview truncated"))
	}
	lines := cardLines([]string{head}, w)
	switch {
	case p.Diff == "" && p.Truncated:
		return append(lines, textMutedStyle.Render(wrap(m.tr("the file or the change is too large to preview"), inner)))
	case p.Diff == "":
		return append(lines, textMutedStyle.Render(m.tr("no lines change")))
	}
	rows := permPreviewRows
	if d.expanded {
		rows = 0
	}
	return append(lines, strings.Split(renderDiffFolded(p.Diff, path, inner, rows), "\n")...)
}

// displayPath is path relative to the working directory when it is inside,
// else with the home directory shortened to ~.
func displayPath(path string) string {
	if path == "" {
		return path
	}
	if wd := cwd(); wd != "" {
		if rel, err := filepath.Rel(wd, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") && filepath.IsAbs(path) {
			return filepath.ToSlash(rel)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + filepath.ToSlash(path[len(home):])
	}
	return path
}

// scopeKind names a remember level for the card.
func (m *model) scopeKind(kind string, edit bool) string {
	switch kind {
	case "exact":
		if edit {
			return m.tr("this file")
		}
		return m.tr("exact")
	case "prefix":
		return m.tr("prefix")
	case "command":
		return m.tr("program")
	case "dir":
		return m.tr("folder")
	case "project":
		return m.tr("project")
	case "host":
		return m.tr("host")
	}
	return m.tr("tool")
}

func (m *model) permView() string {
	d := m.permDialog
	if d == nil {
		return ""
	}
	w := cardWidth(m.width)
	head := cardTitleStyle.Inherit(fgTok(tokStateWarn)).Render(glyphs.Warn) + " " + cardTitleStyle.Render(m.permTitle(d.req))

	body := m.permBody(d, w)
	tail := m.permTail(d, w, false)
	// On a short screen the body would be cut to a sliver: drop the spacing
	// and the mode note so the command or diff keeps a few rows.
	if room := m.cardMaxHeight() - 2 - len(tail); room < min(len(body), permMinBody) {
		tail = m.permTail(d, w, true)
	}
	body = d.scroll.window(body, m.cardMaxHeight()-2-len(tail))
	return renderCard(tokStateWarn, head, append(body, tail...), w)
}

// permMinBody is the body rows a card keeps before its tail is compacted.
const permMinBody = 6

// permTail is the card below its body: why it asks, the options with the
// remembered scope, and the key hints, one line each. compact leaves out the
// blank rows and the mode note.
func (m *model) permTail(d *permDialog, w int, compact bool) []string {
	inner := cardInner(w)
	gap := []string{""}
	if compact {
		gap = nil
	}
	tail := append([]string(nil), gap...)
	if d.why != "" && !compact {
		tail = append(tail, textMutedStyle.Render(wrap(chromeGlyphs(d.why), inner)))
	}
	if d.req.Description != "" {
		tail = append(tail, textSecondaryStyle.Render(wrap(d.req.Description, inner)))
	}
	if m.perms.saveErr != "" {
		tail = append(tail, warnStyle.Render(wrap(glyphs.Warn+" "+m.tr("a remembered rule was not saved: ")+m.perms.saveErr, inner)))
	}
	levels := d.scopes()
	if d.rejecting {
		tail = append(append(tail, gap...), accentFgStyle.Render(m.tr("reject with message: "))+d.rejectIn+"█",
			textHintStyle.Render(chromeGlyphs(m.tr("Enter sends · Esc Back"))))
	} else {
		always := m.tr("Yes, always allow")
		if rules := d.chosenRules(); len(rules) > 0 {
			always += " " + strings.Join(rules, ", ")
		}
		opts := []string{m.tr("Yes, once"), always, m.tr("No, and tell the agent why")}
		keys := []string{"a", "A", "r"}
		if len(tail) > 1 {
			tail = append(tail, gap...)
		}
		for i, o := range opts {
			tail = append(tail, cardOption(i == d.sel, strconv.Itoa(i+1), o+textHintStyle.Render(" ("+keys[i]+")"), w))
			if i != 1 {
				continue
			}
			switch {
			case len(levels) == 0:
				tail = append(tail, textMutedStyle.Render(wrapHanging("     ", m.tr("always allow is not available for this action; it allows once"), inner)))
			default:
				tail = append(tail, wrapHanging("     ", m.scopeLine(d), inner))
			}
		}
		hint := m.tr("↑↓ select · 1-3 or a/A/r")
		if compact {
			hint = m.tr("1-3 or a/A/r")
		}
		if len(levels) > 1 {
			hint += m.tr(" · Tab scope")
		}
		if p := d.req.Preview; p != nil && p.Diff != "" && len(strings.Split(strings.TrimRight(p.Diff, "\n"), "\n")) > permPreviewRows {
			if d.expanded {
				hint += m.tr(" · ctrl+e fold")
			} else {
				hint += m.tr(" · ctrl+e expand")
			}
		}
		hint += m.tr(" · Esc reject")
		tail = append(append(tail, gap...), textHintStyle.Render(chromeGlyphs(hint)))
	}
	return cardLines(tail, w)
}

// scopeLine is the row under "always allow": the breadths it can remember,
// the chosen one marked, and where the rule is kept.
func (m *model) scopeLine(d *permDialog) string {
	levels := d.scopes()
	edit := permissions.IsEdit(d.req.Tool)
	where := m.tr("kept for every project")
	if rules := d.chosenRules(); len(rules) > 0 && permissions.DefaultScope(rules[0]) == permissions.ScopeProject {
		where = m.tr("kept for this project")
	}
	if len(levels) < 2 {
		return textMutedStyle.Render(where)
	}
	kinds := make([]string, len(levels))
	for i, l := range levels {
		kinds[i] = m.scopeKind(l.Kind, edit)
	}
	return cardChoice(kinds, d.scope) + textMutedStyle.Render(chromeGlyphs(" · "+where))
}

func ansi_Truncate(s string, width int) string {
	if width <= 0 {
		width = 80
	}
	// Truncate by display width: a byte cut splits CJK runes and overflows.
	return ansi.Truncate(s, width, "…")
}

var _ = fmt.Sprint
