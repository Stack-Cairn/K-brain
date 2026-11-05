package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

const dimNew = "  (new)"

type modelItem struct {
	model    string
	provider string
	url      string

	fromCatalog bool
	// ctx is the route's context window in tokens, 0 when unknown.
	ctx int
}

type modelFilter struct {
	query string
	match []int
}

func (f *modelFilter) apply(rows int, score func(i int) int) {
	q := strings.ToLower(strings.TrimSpace(f.query))
	if q == "" {
		f.match = nil
		return
	}
	type hit struct {
		i, tier int
	}
	var hits []hit
	for i := range rows {
		if tier := score(i); tier >= 0 {
			hits = append(hits, hit{i, tier})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].tier < hits[b].tier })
	f.match = make([]int, 0, len(hits))
	for _, h := range hits {
		f.match = append(f.match, h.i)
	}
}

func (f *modelFilter) view(rows int) []int {
	if f.match == nil {
		idx := make([]int, rows)
		for i := range idx {
			idx[i] = i
		}
		return idx
	}
	return f.match
}

func (f *modelFilter) typeRunes(rs []rune) bool {
	if len(rs) == 0 {
		return false
	}
	f.query += string(rs)
	return true
}

func (f *modelFilter) backspace() bool {
	if f.query == "" {
		return false
	}
	f.query = f.query[:len(f.query)-1]
	return true
}

type modelPicker struct {
	items      []modelItem
	filter     modelFilter
	idx        int
	staleHints []string

	sessionOnly bool

	// The picker cascades: stage picks a model, then a route when the model
	// is served with more than one context window or endpoint, then the
	// reasoning effort for the model just switched to. idx stays an index
	// into view() throughout the first two stages.
	stage  pickerStage
	levels []string
	lidx   int
}

type pickerStage int

const (
	stageModel pickerStage = iota
	stageRoute
	stageEffort
)

func (p *modelPicker) view() []modelItem {
	idx := p.filter.view(len(p.items))
	out := make([]modelItem, len(idx))
	for i, j := range idx {
		out[i] = p.items[j]
	}
	return out
}

func (p *modelPicker) applyQuery() {
	q := strings.ToLower(strings.TrimSpace(p.filter.query))
	p.filter.apply(len(p.items), func(i int) int {
		it := p.items[i]
		return bestTier(it.model, it.provider, q)
	})
}

func (f *modelFilter) applyModelList(list []string) {
	q := strings.ToLower(strings.TrimSpace(f.query))
	f.apply(len(list), func(i int) int {
		name := strings.TrimSuffix(list[i], dimNew)
		if inner, ok := strings.CutPrefix(name, "default ("); ok {
			name = strings.TrimSuffix(inner, ")")
		}
		name, provider := splitRouteKey(name)
		return bestTier(name, provider, q)
	})
}

func bestTier(model, provider, q string) int {
	tm, tp := matchTier(model, q), matchTier(provider, q)
	switch {
	case tm >= 0 && tp >= 0:
		return min(tm, tp)
	case tm >= 0:
		return tm
	default:
		return tp
	}
}

func resolveModelFuzzy(cfg *config.Config, name string) (string, bool, []string) {
	if _, ok := cfg.Models[name]; ok {
		return name, true, nil
	}
	for p := range cfg.Providers {
		if cat, ok := config.LoadCatalogs()[p]; ok && cat.Find(name) != nil {
			return name, true, nil
		}
	}
	q := strings.ToLower(name)
	type hit struct {
		model string
		tier  int
	}
	var hits []hit
	for _, it := range buildModelItems(cfg) {
		if tier := bestTier(it.model, it.provider, q); tier >= 0 {
			hits = append(hits, hit{it.model, tier})
		}
	}
	if len(hits) == 0 {
		return "", false, nil
	}
	best := hits[0].tier
	seen := map[string]bool{}
	var models []string
	for _, h := range hits {
		if h.tier != best || seen[h.model] {
			continue
		}
		seen[h.model] = true
		models = append(models, h.model)
	}
	if len(models) > 1 {
		return "", false, models
	}
	return models[0], true, nil
}

func routeKey(it modelItem) string {
	k := it.model + "@" + it.provider
	if it.fromCatalog {
		k += dimNew
	}
	return k
}

func splitRouteKey(row string) (model, provider string) {
	model, provider, _ = strings.Cut(strings.TrimSuffix(row, dimNew), "@")
	return model, provider
}

func buildModelItems(cfg *config.Config) []modelItem {
	names := make([]string, 0, len(cfg.Models))
	for name := range cfg.Models {
		names = append(names, name)
	}
	sort.Strings(names)
	var items []modelItem
	for _, name := range names {
		for _, p := range cfg.Models[name].Providers {
			url := ""
			if prov, ok := cfg.Providers[p]; ok {
				url = prov.BaseURL
			}
			items = append(items, modelItem{model: name, provider: p, url: endpointLabel(url)})
		}
	}
	return appendCatalogRoutes(items, cfg, config.LoadCatalogs())
}

func endpointLabel(baseURL string) string {
	return baseURL
}

func appendCatalogRoutes(items []modelItem, cfg *config.Config, cats map[string]config.Catalog) []modelItem {
	provs := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		provs = append(provs, name)
	}
	sort.Strings(provs)
	var extra []modelItem
	for _, p := range provs {
		cat, ok := cats[p]
		if !ok {
			continue
		}
		for _, mi := range cat.Models {
			if _, configured := cfg.Models[mi.ID]; configured {
				continue
			}
			extra = append(extra, modelItem{model: mi.ID, provider: p, url: endpointLabel(cat.BaseURL), fromCatalog: true})
		}
	}
	sort.Slice(extra, func(a, b int) bool {
		if extra[a].provider != extra[b].provider {
			return extra[a].provider < extra[b].provider
		}
		return extra[a].model < extra[b].model
	})
	return append(items, extra...)
}

func staleCatalogs(cfg *config.Config, cats map[string]config.Catalog) []string {
	var out []string
	for name := range cfg.Providers {
		if cat, ok := cats[name]; !ok || cat.Stale() {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func (m *model) openModelPicker(sessionOnly bool) {
	items := buildModelItems(m.cfg)
	if len(items) == 0 {
		m.append(errStyle.Render(m.tr("no models configured in ~/.liveagent/config.json")))
		return
	}
	cats := config.LoadCatalogs()
	for i := range items {
		items[i].ctx = routeContext(m.cfg, cats, items[i])
	}
	mp := &modelPicker{items: items, staleHints: staleCatalogs(m.cfg, cats), sessionOnly: sessionOnly}
	for i, it := range items {
		if it.model == m.modelName && it.provider == m.provName {
			mp.idx = i
			break
		}
	}
	m.mpicker = mp
}

// routeContext is a route's context window: the provider's metadata for the
// model, else the provider catalog's.
func routeContext(cfg *config.Config, cats map[string]config.Catalog, it modelItem) int {
	if mc, ok := cfg.Models[it.model]; ok {
		if n := mc.ForProvider(it.provider).ContextWindow(); n > 0 {
			return n
		}
	}
	if c, ok := cats[it.provider]; ok {
		return c.ContextLength(it.model)
	}
	return 0
}

// modelGroups splits view into runs of the same model: each entry is the
// [start, end) range of one model's routes.
func modelGroups(view []modelItem) [][2]int {
	var out [][2]int
	for i := 0; i < len(view); {
		j := i + 1
		for j < len(view) && view[j].model == view[i].model {
			j++
		}
		out = append(out, [2]int{i, j})
		i = j
	}
	return out
}

// groupOf is the index in groups of the group holding route idx.
func groupOf(groups [][2]int, idx int) int {
	for g, r := range groups {
		if idx >= r[0] && idx < r[1] {
			return g
		}
	}
	return 0
}

func (m *model) modelPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.mpicker
	switch p.stage {
	case stageEffort:
		m.effortStageKey(msg)
		return m, nil
	case stageRoute:
		m.routeStageKey(msg)
		return m, nil
	}
	view := p.view()
	groups := modelGroups(view)
	g := groupOf(groups, p.idx)
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.mpicker = nil
	case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab:
		if g > 0 {
			p.idx = groups[g-1][0]
		}
	case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
		if g < len(groups)-1 {
			p.idx = groups[g+1][0]
		}
	case tea.KeyBackspace:
		if p.filter.backspace() {
			p.applyQuery()
			p.idx = 0
		}
	case tea.KeyEnter:
		if len(view) == 0 {
			return m, nil
		}
		if r := groups[g]; r[1]-r[0] > 1 {
			p.stage = stageRoute
			for i := r[0]; i < r[1]; i++ {
				if view[i].provider == m.provName && view[i].model == m.modelName {
					p.idx = i
				}
			}
			return m, nil
		}
		m.pickRoute(view[p.idx])
	case tea.KeyRunes, tea.KeySpace:
		p.filter.typeRunes(msg.Runes)
		p.applyQuery()
		if p.idx >= len(p.view()) {
			p.idx = max(len(p.view())-1, 0)
		}
	}
	return m, nil
}

// routeStageKey picks among the selected model's routes.
func (m *model) routeStageKey(msg tea.KeyMsg) {
	p := m.mpicker
	view := p.view()
	r := modelGroups(view)[groupOf(modelGroups(view), p.idx)]
	switch msg.Type {
	case tea.KeyCtrlC:
		m.mpicker = nil
	case tea.KeyEsc, tea.KeyLeft:
		p.stage = stageModel
		p.idx = r[0]
	case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab:
		p.idx = max(p.idx-1, r[0])
	case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
		p.idx = min(p.idx+1, r[1]-1)
	case tea.KeyEnter:
		m.pickRoute(view[p.idx])
	}
}

// pickRoute switches to it and moves on to the effort step when the model
// offers a choice of effort.
func (m *model) pickRoute(it modelItem) {
	p := m.mpicker
	m.switchModel(it.model, it.provider, !p.sessionOnly)
	if m.agent == nil || m.modelName != it.model || m.provName != it.provider {
		m.mpicker = nil
		return
	}
	levels := m.effortsFor()
	if len(levels) < 2 {
		m.mpicker = nil
		return
	}
	p.stage, p.levels, p.lidx = stageEffort, levels, 0
	for i, l := range levels {
		if l == m.agent.Effort {
			p.lidx = i
		}
	}
}

// effortStageKey picks the reasoning effort; Esc keeps the current one.
func (m *model) effortStageKey(msg tea.KeyMsg) {
	p := m.mpicker
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.mpicker = nil
	case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab:
		p.lidx = max(p.lidx-1, 0)
	case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
		p.lidx = min(p.lidx+1, len(p.levels)-1)
	case tea.KeyEnter:
		if lv := p.levels[p.lidx]; lv != m.agent.Effort {
			m.setEffort(lv)
		}
		m.mpicker = nil
	}
}

// ctxLabel is a context window for the picker, e.g. "128k ctx".
func ctxLabel(n int) string {
	if n <= 0 {
		return ""
	}
	switch {
	case n%(1<<20) == 0:
		return strconv.Itoa(n>>20) + "M ctx"
	case n%1_000_000 == 0:
		return strconv.Itoa(n/1_000_000) + "M ctx"
	case n%1024 == 0:
		return strconv.Itoa(n/1024) + "k ctx"
	case n >= 1000:
		return strconv.Itoa(n/1000) + "k ctx"
	}
	return strconv.Itoa(n) + " ctx"
}

// groupSummary describes one model's routes on its model row.
func groupSummary(routes []modelItem) string {
	if len(routes) == 1 {
		s := routes[0].provider
		if c := ctxLabel(routes[0].ctx); c != "" {
			s += " · " + c
		}
		return s
	}
	lo, hi := 0, 0
	for _, r := range routes {
		if r.ctx > 0 && (lo == 0 || r.ctx < lo) {
			lo = r.ctx
		}
		hi = max(hi, r.ctx)
	}
	s := fmt.Sprintf("%d routes", len(routes))
	switch {
	case lo > 0 && lo != hi:
		s += " · " + strings.TrimSuffix(ctxLabel(lo), " ctx") + "–" + ctxLabel(hi)
	case hi > 0:
		s += " · " + ctxLabel(hi)
	}
	return s
}

func (m *model) modelPickerView() string {
	p := m.mpicker
	switch p.stage {
	case stageRoute:
		return m.fitPickerSelection(m.routeStageRows())
	case stageEffort:
		return m.fitPickerSelection(m.effortStageRows())
	}
	view := p.view()
	groups := modelGroups(view)
	g := groupOf(groups, p.idx)
	var rows []string
	rows = append(rows, "  "+botStyle.Render("/")+p.filter.query+dimStyle.Render(chromeGlyphs("▏")))
	selRow := 0
	nameW := 0
	for _, r := range groups {
		nameW = max(nameW, len(view[r[0]].model))
	}
	nameW = min(nameW, max(m.width/2, 12))
	for gi, r := range groups {
		it := view[r[0]]
		name := it.model
		if it.fromCatalog {
			name += dimNew
		}
		name = fmt.Sprintf("%-*s", nameW, name)
		summary := groupSummary(view[r[0]:r[1]])
		cur := ""
		if it.model == m.modelName {
			cur = dimStyle.Render(m.tr("  (current)"))
		}
		if gi == g {
			selRow = len(rows)
			rows = append(rows, botStyle.Render(chromeGlyphs(" → ")+name)+"  "+dimStyle.Render(summary)+cur)
			continue
		}
		line := "   " + name + "  " + dimStyle.Render(summary)
		if it.fromCatalog {
			line = dimStyle.Render("   " + name + "  " + summary)
		}
		rows = append(rows, line+cur)
	}
	if len(view) == 0 {
		rows = append(rows, dimStyle.Render(m.tr("  no models match ")+strconv.Quote(p.filter.query)))
	}
	footer := []string{dimStyle.Render(chromeGlyphs(fmt.Sprintf(m.tr("  (%d/%d) Type to Filter · ↑/↓ Select · Enter Switch · Esc Cancel"), min(g+1, len(groups)), len(groups))))}
	if len(p.staleHints) > 0 {
		footer = append(footer, dimStyle.Render(chromeGlyphs(fmt.Sprintf(m.tr("  catalog stale for %s — /model refresh to pull newly announced models"), strings.Join(p.staleHints, ", ")))))
	}
	return m.fitPickerSelection(rows, footer, selRow)
}

// routeStageRows lists the selected model's routes: provider, context
// window and endpoint.
func (m *model) routeStageRows() ([]string, []string, int) {
	p := m.mpicker
	view := p.view()
	r := modelGroups(view)[groupOf(modelGroups(view), p.idx)]
	model := view[r[0]].model
	rows := []string{"  " + botStyle.Render(model) + dimStyle.Render(chromeGlyphs(" — "+m.tr("choose a route and context window")))}
	selRow := 0
	for i := r[0]; i < r[1]; i++ {
		it := view[i]
		ctx := ctxLabel(it.ctx)
		if ctx == "" {
			ctx = "—"
		}
		line := fmt.Sprintf("%-12s  %-10s  ", it.provider, chromeGlyphs(ctx)) + dimStyle.Render(it.url)
		cur := ""
		if it.model == m.modelName && it.provider == m.provName {
			cur = dimStyle.Render(m.tr("  (current)"))
		}
		if i == p.idx {
			selRow = len(rows)
			rows = append(rows, botStyle.Render(chromeGlyphs("   → ")+line)+cur)
		} else {
			rows = append(rows, "     "+line+cur)
		}
	}
	footer := []string{dimStyle.Render(chromeGlyphs(m.tr("  ↑/↓ Select · Enter Switch · Esc Back")))}
	return rows, footer, selRow
}

// effortStageRows lists the reasoning efforts of the model just switched to.
func (m *model) effortStageRows() ([]string, []string, int) {
	p := m.mpicker
	rows := []string{"  " + botStyle.Render(m.modelName+"@"+m.provName) + dimStyle.Render(chromeGlyphs(" — "+m.tr("reasoning effort")))}
	selRow := 0
	for i, lv := range p.levels {
		line := fmt.Sprintf("%-8s  ", effortLabel(lv)) + dimStyle.Render(m.tr(effortDescription(lv)))
		cur := ""
		if lv == m.agent.Effort {
			cur = dimStyle.Render(m.tr("  (current)"))
		}
		if i == p.lidx {
			selRow = len(rows)
			rows = append(rows, botStyle.Render(chromeGlyphs("   → ")+line)+cur)
		} else {
			rows = append(rows, "     "+line+cur)
		}
	}
	footer := []string{dimStyle.Render(chromeGlyphs(m.tr("  ↑/↓ Select · Enter Apply · Esc Keep current")))}
	return rows, footer, selRow
}

// fitPickerSelection lays out the picker: rows[0] stays on top, the footer
// at the bottom, and the body scrolls to keep selRow in view.
func (m *model) fitPickerSelection(rows, footer []string, selRow int) string {
	avail := m.height - 1
	if avail < 1 {
		return strings.Join(append(rows, footer...), "\n")
	}
	if len(rows)+len(footer) > avail {
		body := rows[1:]
		lo, hi := viewportWindow(len(body), selRow-1, max(avail-1-len(footer), 1))
		rows = append([]string{rows[0]}, body[lo:hi]...)
	}
	out := rows
	for len(out)+len(footer) < avail {
		out = append(out, "")
	}
	out = append(out, footer...)
	if m.width > 0 {
		for i, l := range out {
			out[i] = ansi.Truncate(l, m.width, ellipsis())
		}
	}
	return strings.Join(out, "\n")
}
