package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTemplateExpansion(t *testing.T) {
	for _, tc := range []struct{ body, args, want string }{
		{"$1 / $2 / $3", `Button "click handler"`, "Button / click handler / "},
		{"$@ | $ARGUMENTS", `one 'two words'`, "one two words | one two words"},
		{"${1:-seven} ${2:-fallback}", `"" value`, "seven value"},
		{"${@:-all} / ${ARGUMENTS:-nothing}", "", "all / nothing"},
		{"${@:2} | ${@:2:1} | ${@:8}", "one two three", "two three | two | "},
		{"${@:1:0}", "one", ""},
		{"$10 $1", "1 2 3 4 5 6 7 8 9 10", "10 1"},
		{"$1 / $2", `D:\Node\pi "D:\My Files\file.go"`, `D:\Node\pi / D:\My Files\file.go`},
		{"$1", `'$2'`, "$2"},
		{"$HOME ${PATH} $ARGUMENTS_SUFFIX", "ignored", "$HOME ${PATH} $ARGUMENTS_SUFFIX"},
		{"${@:999999999999999999999999}", "one", ""},
	} {
		got, err := (Template{Body: tc.body}).Expand(tc.args)
		if err != nil || got != tc.want {
			t.Errorf("%q + %q = %q, %v; want %q", tc.body, tc.args, got, err, tc.want)
		}
	}
	if _, err := (Template{Body: "$1"}).Expand(`"unclosed`); err == nil {
		t.Fatal("expected quote error")
	}
}

func TestTemplateFrontmatter(t *testing.T) {
	tpl, err := Parse("review", "\ufeff---\r\ndescription: \"Review changes\"\r\nargument-hint: '<file>'\r\n---\r\n\r\nReview $1\r\n")
	if err != nil || tpl.Description != "Review changes" || tpl.ArgumentHint != "<file>" || tpl.Body != "Review $1" {
		t.Fatalf("%+v %v", tpl, err)
	}
	tpl, err = Parse("plain", "\nFirst line\nSecond line")
	if err != nil || tpl.Description != "First line" {
		t.Fatalf("%+v %v", tpl, err)
	}
	for _, text := range []string{"", "---\ndescription: broken", "---\n---\n"} {
		if _, err := Parse("bad", text); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
}

func TestTemplateDiscovery(t *testing.T) {
	global, project := t.TempDir(), t.TempDir()
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(global, "review.md", "global")
	write(project, "review.md", "project")
	write(global, "build.md", "build")
	write(global, "bad name.md", "ignore")
	write(global, "bad.md", "---\nunclosed")
	write(global, "large.md", strings.Repeat("a", maxSize+1))
	if err := os.Mkdir(filepath.Join(global, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(global, "nested"), "hidden.md", "hidden")
	got, errs := Load(filepath.Join(global, "missing"), global, project)
	if len(got) != 2 || got[0].Name != "build" || got[1].Body != "project" || len(errs) != 2 {
		t.Fatalf("%+v errors=%v", got, errs)
	}
}
