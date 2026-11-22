package tui

// Memos for the syntax work a block render repeats.
//
// A block's rows are cached per width, so a resize, a theme change or a
// resumed session draws every block again. Picking a lexer from a file name
// (a glob match against every registered lexer) and tokenising the code are
// the bulk of that work, and neither depends on the width or the theme: the
// lexer is a function of the fence language and the file name, the tokens a
// function of the lexer and the code. Both are memoised here, so a redraw
// pays only for laying the tokens out.

import (
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

type lexerKey struct{ lang, base string }

// lexerMemo maps (fence language, file base name) to the lexer codeLexer
// picks for it, nil included.
var lexerMemo sync.Map // lexerKey -> chroma.Lexer (possibly nil)

// lexTokKey is one tokenisation: the lexer, and the code cut into n lines.
// codeLexer hands out one lexer value per language, so the lexer itself is
// the key.
type lexTokKey struct {
	lexer chroma.Lexer
	code  string
	n     int
}

// lexTokBudget bounds the code one generation of the token memo keeps, in
// bytes. When the young generation fills it becomes the old one and the
// oldest is dropped; a hit in the old generation moves back to the young
// one. So whatever was drawn within the last budget's worth of code stays,
// and the memory a long session holds stays flat.
const lexTokBudget = 8 << 20

// matchLexer is lexers.Match(name) for a base name, without its cost: Match
// tries every file pattern of every registered lexer, and each again with
// every backup suffix, through filepath.Match — about a millisecond a call.
// Here the patterns are sorted once into plain names, "*.ext" suffixes and
// the rest, and a pattern with a backup suffix is only tried on a name that
// ends in that suffix (the suffix is literal, so no other name can match).
// The result is the same lexer, picked by the same priority rule.
func matchLexer(name string) chroma.Lexer {
	ix := lexerGlobs()
	if l := ix.pick(ix.primary, name); l != nil {
		return l
	}
	return ix.pick(ix.alias, name)
}

// lexerGlob is one file pattern of one lexer.
type lexerGlob struct {
	lexer chroma.Lexer
	glob  string
	// lit is the pattern itself when it has no wildcards, ext the part
	// after a leading "*" when nothing else in it is one.
	lit, ext string
}

type lexerGlobIndex struct {
	primary, alias []lexerGlob
}

// backupSuffixes are the suffixes chroma's Match also accepts after a
// pattern (its ignoredSuffixes).
var backupSuffixes = [...]string{
	"~", ".bak", ".old", ".orig",
	".dpkg-dist", ".dpkg-old", ".ucf-dist", ".ucf-new", ".ucf-old",
	".rpmnew", ".rpmorig", ".rpmsave",
	".in",
}

var lexerGlobsOnce = sync.OnceValue(func() *lexerGlobIndex {
	ix := &lexerGlobIndex{}
	compile := func(l chroma.Lexer, glob string) lexerGlob {
		g := lexerGlob{lexer: l, glob: glob}
		switch {
		case !strings.ContainsAny(glob, `*?[\`):
			g.lit = glob
		case strings.HasPrefix(glob, "*") && !strings.ContainsAny(glob[1:], `*?[\`):
			g.ext = glob[1:]
		}
		return g
	}
	for _, l := range lexers.GlobalLexerRegistry.Lexers {
		cfg := l.Config()
		for _, glob := range cfg.Filenames {
			ix.primary = append(ix.primary, compile(l, glob))
		}
		for _, glob := range cfg.AliasFilenames {
			ix.alias = append(ix.alias, compile(l, glob))
		}
	}
	return ix
})

func lexerGlobs() *lexerGlobIndex { return lexerGlobsOnce() }

// matches is filepath.Match(g.glob, name), errors counting as no match.
func (g *lexerGlob) matches(name string) bool {
	switch {
	case g.lit != "":
		return name == g.lit
	case g.ext != "":
		return strings.HasSuffix(name, g.ext)
	}
	ok, _ := filepath.Match(g.glob, name)
	return ok
}

// pick runs one pass of chroma's Match over globs.
func (ix *lexerGlobIndex) pick(globs []lexerGlob, name string) chroma.Lexer {
	var sufs []string
	for _, suf := range backupSuffixes {
		if strings.HasSuffix(name, suf) {
			sufs = append(sufs, suf)
		}
	}
	var matched chroma.PrioritisedLexers
	for i := range globs {
		g := &globs[i]
		if g.matches(name) {
			matched = append(matched, g.lexer)
			continue
		}
		for _, suf := range sufs {
			if ok, _ := filepath.Match(g.glob+suf, name); ok {
				matched = append(matched, g.lexer)
				break
			}
		}
	}
	if len(matched) == 0 {
		return nil
	}
	sort.Sort(matched)
	return matched[0]
}

// tokMemo is a two-generation memo of tokenisations.
type tokMemo struct {
	mu         sync.Mutex
	young, old map[lexTokKey][][]lexTok
	bytes      int
}

var lexTokMemo tokMemo

// lexLinesMemo is lexLines, remembered. The result is shared: callers must
// not change it.
func lexLinesMemo(code string, lexer chroma.Lexer, n int) [][]lexTok {
	if lexer == nil {
		return lexLines(code, nil, n)
	}
	if reflect.TypeOf(lexer).Kind() != reflect.Pointer || len(code) > lexTokBudget/4 {
		// Only pointer lexers are safe map keys; huge code is not kept.
		return lexLines(code, lexer, n)
	}
	k := lexTokKey{lexer: lexer, code: code, n: n}
	if toks, ok := lexTokMemo.get(k); ok {
		return toks
	}
	toks := lexLines(code, lexer, n)
	lexTokMemo.mu.Lock()
	lexTokMemo.putLocked(k, toks)
	lexTokMemo.mu.Unlock()
	return toks
}

func (t *tokMemo) get(k lexTokKey) ([][]lexTok, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if toks, ok := t.young[k]; ok {
		return toks, true
	}
	toks, ok := t.old[k]
	if ok {
		t.putLocked(k, toks)
	}
	return toks, ok
}

func (t *tokMemo) putLocked(k lexTokKey, toks [][]lexTok) {
	if t.young == nil || t.bytes+len(k.code) > lexTokBudget {
		t.old, t.young, t.bytes = t.young, map[lexTokKey][][]lexTok{}, 0
	}
	if _, ok := t.young[k]; !ok {
		t.young[k] = toks
		t.bytes += len(k.code)
	}
}

// resetSyntaxMemos empties both memos; benchmarks use it to measure a cold
// start.
func resetSyntaxMemos() {
	lexerMemo.Clear()
	lexTokMemo.mu.Lock()
	lexTokMemo.young, lexTokMemo.old, lexTokMemo.bytes = nil, nil, 0
	lexTokMemo.mu.Unlock()
}
