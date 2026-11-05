package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Verb groups. Reads and searches are context the model gathers; a run of
// them back to back folds into one row that says what was done:
//
//	◆ Read 3 files, searched 2 patterns                        ✓ 0.4s
//	  └ status.go, main.go, view.go, asJSON, TODO
//
// Expanded (ctrl+e or a click), the group lists each call with its own count
// and duration. Only finished calls that went fine join a group: a failure,
// a running call or any other tool ends the run, and a result the reader
// expanded by hand stays as it is. A group is a blockTool whose view
// metadata carries the calls.

// groupMember is one call inside a verb group.
type groupMember struct {
	id, name, args, result string
	run                    *toolRunMeta
	// dur is the call's duration as of grouping; -1 when unknown.
	dur time.Duration
}

// groupableTool reports read-only tools whose calls fold into verb groups.
func groupableTool(name string) bool {
	switch name {
	case "read", "Read", "list", "List", "grep", "Grep", "glob", "Glob", "web_fetch", "WebFetch", "Image":
		return true
	}
	return false
}

// toolGroupBlock reports whether b is a verb group.
func toolGroupBlock(b *block) bool {
	v := b.viewMeta()
	return b.kind == blockTool && v != nil && v.group != nil
}

// groupToolRuns folds every run of two or more eligible calls in the
// transcript into a group, merging into groups already there.
func (m *model) groupToolRuns() {
	for end := len(m.blocks); end > 0; {
		start := end
		for start > 0 && toolFamily(m.blocks[start-1]) {
			start--
		}
		if start == end {
			end--
			continue
		}
		m.groupRange(start, end)
		end = start
	}
}

// groupTrailingTools folds the tool run at the end of the transcript, the one
// a live turn is adding to.
func (m *model) groupTrailingTools() {
	end := len(m.blocks)
	start := end
	for start > 0 && toolFamily(m.blocks[start-1]) {
		start--
	}
	if start < end {
		m.groupRange(start, end)
	}
}

// groupRange folds the eligible runs among blocks [start, end).
func (m *model) groupRange(start, end int) {
	type unit struct {
		at, n   int
		members []groupMember
	}
	var units []unit
	for i := start; i < end; {
		b := &m.blocks[i]
		switch {
		case toolGroupBlock(b):
			units = append(units, unit{at: i, n: 1, members: b.viewMeta().group})
			i++
			continue
		case b.kind == blockToolRun && i+1 < end && groupableCall(b, &m.blocks[i+1]):
			r := &m.blocks[i+1]
			units = append(units, unit{at: i, n: 2, members: []groupMember{{
				id: b.toolID, name: b.toolName, args: b.toolArgs, result: r.text, run: b.run, dur: toolDurationOf(b),
			}}})
			i += 2
			continue
		}
		units = append(units, unit{at: i, n: 1})
		i++
	}
	// Fold from the back so earlier indices stay put.
	for j := len(units); j > 0; {
		k := j
		for k > 0 && units[k-1].members != nil {
			k--
		}
		if k == j {
			j--
			continue
		}
		run := units[k:j]
		j = k
		if len(run) < 2 {
			continue
		}
		var members []groupMember
		expanded := false
		for _, u := range run {
			members = append(members, u.members...)
			if toolGroupBlock(&m.blocks[u.at]) {
				expanded = expanded || m.blocks[u.at].expanded
			}
		}
		g := block{kind: blockTool, toolName: "tool_group", expanded: expanded, run: &toolRunMeta{view: &toolViewMeta{group: members}}}
		last := run[len(run)-1]
		m.replaceBlocks(run[0].at, last.at+last.n, g)
	}
}

// groupableCall reports whether a finished header and its result can join a
// verb group.
func groupableCall(h, r *block) bool {
	if h.toolRunning || !groupableTool(h.toolName) || h.expanded || r.expanded {
		return false
	}
	if r.kind != blockTool || r.toolName != h.toolName || toolGroupBlock(r) {
		return false
	}
	if o, _ := toolOutcomeOf(h); o != outcomeOK || strings.HasPrefix(r.text, "Error") {
		return false
	}
	if v := h.viewMeta(); v != nil && len(v.images) > 0 {
		return false
	}
	return true
}

// replaceBlocks swaps blocks [a, b) for one block, keeping msgBlock's
// indices on the blocks they named.
func (m *model) replaceBlocks(a, b int, nb block) {
	m.blocks = slices.Replace(m.blocks, a, b, nb)
	shift := b - a - 1
	for i, at := range m.msgBlock {
		switch {
		case at >= b:
			m.msgBlock[i] = at - shift
		case at > a:
			m.msgBlock[i] = a
		}
	}
}

// groupVerb is how a tool's calls read in a group's summary.
type groupVerb struct{ verb, one, many string }

func groupVerbOf(name string) (key string, v groupVerb) {
	switch name {
	case "read", "Read":
		return "read", groupVerb{"read", "file", "files"}
	case "list", "List":
		return "list", groupVerb{"listed", "directory", "directories"}
	case "grep", "Grep", "glob", "Glob":
		return "search", groupVerb{"searched", "pattern", "patterns"}
	case "web_fetch", "WebFetch":
		return "fetch", groupVerb{"fetched", "page", "pages"}
	case "Image":
		return "image", groupVerb{"viewed", "image", "images"}
	}
	return name, groupVerb{"ran", name, name}
}

// toolGroupSummary is "Read 3 files, searched 2 patterns", verbs in the order
// they first ran.
func toolGroupSummary(members []groupMember) string {
	var keys []string
	counts := map[string]int{}
	verbs := map[string]groupVerb{}
	for _, mb := range members {
		k, v := groupVerbOf(mb.name)
		if counts[k] == 0 {
			keys = append(keys, k)
			verbs[k] = v
		}
		counts[k]++
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		v, n := verbs[k], counts[k]
		noun := v.many
		if n == 1 {
			noun = v.one
		}
		parts[i] = fmt.Sprintf("%s %d %s", v.verb, n, noun)
	}
	s := strings.Join(parts, ", ")
	if s != "" {
		s = strings.ToUpper(s[:1]) + s[1:]
	}
	return s
}

// groupDuration is the calls' total time; -1 when none is known.
func groupDuration(members []groupMember) time.Duration {
	total, known := time.Duration(0), false
	for _, mb := range members {
		if d := memberDuration(mb); d >= 0 {
			total += d
			known = true
		}
	}
	if !known {
		return -1
	}
	return total
}

func memberDuration(mb groupMember) time.Duration {
	if mb.run != nil && mb.run.duration > 0 {
		return mb.run.duration
	}
	return mb.dur
}

// shortSubject is a call's subject for the group's one-line list: a path's
// base name, else the subject as is.
func shortSubject(name, args string) string {
	s := toolSubject(name, args)
	if k, _ := groupVerbOf(name); k == "read" || k == "list" {
		if i := strings.LastIndexAny(s, `/\`); i >= 0 && i < len(s)-1 {
			s = s[i+1:]
		}
	}
	return s
}

// renderToolGroup draws a verb group: the summary row, then either the list
// of subjects or, expanded, one row per call.
func renderToolGroup(b *block, width int) string {
	members := b.viewMeta().group
	head := textMutedStyle.Render(glyphs.ToolDone) + " " + toolNameStyle.Render(toolGroupSummary(members))
	out := []string{withMeta(chromeGlyphs(head), outcomeMeta(outcomeOK, 0, groupDuration(members)), width)}
	if !b.expanded {
		subjects := make([]string, len(members))
		for i, mb := range members {
			subjects[i] = shortSubject(mb.name, mb.args)
		}
		elbow := textMutedStyle.Render("  " + glyphs.Result + " ")
		out = append(out, truncCells(elbow+textMutedStyle.Render(chromeGlyphs(strings.Join(subjects, ", "))), width))
		return strings.Join(out, "\n")
	}
	for _, mb := range members {
		row := "  " + toolRowText(textMutedStyle.Render(glyphs.ToolDone), mb.name, toolSubject(mb.name, mb.args), textSecondaryStyle)
		lines := strings.Count(strings.TrimRight(mb.result, "\n"), "\n") + 1
		meta := textMutedStyle.Render(quietResultSummary(mb.name, mb.result, lines))
		if d := memberDuration(mb); d >= 0 {
			meta += textMutedStyle.Render(chromeGlyphs(" · ") + fmtToolDuration(d))
		}
		out = append(out, withMeta(chromeGlyphs(row), meta, width))
	}
	return strings.Join(out, "\n")
}
