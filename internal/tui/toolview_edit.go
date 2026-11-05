package tui

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The edit family: edit and write. The header carries the path and the
// change's "+2 −1" counts; the body is the diff itself (diff.go), folded past
// diffPreviewRows rows. A write that created a file has no diff in its result,
// so its content, from the call's arguments, shows as added rows.

// editArgs are the fields of an edit or write call the view reads.
type editArgs struct {
	Path     string `json:"path"`
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

func parseEditArgs(args string) editArgs {
	var a editArgs
	_ = json.Unmarshal([]byte(args), &a)
	if a.Path == "" {
		a.Path = a.FilePath
	}
	return a
}

// editResultDiff is the diff an edit or write result shows: the fenced diff
// in its text, or a created file's content as added rows.
func editResultDiff(b *block) string {
	if diff, _ := extractDiff(strings.TrimRight(b.text, "\n")); diff != "" {
		return diff
	}
	if (b.toolName == "write" || b.toolName == "Write") && strings.HasPrefix(b.text, "Wrote ") {
		if a := parseEditArgs(b.toolArgs); a.Content != "" {
			return createdFileDiff(a.Content)
		}
	}
	return ""
}

// createdFileDiff is a new file's content as numbered added rows, cut at the
// same 200 rows the edit tool caps its own diffs at.
func createdFileDiff(content string) string {
	const maxRows = 200
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	var b strings.Builder
	for i, l := range lines {
		if i == maxRows {
			fmt.Fprintf(&b, "… +%d more lines\n", len(lines)-maxRows)
			break
		}
		fmt.Fprintf(&b, "%d + %s\n", i+1, l)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// renderEditBody draws an edit's diff and any notes the tool wrote after it
// (lenient-match notices, diagnostics). ok is false when there is no diff.
func renderEditBody(b *block, text string, width int) (string, bool) {
	diff, rest := extractDiff(text)
	if diff == "" {
		if diff = editResultDiff(b); diff == "" {
			return "", false
		}
		rest = text
	}
	path := parseEditArgs(b.toolArgs).Path
	if path == "" {
		if first, _, _ := strings.Cut(rest, "\n"); first != "" {
			path = diffPathRE.FindString(first)
		}
	}
	limit := diffPreviewRows
	if b.expanded {
		limit = 0
	}
	var out strings.Builder
	if b.run == nil || b.run.view == nil || !b.run.view.hasDiff {
		// No header carries the counts (a result drawn on its own): say them here.
		added, removed := diffCounts(diff)
		out.WriteString(textMutedStyle.Render("  "+glyphs.Result+" ") + diffCountsCell(added, removed) + textMutedStyle.Render("  "+diffSummary(added, removed)) + "\n")
	}
	out.WriteString(renderDiffFolded(diff, path, width, limit))
	if trail := strings.TrimSpace(strings.Join(strings.Split(rest, "\n")[1:], "\n")); trail != "" {
		for l := range strings.SplitSeq(trail, "\n") {
			out.WriteString("\n" + wrapHanging("    ", textMutedStyle.Render(l), width))
		}
	}
	return out.String(), true
}
