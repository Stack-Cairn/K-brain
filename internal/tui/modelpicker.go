package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

const dimNew = "  (new)"

type modelItem struct {
	model    string
	provider string
	url      string

	fromCatalog bool
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
}

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
		m.append(errStyle.Render(m.tr("no models configured in ~/.k-brain/config.json")))
		return
	}
	mp := &modelPicker{items: items, staleHints: staleCatalogs(m.cfg, config.LoadCatalogs()), sessionOnly: sessionOnly}
	for i, it := range items {
		if it.model == m.modelName && it.provider == m.provName {
			mp.idx = i
			break
		}
	}
	m.mpicker = mp
}

func (m *model) modelPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.mpicker
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.mpicker = nil
	case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab:
		if p.idx > 0 {
			p.idx--
		}
	case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
		if p.idx < len(p.view())-1 {
			p.idx++
		}
	case tea.KeyBackspace:
		if p.filter.backspace() {
			p.applyQuery()
			p.idx = 0
		}
	case tea.KeyEnter:
		v := p.view()
		if len(v) == 0 {
			return m, nil
		}
		it := v[p.idx]
		sessionOnly := p.sessionOnly
		m.mpicker = nil
		m.switchModel(it.model, it.provider, !sessionOnly)
	case tea.KeyRunes, tea.KeySpace:
		p.filter.typeRunes(msg.Runes)
		p.applyQuery()
		if p.idx >= len(p.view()) {
			p.idx = max(len(p.view())-1, 0)
		}
	}
	return m, nil
}

func (m *model) modelPickerView() string {
	p := m.mpicker
	view := p.view()
	var rows []string
	rows = append(rows, "  "+botStyle.Render("/")+p.filter.query+dimStyle.Render("▏"))
	lastModel := ""
	selRow := 0
	for i, it := range view {
		heading := " " + it.model
		if it.fromCatalog {
			heading = dimStyle.Render(heading + dimNew)
		}
		if it.model != lastModel {
			rows = append(rows, heading)
			lastModel = it.model
		}
		cur := ""
		if it.model == m.modelName && it.provider == m.provName {
			cur = dimStyle.Render(m.tr("  (current)"))
		}
		line := fmt.Sprintf("%-12s  ", it.provider) + dimStyle.Render(it.url)
		if it.fromCatalog {
			line = dimStyle.Render(line)
		}
		if i == p.idx {
			selRow = len(rows)
			rows = append(rows, botStyle.Render("   → "+line)+cur)
		} else {
			rows = append(rows, "     "+line+cur)
		}
	}
	if len(view) == 0 {
		rows = append(rows, dimStyle.Render(m.tr("  no models match ")+strconv.Quote(p.filter.query)))
	}
	rows = append(rows, dimStyle.Render(fmt.Sprintf(m.tr("  (%d/%d) Type to Filter · ↑/↓ Select · Enter Switch · Esc Cancel"), p.idx+1, len(view))))
	if len(p.staleHints) > 0 {
		rows = append(rows, dimStyle.Render(fmt.Sprintf(m.tr("  catalog stale for %s — /model refresh to pull newly announced models"), strings.Join(p.staleHints, ", "))))
	}
	avail := m.height - 1
	if avail < 1 {
		return strings.Join(rows, "\n")
	}
	for len(rows) < avail {
		rows = append(rows, "")
	}
	if len(rows) > avail {
		footer := 1
		if len(p.staleHints) > 0 {
			footer = 2
		}
		body := rows[1 : len(rows)-footer]
		lo, hi := viewportWindow(len(body), selRow-1, max(avail-1-footer, 1))
		rows = append(append([]string{rows[0]}, body[lo:hi]...), rows[len(rows)-footer:]...)
	}
	return strings.Join(rows, "\n")
}
