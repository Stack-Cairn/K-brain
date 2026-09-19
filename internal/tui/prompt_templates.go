package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/prompts/templates"
)

type promptCatalog struct {
	root    string
	home    string
	trusted bool
	loaded  bool
	items   []templates.Template
	errors  []error
}

func reservedPrompt(name string) bool {
	if name == "review" {
		return false
	}
	if registryFind("/"+name) != nil {
		return true
	}
	switch name {
	case "exit", "q", "tasks", "computer", "lsp", "auth":
		return true
	}
	return false
}

func (m *model) loadPrompts(refresh bool) {
	root, err := currentRoot()
	if err != nil {
		m.promptCatalog = promptCatalog{errors: []error{err}}
		return
	}
	home, err := config.Dir()
	if err != nil {
		m.promptCatalog = promptCatalog{errors: []error{err}}
		return
	}
	trusted := config.Trusted(root)
	old := m.promptCatalog
	if !refresh && old.loaded && old.root == root && old.home == home && old.trusted == trusted {
		return
	}
	dirs := []string{filepath.Join(home, "prompts")}
	if trusted {
		dirs = append(dirs, filepath.Join(root, ".k-brain", "prompts"))
	}
	items, problems := templates.Load(dirs...)
	m.promptCatalog = promptCatalog{root: root, home: home, trusted: trusted, loaded: true, errors: problems}
	for _, tpl := range items {
		if !reservedPrompt(tpl.Name) {
			m.promptCatalog.items = append(m.promptCatalog.items, tpl)
		}
	}
}

func (m *model) isPromptCommand(text string) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return false
	}
	m.loadPrompts(false)
	for _, tpl := range m.promptCatalog.items {
		if fields[0] == "/"+tpl.Name {
			return true
		}
	}
	return false
}

func (m *model) expandPromptCommand(text string) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false
	}
	m.loadPrompts(false)
	for _, tpl := range m.promptCatalog.items {
		if fields[0] != "/"+tpl.Name {
			continue
		}
		expanded, err := tpl.Expand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
		if err != nil {
			m.append(errStyle.Render("/" + tpl.Name + ": " + err.Error()))
			return true
		}
		if strings.TrimSpace(expanded) == "" {
			m.append(errStyle.Render("/" + tpl.Name + ": template expanded to empty text"))
			return true
		}
		previous := m.input.Value()
		m.input.SetValue(expanded)
		if m.input.Value() != expanded {
			m.input.SetValue(previous)
			m.append(errStyle.Render("/" + tpl.Name + ": expanded template exceeds editor limits or contains unsupported control characters"))
			return true
		}
		m.input.CursorEnd()
		m.menu = nil
		m.append(dimStyle.Render("prompt expanded — review and press Enter to send"))
		return true
	}
	return false
}

func (m *model) promptCompletions(val string) (string, []cand) {
	head, cands := completions(val, m.modelCands(), m.providerCands(), m.skillCands(), effortCandsFor(m.effortsFor()))
	if strings.HasPrefix(val, "/") && !strings.ContainsAny(val, " \t\n") {
		m.loadPrompts(false)
		for _, tpl := range m.promptCatalog.items {
			if strings.HasPrefix("/"+tpl.Name, val) {
				for i := len(cands) - 1; i >= 0; i-- {
					if cands[i].Text == "/"+tpl.Name {
						cands = append(cands[:i], cands[i+1:]...)
					}
				}
				desc := tpl.Description
				cands = append(cands, cand{Text: "/" + tpl.Name, Desc: desc})
			}
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i].Text < cands[j].Text })
	}
	for i := range cands {
		isTemplate := false
		for _, tpl := range m.promptCatalog.items {
			if cands[i].Text == "/"+tpl.Name {
				isTemplate = true
				break
			}
		}
		if e := registryFind(cands[i].Text); head == "" && e != nil && !isTemplate {
			cands[i].Desc = m.tr(e.Hint)
		} else {
			cands[i].Desc = m.tr(cands[i].Desc)
		}
	}
	return head, cands
}

func (m *model) promptsCommand(args []string) {
	if len(args) > 1 || len(args) == 1 && args[0] != "refresh" {
		m.append(errStyle.Render("usage: /prompts [refresh]"))
		return
	}
	m.loadPrompts(len(args) == 1)
	var b strings.Builder
	b.WriteString("Prompt templates\nGlobal: ~/.k-brain/prompts/*.md\nProject: .k-brain/prompts/*.md (trusted projects only)\n")
	if !m.promptCatalog.trusted {
		b.WriteString("Project templates disabled: project is not trusted.\n")
	}
	if len(m.promptCatalog.items) == 0 {
		b.WriteString("No templates loaded.\n")
	}
	for _, tpl := range m.promptCatalog.items {
		fmt.Fprintf(&b, "/%s %s — %s\n  %s\n", tpl.Name, tpl.ArgumentHint, tpl.Description, tpl.Path)
	}
	for _, err := range m.promptCatalog.errors {
		fmt.Fprintf(&b, "Error: %v\n", err)
	}
	b.WriteString("Built-in commands take precedence. Use /prompts refresh after editing files.")
	m.append(dimStyle.Render(b.String()))
}
