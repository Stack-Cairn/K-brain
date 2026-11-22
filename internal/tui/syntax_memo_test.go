package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// matchLexer must pick exactly the lexer lexers.Match picks. The names
// tried are every registered file pattern made concrete, a sample of them
// again with each backup suffix, and common file names. (lexers.Match takes
// about a millisecond a call, which bounds the sample.)
func TestMatchLexerAgreesWithChroma(t *testing.T) {
	class := regexp.MustCompile(`\[([^\]])[^\]]*\]`)
	concrete := func(glob string) string {
		s := class.ReplaceAllString(glob, "$1")
		s = strings.ReplaceAll(s, "*", "main")
		s = strings.ReplaceAll(s, "?", "x")
		return strings.ReplaceAll(s, `\`, "")
	}
	names := []string{
		"main.go", "handler.go", "Makefile", "Dockerfile", "CMakeLists.txt", "README.md",
		"x.ts", "x.tsx", "x.py", "x.rs", "x.h", "x.hpp", "x.c", "x.cc", "x.json", "x.yaml",
		"x.yml", ".bashrc", "go.mod", "go.sum", "x.unknownext", "noext", "x.go~", "x.go.bak",
		"x.sh.in", "Makefile.in", "x", "", ".", "x.", "build.gradle.kts", "x.d.ts",
	}
	seen := map[string]bool{}
	k := 0
	for _, l := range lexers.GlobalLexerRegistry.Lexers {
		cfg := l.Config()
		for _, glob := range append(append([]string(nil), cfg.Filenames...), cfg.AliasFilenames...) {
			n := concrete(glob)
			names = append(names, n)
			if k++; k%40 == 0 {
				for _, suf := range backupSuffixes {
					names = append(names, n+suf)
				}
			}
		}
	}
	name := func(l chroma.Lexer) string {
		if l == nil {
			return "<nil>"
		}
		return l.Config().Name
	}
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		if got, want := matchLexer(n), lexers.Match(n); got != want {
			t.Errorf("matchLexer(%q) = %s, lexers.Match = %s", n, name(got), name(want))
		}
	}
	if len(seen) < 500 {
		t.Fatalf("only %d names tried", len(seen))
	}
}

// The token memo hands back the same tokens lexLines computes.
func TestLexLinesMemoMatchesLexLines(t *testing.T) {
	resetSyntaxMemos()
	lx := codeLexer("go", "")
	code := "func f(x int) int {\n\t// double\n\treturn x * 2\n}"
	want := lexLines(code, lx, 4)
	for range 2 {
		got := lexLinesMemo(code, lx, 4)
		if len(got) != len(want) {
			t.Fatalf("lines: got %d, want %d", len(got), len(want))
		}
		for i := range want {
			if len(got[i]) != len(want[i]) {
				t.Fatalf("line %d: got %v, want %v", i, got[i], want[i])
			}
			for j := range want[i] {
				if got[i][j] != want[i][j] {
					t.Fatalf("line %d token %d: got %v, want %v", i, j, got[i][j], want[i][j])
				}
			}
		}
	}
	if codeLexer("go", "") != lx || codeLexer("", "a/b/main.go") != codeLexer("", "main.go") {
		t.Fatal("codeLexer must hand out one lexer per language and file name")
	}
}
