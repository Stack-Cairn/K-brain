package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func coreFixture(t *testing.T, content string) (context.Context, []Tool, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.go")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return WithWorkingDir(context.Background(), dir), All(), path
}

func runCore(ctx context.Context, ts []Tool, name string, args any) Result {
	raw, _ := json.Marshal(args)
	return ExecuteResult(ctx, ts, name, raw, false)
}

func fileText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestEditFallsBackToWhitespaceTolerantMatching(t *testing.T) {
	ctx, ts, path := coreFixture(t, "func f() {\n\t\tif x {\n\t\t\treturn 1\n\t\t}\n}\n")
	res := runCore(ctx, ts, "edit", map[string]any{"path": "f.go", "old_string": "if x {\n\treturn 1\n}", "new_string": "if x {\n\treturn 2\n}"})
	if res.Failed {
		t.Fatal(res.Text)
	}
	if got := fileText(t, path); got != "func f() {\n\t\tif x {\n\t\t\treturn 2\n\t\t}\n}\n" {
		t.Errorf("indentation-tolerant edit wrote %q", got)
	}
	if !strings.Contains(res.Text, "matched ignoring indentation") {
		t.Errorf("result should name the fallback: %s", res.Text)
	}

	ctx, ts, path = coreFixture(t, "a := 1   \nb := 2\n")
	if res := runCore(ctx, ts, "edit", map[string]any{"path": "f.go", "old_string": "a := 1\nb := 2", "new_string": "a := 3\nb := 4"}); res.Failed {
		t.Fatal(res.Text)
	}
	if got := fileText(t, path); got != "a := 3\nb := 4\n" {
		t.Errorf("trailing-whitespace edit wrote %q", got)
	}
}

func TestEditFoldsLookalikeCharacters(t *testing.T) {
	for _, tc := range []struct{ name, file, old, want string }{
		{"smart quotes in file", "msg := “don’t panic” — ok\nnext\n", `msg := "don't panic" - ok`, "fixed\nnext\n"},
		{"smart quotes in old_string", "say(\"it's\")\n", "say(“it’s”)", "fixed\n"},
		{"non-breaking space", "total: 100 – 200\n", "total: 100 - 200", "fixed\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, ts, path := coreFixture(t, tc.file)
			res := runCore(ctx, ts, "edit", map[string]any{"path": "f.go", "old_string": tc.old, "new_string": "fixed"})
			if res.Failed {
				t.Fatal(res.Text)
			}
			if got := fileText(t, path); got != tc.want {
				t.Errorf("wrote %q, want %q", got, tc.want)
			}
			if !strings.Contains(res.Text, "smart quotes, dashes and non-breaking spaces") {
				t.Errorf("result should name the look-alike fallback: %s", res.Text)
			}
		})
	}
}

func TestEditRefusesAmbiguousFallbackMatches(t *testing.T) {
	ctx, ts, path := coreFixture(t, "x := “a”\ny := 1\nx := “a”\n")
	res := runCore(ctx, ts, "edit", map[string]any{"path": "f.go", "old_string": `x := "a"`, "new_string": "z"})
	if !res.Failed || !strings.Contains(res.Text, "appears 2 times (first at line 1)") {
		t.Fatalf("ambiguous look-alike match should be refused: %s", res.Text)
	}
	if res := runCore(ctx, ts, "edit", map[string]any{"path": "f.go", "old_string": `x := "a"`, "new_string": "z", "replace_all": true}); res.Failed {
		t.Fatal(res.Text)
	}
	if got := fileText(t, path); got != "z\ny := 1\nz\n" {
		t.Errorf("replace_all look-alike edit wrote %q", got)
	}

	ctx, ts, _ = coreFixture(t, "\tif a {\n\t    b()\n\t}\n  if a {\n      b()\n  }\n")
	res = runCore(ctx, ts, "edit", map[string]any{"path": "f.go", "old_string": "if a {\n    b()\n}", "new_string": "c()"})
	if !res.Failed || !strings.Contains(res.Text, "matches several places once whitespace is ignored") {
		t.Errorf("differently indented matches should be refused: %s", res.Text)
	}
}

func TestEditNotFoundNamesTheNearestLine(t *testing.T) {
	ctx, ts, _ := coreFixture(t, "package main\n\nfunc handleRequest(w http.ResponseWriter, r *http.Request) {\n\tlog.Println(r.URL)\n}\n")
	res := runCore(ctx, ts, "edit", map[string]any{"path": "f.go", "old_string": "func handleRequests(w http.ResponseWriter, req *http.Request) {", "new_string": "x"})
	if !res.Failed || !strings.Contains(res.Text, "old_string not found") || !strings.Contains(res.Text, "the closest line is line 3:") || !strings.Contains(res.Text, `file:       "func handleRequest(w http.ResponseWriter, r *http.Request) {"`) {
		t.Errorf("not-found error should quote the closest line:\n%s", res.Text)
	}

	// The first line matches but a later one does not: name the diverging line.
	res = runCore(ctx, ts, "edit", map[string]any{"path": "f.go", "old_string": "func handleRequest(w http.ResponseWriter, r *http.Request) {\n\tlog.Printf(r.URL)\n}", "new_string": "x"})
	if !res.Failed || !strings.Contains(res.Text, "old_string line 1 matches line 3 of the file, but old_string line 2 differs from line 4") || !strings.Contains(res.Text, `"\tlog.Println(r.URL)"`) {
		t.Errorf("not-found error should name the diverging line:\n%s", res.Text)
	}

	res = runCore(ctx, ts, "edit", map[string]any{"path": "f.go", "old_string": "SELECT * FROM users", "new_string": "x"})
	if !res.Failed || !strings.Contains(res.Text, "no line in the file resembles") {
		t.Errorf("unrelated old_string should say nothing resembles it:\n%s", res.Text)
	}

	res = runCore(ctx, ts, "edit", map[string]any{"path": "f.go", "old_string": "", "new_string": "x"})
	if !res.Failed || !strings.Contains(res.Text, "old_string is empty") {
		t.Errorf("empty old_string: %s", res.Text)
	}
}

func TestWriteRefusesToClobberChangesMadeSinceRead(t *testing.T) {
	ctx, ts, path := coreFixture(t, "v1\n")
	if res := runCore(ctx, ts, "read", map[string]any{"path": "f.go"}); res.Failed {
		t.Fatal(res.Text)
	}
	if err := os.WriteFile(path, []byte("v2 from the user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := runCore(ctx, ts, "write", map[string]any{"path": "f.go", "content": "agent\n"})
	if !res.Failed || !strings.Contains(res.Text, "changed on disk since you last read it") {
		t.Fatalf("write over a changed file should be refused: %s", res.Text)
	}
	if got := fileText(t, path); got != "v2 from the user\n" {
		t.Fatalf("refused write still changed the file: %q", got)
	}
	// An edit's not-found error says why; a matching edit still applies, with a note.
	if res := runCore(ctx, ts, "edit", map[string]any{"path": "f.go", "old_string": "v1", "new_string": "x"}); !res.Failed || !strings.Contains(res.Text, "changed on disk since you last read it; read it again") {
		t.Errorf("stale edit should say the file changed: %s", res.Text)
	}
	res = runCore(ctx, ts, "edit", map[string]any{"path": "f.go", "old_string": "from the user", "new_string": "edited"})
	if res.Failed || !strings.Contains(res.Text, "had changed on disk since you last read it") {
		t.Errorf("matching stale edit should apply with a note: %s", res.Text)
	}
	// The tools' own writes keep the ledger current: no refusal after edit or write.
	for _, content := range []string{"one\n", "two\n"} {
		if res := runCore(ctx, ts, "write", map[string]any{"path": "f.go", "content": content}); res.Failed {
			t.Fatalf("write after the agent's own change: %s", res.Text)
		}
	}
	// Files never read can be written as before.
	if res := runCore(ctx, ts, "write", map[string]any{"path": "new.txt", "content": "x"}); res.Failed {
		t.Fatal(res.Text)
	}
}

func TestCoreToolsDecodeArgumentsLeniently(t *testing.T) {
	ctx, ts, path := coreFixture(t, "a\nb\nc\nd\n")
	read := ExecuteResult(ctx, ts, "read", json.RawMessage(`{"path":"f.go","offset":"2","limit":2.0}`), false)
	if read.Failed || read.Text != "2\tb\n3\tc\n" {
		t.Errorf("numeric strings and whole floats should decode: %q", read.Text)
	}
	edit := ExecuteResult(ctx, ts, "edit", json.RawMessage(`{"path":"f.go","old_string":"b","new_string":"B","replace_all":"true"}`), false)
	if edit.Failed || fileText(t, path) != "a\nB\nc\nd\n" {
		t.Errorf(`"true" should decode as a boolean: %s`, edit.Text)
	}
	// Arguments sent as a JSON-encoded string are unwrapped.
	if res := ExecuteResult(ctx, ts, "read", json.RawMessage(`"{\"path\":\"f.go\",\"limit\":1}"`), false); res.Failed || res.Text != "1\ta\n" {
		t.Errorf("string-wrapped arguments: %q", res.Text)
	}

	for args, want := range map[string]string{
		`{"path":"f.go","offset":"two"}`: `read: argument "offset" must be a whole number, got string "two"`,
		`{"path":"f.go","limit":2.5}`:    `read: argument "limit" must be a whole number, got number 2.5`,
		`{"path":["f.go"]}`:              `read: argument "path" must be a string, got an array`,
		`{"offset":1}`:                   `read: missing required argument "path"`,
		`{"path":"f.go"`:                 "read: arguments must be a JSON object of named arguments (invalid JSON",
		`[1]`:                            "read: arguments must be a JSON object of named arguments (an array)",
	} {
		res := ExecuteResult(ctx, ts, "read", json.RawMessage(args), false)
		if !res.Failed || !strings.Contains(res.Text, want) || strings.Contains(res.Text, "Go value") {
			t.Errorf("read %s = %q, want %q", args, res.Text, want)
		}
	}
	res := ExecuteResult(ctx, ts, "edit", json.RawMessage(`{"path":"f.go","old_string":"a","new_string":"b","replace_all":"maybe"}`), false)
	if !res.Failed || !strings.Contains(res.Text, `edit: argument "replace_all" must be a boolean (true or false), got string "maybe"`) {
		t.Errorf("bad boolean: %s", res.Text)
	}
	res = ExecuteResult(ctx, ts, "bash", json.RawMessage(`{"command":"echo hi","timeout":"soon"}`), false)
	if !res.Failed || !strings.Contains(res.Text, `bash: argument "timeout" must be a number, got string "soon"`) {
		t.Errorf("bad number: %s", res.Text)
	}
	if res := ExecuteResult(ctx, ts, "bash", json.RawMessage(`{"command":"echo hi","timeout":"30"}`), false); res.Failed || !strings.Contains(res.Text, "hi") {
		t.Errorf("quoted timeout should run: %s", res.Text)
	}
}
