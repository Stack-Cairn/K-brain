package tui

import (
	"fmt"
	"strings"
)

// A long prompt stays short in the transcript. One sent with paste chips
// shows as it was typed, "summarize this [Pasted 2000 lines]", and opens to
// the whole text; any other prompt over userFoldAt lines shows its first
// userPreviewLines and a count of the rest. A click, or e / Enter in the
// transcript focus, opens and folds it. The model always gets the whole
// text: only the drawing is short.

const (
	userFoldAt       = 40
	userPreviewLines = 8
)

func init() {
	registerBlockKind(blockUser, blockKindSpec{
		render:   renderUserBlock,
		toggle:   toggleExpanded,
		foldable: userFoldable,
	})
}

// userFoldable reports whether the prompt b has a short form.
func userFoldable(b *block) bool {
	return b.typed != "" || strings.Count(b.text, "\n")+1 > userFoldAt
}

func renderUserBlock(b *block, width int) string {
	if b.expanded || !userFoldable(b) {
		return renderUserBand(b.text, width)
	}
	if b.typed != "" {
		return renderUserBand(b.typed, width)
	}
	lines := strings.Split(b.text, "\n")
	rest := len(lines) - userPreviewLines
	short := strings.Join(lines[:userPreviewLines], "\n")
	return renderUserBand(short+"\n"+chromeGlyphs(fmt.Sprintf("%s +%d lines", glyphs.Fold, rest)), width)
}

// noteTypedPrompt remembers how the composer showed a prompt whose paste
// chips were expanded for sending, so its transcript row can show it that
// way (typedPrompt).
func (m *model) noteTypedPrompt(raw, sent string) {
	typed := strings.TrimSpace(strings.ReplaceAll(raw, chipSentinel, ""))
	if typed == sent || !pasteChipRe.MatchString(raw) {
		return
	}
	c := m.composer()
	if c.typed == nil {
		c.typed = map[string]string{}
	}
	c.typed[sent] = typed
}

// typedPrompt is, once, the composer's form of the prompt sent as text.
func (m *model) typedPrompt(text string) string {
	if m.cmp == nil {
		return ""
	}
	typed, ok := m.cmp.typed[text]
	if ok {
		delete(m.cmp.typed, text)
	}
	return typed
}
