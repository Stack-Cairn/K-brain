package privacy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"
)

// redactTextBefore is RedactText as of 52a0ef2, before it became a wrapper of RedactTextFunc.
func redactTextBefore(text string) (string, bool) {
	changed := false
	for _, rule := range rules {
		text = rule.rx.ReplaceAllStringFunc(text, func(match string) string {
			value := match
			if rule.valueGroup > 0 {
				groups := rule.rx.FindStringSubmatch(match)
				if len(groups) <= rule.valueGroup {
					return match
				}
				value = groups[rule.valueGroup]
			}
			if rule.name != "SECRET" && rule.check != nil && !rule.check(value) {
				return match
			}
			if rule.locate != nil {
				start, end, ok := rule.locate(match)
				if !ok {
					return match
				}
				changed = true
				return match[:start] + "[REDACTED_" + rule.name + "]" + match[end:]
			}
			changed = true
			return "[REDACTED_" + rule.name + "]"
		})
	}
	return text, changed
}

// Differential over every string literal in the privacy and approval test corpora, plus
// pairwise joins so rule interactions (tokens, adjacent values) are exercised too.
func TestRedactTextMatchesPreviousImplementation(t *testing.T) {
	var corpus []string
	for _, glob := range []string{"*_test.go", "../approval/*_test.go", "../backend/ai_approval*_test.go"} {
		files, _ := filepath.Glob(glob)
		for _, name := range files {
			f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil && s != "" {
						corpus = append(corpus, s)
					}
				}
				return true
			})
		}
	}
	if len(corpus) < 200 {
		t.Fatalf("corpus too small: %d", len(corpus))
	}
	check := func(s string) {
		wantText, wantChanged := redactTextBefore(s)
		gotText, gotChanged := RedactText(s)
		if gotText != wantText || gotChanged != wantChanged {
			t.Fatalf("RedactText drift on %q:\n got %q %v\nwant %q %v", s, gotText, gotChanged, wantText, wantChanged)
		}
	}
	for _, s := range corpus {
		check(s)
	}
	for i := 0; i+1 < len(corpus); i++ {
		for _, sep := range []string{"", " ", ":", "\n"} {
			check(corpus[i] + sep + corpus[i+1])
		}
	}
	t.Logf("compared %d literals and %d joins", len(corpus), 4*(len(corpus)-1))
}
