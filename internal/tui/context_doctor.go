package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/skills"
)

type ctxRow struct {
	label string
	bytes int
	note  string
}

func (r ctxRow) tokens() int { return (r.bytes + 3) / 4 }

func (m *model) doctorReport() string {
	var rows []ctxRow

	rows = append(rows, ctxRow{"system prompt (base)", len(m.sysPrompt), ""})

	scan := m.skillScan
	if scan == nil {
		scan = func() []skills.Skill { return skills.Scan(skills.DefaultDirs()...) }
	}
	sk := scan()
	block := skills.PromptBlock(sk)
	row := ctxRow{fmt.Sprintf("skills (%d loaded)", len(sk)), len(block), ""}

	type sc struct {
		name string
		dir  string
		n    int
	}
	per := make([]sc, 0, len(sk))
	for _, s := range sk {
		n := len(s.Name) + min(len(s.Description), 300) + len(s.Path) + 8
		per = append(per, sc{s.Name, filepath.Dir(filepath.Dir(s.Path)), n})
	}
	sort.Slice(per, func(i, j int) bool { return per[i].n > per[j].n })
	var top []string
	for i := 0; i < len(per) && i < 5; i++ {
		top = append(top, fmt.Sprintf("%s ~%dtok (%s)", per[i].name, (per[i].n+3)/4, shortSkillsDir(per[i].dir)))
	}
	if len(top) > 0 {
		row.note = "biggest: " + strings.Join(top, ", ")
	}
	rows = append(rows, row)

	if m.mcpMgr != nil {
		toolBytes := map[string]int{}
		for _, t := range m.mcpMgr.Tools() {
			n := t.Def.Function.Name
			srv := n
			if i := strings.Index(strings.TrimPrefix(n, "mcp__"), "__"); i >= 0 {
				srv = strings.TrimPrefix(n, "mcp__")[:i]
			}
			schema, _ := json.Marshal(t.Def)
			toolBytes[srv] += len(schema) + len(n) + 8
		}
		for _, st := range m.mcpMgr.Statuses() {
			switch st.Status {
			case mcp.StatusReady:
				b := toolBytes[st.Name]
				rows = append(rows, ctxRow{fmt.Sprintf("mcp: %s (%d tools)", st.Name, st.Tools), b, ""})
			case mcp.StatusFailed:
				rows = append(rows, ctxRow{"mcp: " + st.Name, 0, "failed — contributes 0 tools"})
			case mcp.StatusDisabled:
				rows = append(rows, ctxRow{"mcp: " + st.Name, 0, "disabled"})
			default:
				rows = append(rows, ctxRow{"mcp: " + st.Name, 0, "still connecting — 0 tools yet"})
			}
		}
		if ib := m.mcpMgr.InstructionsBlock(); ib != "" {
			rows = append(rows, ctxRow{"mcp: server instructions", len(ib), ""})
		}
	}
	if m.pluginMgr != nil {
		pluginBytes := len(m.pluginMgr.PromptBlock())
		rows = append(rows, ctxRow{fmt.Sprintf("plugins (%d enabled)", len(m.pluginMgr.Tools())), pluginBytes, "prompt and tool schemas"})
	}

	var tb int
	for _, t := range m.agent.AllTools() {
		schema, _ := json.Marshal(t.Def)
		tb += len(schema) + 8
	}
	rows = append(rows, ctxRow{fmt.Sprintf("tool schemas (%d tools)", len(m.agent.AllTools())), tb, "sent with every request"})

	hist := agent.EstimateTokens(m.agent.Messages)
	if hist > 0 {
		rows = append(rows, ctxRow{"conversation history", hist * 4, "estimated"})
	}

	if u := m.agent.TotalUsage(); u.PromptTokens > 0 {
		rows = append(rows, ctxRow{"session spend so far", 0, fmt.Sprintf("%s in / %s out (actual)", tok(u.PromptTokens), tok(u.CompletionTokens))})
	}

	var b strings.Builder
	b.WriteString("Fresh-session context audit (estimated tokens)\n")
	total := 0
	w := 0
	for _, r := range rows {
		if len(r.label) > w {
			w = len(r.label)
		}
		total += r.tokens()
	}
	for _, r := range rows {
		line := fmt.Sprintf("  %-*s %7s", w, r.label, "~"+tok(r.tokens()))
		if r.note != "" {
			line += "  " + r.note
		}
		b.WriteString(line + "\n")
	}
	fmt.Fprintf(&b, "  %-*s %7s\n", w, "TOTAL injected before you type", "~"+tok(total))
	b.WriteString("\nTrim: /mcp <name> disable · /plugins disable NAME · remove a skill from .agents/skills · /context-doctor again")
	return b.String()
}

func shortSkillsDir(dir string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			if rel == "." {
				return "~"
			}
			return "~" + string(filepath.Separator) + rel
		}
	}
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, dir); err == nil && !strings.HasPrefix(rel, "..") {
			if rel == "." {
				return "."
			}
			return "." + string(filepath.Separator) + rel
		}
	}
	return dir
}

func tok(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return strconv.Itoa(n)
}
