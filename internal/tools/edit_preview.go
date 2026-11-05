package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// EditPreview shows the change a gated file tool (write, edit, Write, Edit) is
// about to make, so a client can show it while the user decides. It is display
// only: the model never sees it.
type EditPreview struct {
	// Path is the file the tool will write.
	Path string
	// Diff lists the changed lines in the edit tool's result diff format:
	// "<line> <mark> <text>", mark one of ' ', '-', '+'. Empty when the change
	// is too large to compute a preview for (Truncated is then set) or when it
	// changes nothing.
	Diff string
	// NewFile marks a write that creates the file.
	NewFile bool
	// Truncated marks a Diff cut to the preview limits, or left out because the
	// file or the new content is too large.
	Truncated bool
}

const (
	// previewMaxInput bounds the file and new content a preview is computed from.
	previewMaxInput = 2 << 20
	// previewMaxBytes bounds the Diff text.
	previewMaxBytes = 32 << 10
)

// previewArgs are the arguments of the file tools a preview is computed from.
type previewArgs struct {
	Content    string `json:"content"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

// editPreviewFor is filePreview for raw tool arguments; nil for other tools.
func editPreviewFor(tool, path string, args json.RawMessage) *EditPreview {
	if !previewTool(tool) {
		return nil
	}
	var a previewArgs
	if json.Unmarshal(args, &a) != nil {
		return nil
	}
	return filePreview(tool, path, a)
}

func previewTool(tool string) bool {
	switch tool {
	case "write", "edit", "Write", "Edit":
		return true
	}
	return false
}

// filePreview computes the change tool will make to path (already resolved).
// It is nil when there is nothing to show: another tool, a binary file, or an
// edit that will fail (the tool reports why after the gate).
func filePreview(tool, path string, a previewArgs) *EditPreview {
	if path == "" || !previewTool(tool) {
		return nil
	}
	p := &EditPreview{Path: path}
	var old string
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if tool == "edit" || tool == "Edit" {
			return nil
		}
		p.NewFile = true
	case err != nil || !info.Mode().IsRegular():
		// Only regular files: reading a FIFO or device would block the prompt.
		return nil
	case info.Size() > previewMaxInput:
		p.Truncated = true
		return p
	default:
		data, err := os.ReadFile(path)
		if err != nil || isBinary(data) {
			return nil
		}
		old = string(data)
	}
	var next string
	switch tool {
	case "write", "Write":
		next = a.Content
	case "edit":
		m, err := matchEdit(old, a.OldString, a.NewString, a.ReplaceAll)
		if err != nil {
			return nil
		}
		next = m.apply(old)
	case "Edit":
		if a.OldString == "" {
			return nil
		}
		actual, replacement, _, err := liveagentEditMatch(old, a.OldString, a.NewString)
		if err != nil {
			return nil
		}
		m, err := matchEdit(old, actual, replacement, a.ReplaceAll)
		if err != nil {
			return nil
		}
		next = m.apply(old)
	}
	if len(next) > previewMaxInput {
		p.Truncated = true
		return p
	}
	if isBinary([]byte(next)) {
		return nil
	}
	var cut bool
	if p.NewFile {
		p.Diff, cut = addedLines(next)
	} else {
		p.Diff, cut = editDiffRows(old, next, 1)
	}
	if len(p.Diff) > previewMaxBytes {
		end := strings.LastIndexByte(p.Diff[:previewMaxBytes], '\n')
		if end < 0 {
			end = previewMaxBytes
		}
		p.Diff, cut = p.Diff[:end], true
	}
	p.Truncated = cut
	return p
}

// addedLines is the diff of a new file: every line added, in editDiff's format
// and limits.
func addedLines(content string) (string, bool) {
	if content == "" {
		return "", false
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	var b strings.Builder
	for i, line := range lines {
		if i == editDiffMaxLines {
			fmt.Fprintf(&b, "… +%d more lines", len(lines)-editDiffMaxLines)
			return b.String(), true
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d + %s", i+1, clipDiffLine(line))
	}
	return b.String(), false
}
