package tui

import (
	"path/filepath"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	glamouransi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Code is coloured from the theme's syn.* tokens, never from a stock chroma
// style, so code in replies and in diffs matches the rest of the palette and
// degrades with it: hand-picked 256/16-colour values, no colour in mono mode.

// synToken maps a chroma token type onto a syn.* token. ok is false for
// tokens that keep the default foreground (names, operators, punctuation).
func synToken(tt chroma.TokenType) (tokenID, bool) {
	switch {
	case tt.InCategory(chroma.Comment):
		return tokSynComment, true
	case tt == chroma.KeywordType, tt == chroma.NameClass, tt == chroma.NameBuiltinPseudo:
		return tokSynType, true
	case tt == chroma.KeywordConstant, tt == chroma.NameConstant:
		return tokSynNumber, true
	case tt.InCategory(chroma.Keyword):
		return tokSynKeyword, true
	case tt.InSubCategory(chroma.LiteralString):
		return tokSynString, true
	case tt.InSubCategory(chroma.LiteralNumber):
		return tokSynNumber, true
	case tt == chroma.NameFunction, tt == chroma.NameBuiltin, tt == chroma.NameFunctionMagic:
		return tokSynFunc, true
	}
	return 0, false
}

// synStyle is the style for one syn.* token over a base (which may carry a
// background band).
func synStyle(base lipgloss.Style, id tokenID) lipgloss.Style {
	s := withMono(base.Foreground(tokColor(id)), id)
	if id == tokSynComment {
		s = s.Italic(true)
	}
	return s
}

// codeLexer picks a lexer from a fence language or a file name; nil means the
// code is shown uncoloured rather than guessed at.
func codeLexer(lang, filename string) chroma.Lexer {
	var l chroma.Lexer
	if lang = strings.TrimSpace(lang); lang != "" {
		l = lexers.Get(lang)
	}
	if l == nil && filename != "" {
		l = lexers.Match(filepath.Base(filename))
	}
	if l == nil {
		return nil
	}
	return chroma.Coalesce(l)
}

// highlightLines colours code and returns it split into display lines. Every
// span is rendered over base so a band background runs unbroken under the
// tokens. Tabs become four spaces: a tab's width depends on the column it
// lands in, which would break the band and the wrap arithmetic.
func highlightLines(code string, lexer chroma.Lexer, base lipgloss.Style) []string {
	code = strings.ReplaceAll(code, "\t", "    ")
	if lexer == nil {
		lines := strings.Split(code, "\n")
		for i, l := range lines {
			if l != "" {
				lines[i] = base.Render(l)
			}
		}
		return lines
	}
	it, err := lexer.Tokenise(nil, code)
	if err != nil {
		return highlightLines(code, nil, base)
	}
	lines := []string{""}
	for t := it(); t != chroma.EOF; t = it() {
		st := base
		if id, ok := synToken(t.Type); ok {
			st = synStyle(base, id)
		}
		for i, part := range strings.Split(t.Value, "\n") {
			if i > 0 {
				lines = append(lines, "")
			}
			if part != "" {
				lines[len(lines)-1] += st.Render(part)
			}
		}
	}
	// Lexers may append a final newline; keep exactly the input's line count.
	want := strings.Count(code, "\n") + 1
	for len(lines) < want {
		lines = append(lines, "")
	}
	return lines[:want]
}

// glamourChroma feeds the syn.* tokens to glamour for the code blocks it still
// renders itself (fences nested in lists or quotes).
func glamourChroma() *glamouransi.Chroma {
	if !colorsActive() {
		return nil
	}
	c := func(id tokenID) glamouransi.StylePrimitive {
		hex := tokHex(id)
		p := glamouransi.StylePrimitive{Color: &hex}
		if id == tokSynComment {
			it := true
			p.Italic = &it
		}
		return p
	}
	return &glamouransi.Chroma{
		Comment:             c(tokSynComment),
		CommentPreproc:      c(tokSynComment),
		Keyword:             c(tokSynKeyword),
		KeywordReserved:     c(tokSynKeyword),
		KeywordNamespace:    c(tokSynKeyword),
		KeywordType:         c(tokSynType),
		NameBuiltin:         c(tokSynFunc),
		NameClass:           c(tokSynType),
		NameConstant:        c(tokSynNumber),
		NameFunction:        c(tokSynFunc),
		LiteralNumber:       c(tokSynNumber),
		LiteralString:       c(tokSynString),
		LiteralStringEscape: c(tokSynNumber),
		GenericDeleted:      c(tokStateError),
		GenericInserted:     c(tokStateOK),
	}
}

// chromaFormatter is the chroma output format for the active profile.
func chromaFormatter() string {
	if !backgroundsActive() && colorsActive() {
		return "terminal16"
	}
	if lipgloss.ColorProfile() == termenv.TrueColor {
		return "terminal16m"
	}
	return "terminal256"
}
