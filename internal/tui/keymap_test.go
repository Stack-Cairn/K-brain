package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Stack-Cairn/K-brain/internal/i18n"
)

// legacyHelpKeys is /help's key section as it was written out by hand before
// the keymap existed. Generating it from the keymap must not change a byte,
// in any language.
var legacyHelpKeys = []string{
	"Tab — complete",
	"Shift+Tab — cycle Normal / Plan / Auto review / Always allow",
	"Ctrl+K — clear the conversation",
	"Ctrl+T — subagents dock: ↑/↓ select, Space preview, Enter open, Ctrl+X stop",
	"in a subagent view: Enter steers or follows up, Ctrl+X stops, PgUp/PgDn scroll, Esc returns",
	palHintThinking + " — toggle thinking tokens",
	"Ctrl+E — expand the last tool result",
	"Ctrl+J / Shift+Enter — newline",
	"Ctrl+G — edit the current draft in an external editor",
	"Ctrl+V — paste image",
	"Esc — interrupt the agent",
	"Esc Esc (idle) — " + palDescRewind + " (↑/↓ browse, Enter rewinds, f forks)",
	"while busy with queued messages: ↑/↓ select, Del removes",
	"PgUp/PgDn — scroll · wheel — scroll · drag — select/copy text",
	palHintQuit + " — quit",
	"Ctrl+Z / Alt+Z — undo / redo the draft",
	"Ctrl+W / Ctrl+U / Alt+K / Alt+D — cut word, to line start, to line end, next word · Ctrl+Y paste it back (Alt+Y older)",
	"Alt+M — multiline mode: Enter adds a line, Ctrl+J sends",
	"Ctrl+R — search prompt history (Tab: all directories)",
	"Ctrl+S — stash the draft, or restore it on an empty composer",
	"with a queued message selected: Enter sends it now, Ctrl+E edits, Alt+↑/↓ moves",
	"Alt+↑ — focus the transcript: j/k select a block, h/l fold / open, e toggle, E fold all, v view, / find, Esc back",
	"in the block viewer: j/k scroll, Space/PgDn page, g/G top / end, / find, n/N next / previous, Esc back",
}

func TestKeymapHelpUnchanged(t *testing.T) {
	for _, language := range []string{i18n.English, i18n.Chinese, i18n.TraditionalChinese} {
		tr := func(s string) string { return i18n.Text(language, s) }
		parts := make([]string, len(legacyHelpKeys))
		for i, h := range legacyHelpKeys {
			parts[i] = tr(h)
		}
		if got, want := keymapHelp(tr), strings.Join(parts, " · "); got != want {
			t.Errorf("%s: /help keys changed\n got: %s\nwant: %s", language, got, want)
		}
		if !strings.HasSuffix(helpTextFor(language), keymapHelp(tr)) {
			t.Errorf("%s: /help does not end with the keymap section", language)
		}
	}
}

func TestKeymapRowsWellFormed(t *testing.T) {
	known := []keyContext{keyGlobal, keyComposer, keyBusy, keyIdle, keyQueue, keyMenu, keyDock, keyTaskView, keyTranscript, keyViewer}
	seen := map[string]bool{}
	for _, b := range keymap {
		if b.key == "" || b.action == "" {
			t.Errorf("row %+v needs a key and an action", b)
		}
		if !slices.Contains(known, b.context) {
			t.Errorf("row %+v has an unknown context", b)
		}
		id := b.key + "@" + string(b.context)
		if seen[id] {
			t.Errorf("%s is listed twice", id)
		}
		seen[id] = true
	}
}

// keyDispatchers are the functions that implement each context's keys.
var keyDispatchers = map[keyContext][]string{
	keyTaskView:   {"taskViewKey"},
	keyTranscript: {"transcriptFocusKey", "findTypingKey"},
	keyViewer:     {"blockViewerKey"},
}

func init() {
	main := []string{"key", "transcriptKey", "composerPreKey", "composerPasteKey", "composerKey"}
	for _, c := range []keyContext{keyGlobal, keyComposer, keyBusy, keyIdle, keyQueue, keyMenu, keyDock} {
		keyDispatchers[c] = main
	}
}

// teaKeys maps the bubbletea key constants dispatch may switch on to their
// values. A constant missing here fails the test, so a new key cannot slip
// into dispatch without a keymap row.
var teaKeys = map[string]tea.KeyType{
	"KeyEsc": tea.KeyEsc, "KeyEnter": tea.KeyEnter, "KeyTab": tea.KeyTab, "KeyShiftTab": tea.KeyShiftTab,
	"KeySpace": tea.KeySpace, "KeyBackspace": tea.KeyBackspace, "KeyDelete": tea.KeyDelete,
	"KeyUp": tea.KeyUp, "KeyDown": tea.KeyDown, "KeyLeft": tea.KeyLeft, "KeyRight": tea.KeyRight,
	"KeyPgUp": tea.KeyPgUp, "KeyPgDown": tea.KeyPgDown, "KeyHome": tea.KeyHome, "KeyEnd": tea.KeyEnd,
	"KeyCtrlHome": tea.KeyCtrlHome, "KeyCtrlEnd": tea.KeyCtrlEnd,
	"KeyCtrlA": tea.KeyCtrlA, "KeyCtrlB": tea.KeyCtrlB, "KeyCtrlC": tea.KeyCtrlC, "KeyCtrlD": tea.KeyCtrlD,
	"KeyCtrlE": tea.KeyCtrlE, "KeyCtrlF": tea.KeyCtrlF, "KeyCtrlG": tea.KeyCtrlG, "KeyCtrlH": tea.KeyCtrlH,
	"KeyCtrlJ": tea.KeyCtrlJ, "KeyCtrlK": tea.KeyCtrlK, "KeyCtrlL": tea.KeyCtrlL, "KeyCtrlN": tea.KeyCtrlN,
	"KeyCtrlO": tea.KeyCtrlO, "KeyCtrlP": tea.KeyCtrlP, "KeyCtrlQ": tea.KeyCtrlQ, "KeyCtrlR": tea.KeyCtrlR,
	"KeyCtrlS": tea.KeyCtrlS, "KeyCtrlT": tea.KeyCtrlT, "KeyCtrlU": tea.KeyCtrlU, "KeyCtrlV": tea.KeyCtrlV,
	"KeyCtrlW": tea.KeyCtrlW, "KeyCtrlX": tea.KeyCtrlX, "KeyCtrlY": tea.KeyCtrlY, "KeyCtrlZ": tea.KeyCtrlZ,
}

// notKeys are tea identifiers that show up in dispatch without naming a key
// binding: types, and plain typing.
var notKeys = map[string]bool{"KeyMsg": true, "KeyType": true, "KeyRunes": true}

func keyName(k tea.KeyType) string {
	if k == tea.KeySpace {
		return "space"
	}
	return tea.KeyMsg{Type: k}.String()
}

// dispatchedKeys reads the named functions of this package and returns every
// key they compare against: tea key constants, key-name strings compared with
// msg.String(), "alt+<key>" where a constant is tested together with msg.Alt,
// and shift+enter through isShiftEnterSeq.
func dispatchedKeys(t *testing.T, funcs []string) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	found := map[string]bool{}
	out := map[string]bool{}
	teaKey := func(e ast.Expr) (string, bool) {
		sel, ok := e.(*ast.SelectorExpr)
		if !ok {
			return "", false
		}
		if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "tea" || !strings.HasPrefix(sel.Sel.Name, "Key") || notKeys[sel.Sel.Name] {
			return "", false
		}
		k, ok := teaKeys[sel.Sel.Name]
		if !ok {
			t.Errorf("dispatch uses tea.%s: add it to teaKeys and give it a keymap row", sel.Sel.Name)
			return "", false
		}
		return keyName(k), true
	}
	isMsgAlt := func(e ast.Expr) bool {
		sel, ok := e.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "Alt"
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || !slices.Contains(funcs, fd.Name.Name) {
				continue
			}
			found[fd.Name.Name] = true
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.SelectorExpr:
					if k, ok := teaKey(n); ok {
						out[k] = true
					}
				case *ast.CallExpr:
					if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "isShiftEnterSeq" {
						out["shift+enter"] = true
					}
				case *ast.BinaryExpr:
					switch n.Op {
					case token.EQL:
						for _, pair := range [][2]ast.Expr{{n.X, n.Y}, {n.Y, n.X}} {
							lit, ok := pair[0].(*ast.BasicLit)
							if !ok || lit.Kind != token.STRING {
								continue
							}
							named := false
							switch o := pair[1].(type) {
							case *ast.Ident:
								named = o.Name == "k"
							case *ast.CallExpr:
								sel, ok := o.Fun.(*ast.SelectorExpr)
								named = ok && sel.Sel.Name == "String"
							}
							if s, err := strconv.Unquote(lit.Value); err == nil && named {
								out[s] = true
							}
						}
					case token.LAND:
						for _, pair := range [][2]ast.Expr{{n.X, n.Y}, {n.Y, n.X}} {
							cmp, ok := pair[0].(*ast.BinaryExpr)
							if !ok || cmp.Op != token.EQL || !isMsgAlt(pair[1]) {
								continue
							}
							for _, side := range []ast.Expr{cmp.X, cmp.Y} {
								if k, ok := teaKey(side); ok {
									out["alt+"+k] = true
								}
							}
						}
					}
				}
				return true
			})
		}
	}
	for _, fn := range funcs {
		if !found[fn] {
			t.Fatalf("dispatch function %s not found", fn)
		}
	}
	return out
}

// Every keymap row is a key its context's dispatch really handles.
func TestKeymapRowsAreDispatched(t *testing.T) {
	cache := map[keyContext]map[string]bool{}
	for _, b := range keymap {
		keys, ok := cache[b.context]
		if !ok {
			keys = dispatchedKeys(t, keyDispatchers[b.context])
			cache[b.context] = keys
		}
		if !keys[b.key] {
			t.Errorf("keymap lists %q in %s (%s), but %v never handles it", b.key, b.context, b.action, keyDispatchers[b.context])
		}
	}
}

// Every key dispatch handles has at least one keymap row.
func TestDispatchedKeysAreInKeymap(t *testing.T) {
	byFuncs := map[string][]keyContext{}
	for c, funcs := range keyDispatchers {
		id := strings.Join(funcs, ",")
		byFuncs[id] = append(byFuncs[id], c)
	}
	for id, contexts := range byFuncs {
		for k := range dispatchedKeys(t, strings.Split(id, ",")) {
			listed := slices.ContainsFunc(keymap, func(b keyBinding) bool {
				return b.key == k && slices.Contains(contexts, b.context)
			})
			if !listed {
				t.Errorf("%s handles %q, which has no keymap row in %v", id, k, contexts)
			}
		}
	}
}
