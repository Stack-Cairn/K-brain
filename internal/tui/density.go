package tui

import (
	"slices"
	"strings"
)

// Layout density. Comfortable is the default layout. Compact trades
// whitespace and decoration for rows:
//   - blocks sit back to back, with one blank row only before each prompt;
//   - a tool call is its header row alone: its output stays folded away
//     until it is opened (Alt+O, or a click on the row), unless the call
//     failed; a running call drops its live output tail;
//   - the busy row keeps only the clocks and the stop key, and the context
//     readout drops its bar;
//   - blocking cards lose their frame: a titled rule and a rail.

const (
	densityComfortable = "comfortable"
	densityCompact     = "compact"
)

// compactDensity is the active density; like the glyph set it is read by
// renderers that have no model at hand.
var compactDensity bool

// densityChoices is what /density and the settings row offer.
var densityChoices = []string{densityComfortable, densityCompact}

// densityName is the saved density, normalised.
func (m *model) densityName() string {
	if m.cfg != nil && strings.EqualFold(strings.TrimSpace(m.cfg.Density), densityCompact) {
		return densityCompact
	}
	return densityComfortable
}

// applyDensity makes the saved density the active one and redraws the
// transcript when it changed.
func (m *model) applyDensity() {
	on := m.densityName() == densityCompact
	if on == compactDensity {
		return
	}
	compactDensity = on
	m.restyleTranscript()
}

// setDensity saves and applies a density.
func (m *model) setDensity(name string) {
	if m.cfg == nil {
		return
	}
	m.cfg.Density = name
	if name == densityComfortable {
		m.cfg.Density = ""
	}
	if m.cfgExtra == nil {
		m.cfgExtra = map[string]string{}
	}
	m.cfgExtra["density"] = name
	if err := m.cfg.Save(); err != nil {
		m.append(errStyle.Render("config save failed: " + err.Error()))
	}
	m.applyDensity()
}

// densityCommand is /density [compact|comfortable]; bare, it says which
// density is on.
func (m *model) densityCommand(args []string) {
	usage := "/density " + strings.Join(densityChoices, "|")
	if len(args) == 0 {
		m.append(dimStyle.Render(m.tr("density: ") + m.densityName() + "  ·  " + usage))
		return
	}
	name := strings.ToLower(args[0])
	if len(args) > 1 || !slices.Contains(densityChoices, name) {
		m.append(errStyle.Render("usage: " + usage))
		return
	}
	m.setDensity(name)
	m.append(dimStyle.Render(m.tr("density: ") + name))
}

// gapBefore reports whether a blank row separates block i from the block
// drawn before it: unless it hangs under that block, and in compact density
// only before a prompt.
func gapBefore(blocks []block, i int) bool {
	if gluedToPrev(blocks, i) {
		return false
	}
	return !compactDensity || blocks[i].kind == blockUser
}

// compactHidden reports whether block i is a tool result compact density
// folds away entirely: a successful call's output, while neither it nor its
// header has been opened. Read groups and subagent reports are their own
// one-line summaries and stay.
func compactHidden(blocks []block, i int) bool {
	if !compactDensity {
		return false
	}
	b := &blocks[i]
	if b.kind != blockTool || b.expanded || toolGroupBlock(b) || toolKindOf(b.toolName) == tkSubagent {
		return false
	}
	if strings.HasPrefix(b.text, "Error") || strings.Contains(b.text, "Hook PostToolUse") {
		return false
	}
	if o, _ := toolOutcomeOf(b); o != outcomeOK {
		return false
	}
	if i > 0 && toolHead(blocks[i-1]) {
		h := &blocks[i-1]
		if h.expanded || h.toolFailed || h.toolRunning {
			return false
		}
		if o, _ := toolOutcomeOf(h); o != outcomeOK {
			return false
		}
	}
	return true
}
