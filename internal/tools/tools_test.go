package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, name, args string) string {
	t.Helper()
	return Execute(context.Background(), All(), name, json.RawMessage(args))
}

func TestToolRoundTrip(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "sub", "a.txt")

	out := run(t, "write", fmt.Sprintf(`{"path":%q,"content":"one\ntwo\nthree\n"}`, f))
	if strings.HasPrefix(out, "Error") {
		t.Fatal(out)
	}
	out = run(t, "read", fmt.Sprintf(`{"path":%q}`, f))
	if !strings.Contains(out, "2\ttwo") {
		t.Fatalf("read missing line numbers: %q", out)
	}
	out = run(t, "edit", fmt.Sprintf(`{"path":%q,"old_string":"two","new_string":"2"}`, f))
	if strings.HasPrefix(out, "Error") {
		t.Fatal(out)
	}
	out = run(t, "read", fmt.Sprintf(`{"path":%q,"offset":2,"limit":1}`, f))
	if strings.TrimSpace(out) != "2\t2" {
		t.Fatalf("edit not applied: %q", out)
	}

	run(t, "write", fmt.Sprintf(`{"path":%q,"content":"x x"}`, f))
	out = run(t, "edit", fmt.Sprintf(`{"path":%q,"old_string":"x","new_string":"y"}`, f))
	if !strings.HasPrefix(out, "Error") {
		t.Fatalf("expected ambiguity error, got %q", out)
	}
	out = run(t, "bash", `{"command":"echo hi; echo err >&2; exit 3"}`)
	if !strings.Contains(out, "hi") || !strings.Contains(out, "err") || !strings.Contains(out, "exit") {
		t.Fatalf("bash output wrong: %q", out)
	}
	out = run(t, "nope", `{}`)
	if !strings.Contains(out, "unknown tool") {
		t.Fatalf("expected unknown tool error, got %q", out)
	}
}

func TestHelpersAndEdgeCases(t *testing.T) {
	if len(Defs(All())) != 4 {
		t.Fatal("expected 4 tool defs")
	}
	long := strings.Repeat("x", maxOutput+10)
	out := truncate(long)
	if !strings.Contains(out, "10 bytes elided from the middle") {
		t.Fatalf("truncate: %q", out[len(out)-60:])
	}

	if !strings.HasPrefix(out, strings.Repeat("x", 100)) || !strings.HasSuffix(out, strings.Repeat("x", 100)) {
		t.Fatal("middle elision must keep head and tail")
	}
	if !strings.Contains(out, "full output") {
		t.Fatal("truncation should spill the full output and point at it")
	}
	if out2 := TruncateTail(long); !strings.HasPrefix(out2, "[... first 10 bytes truncated]") {
		t.Fatalf("truncateTail: %q", out2[:40])
	}

	if truncate("ok") != "ok" || TruncateTail("ok") != "ok" {
		t.Fatal("short strings must not be modified")
	}

	for _, name := range []string{"bash", "read", "write", "edit"} {
		if out := run(t, name, `{bad`); !strings.HasPrefix(out, "Error") {
			t.Fatalf("%s: expected error, got %q", name, out)
		}
	}

	if out := run(t, "bash", `{"command":"true"}`); out != "(no output)" {
		t.Fatalf("empty output: %q", out)
	}

	if out := run(t, "bash", `{"command":"sleep 5","timeout":0.1}`); !strings.Contains(out, "timed out") {
		t.Fatalf("timeout: %q", out)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "f.txt")

	if out := run(t, "read", fmt.Sprintf(`{"path":%q}`, f)); !strings.HasPrefix(out, "Error") {
		t.Fatalf("missing file: %q", out)
	}
	run(t, "write", fmt.Sprintf(`{"path":%q,"content":"a\nb"}`, f))
	if out := run(t, "read", fmt.Sprintf(`{"path":%q,"offset":99}`, f)); !strings.Contains(out, "past end") {
		t.Fatalf("offset past EOF: %q", out)
	}

	if out := run(t, "write", fmt.Sprintf(`{"path":%q,"content":"x"}`, f+"/child.txt")); !strings.HasPrefix(out, "Error") {
		t.Fatalf("bad parent: %q", out)
	}

	if out := run(t, "edit", fmt.Sprintf(`{"path":%q,"old_string":"x","new_string":"y"}`, filepath.Join(dir, "nope"))); !strings.HasPrefix(out, "Error") {
		t.Fatalf("edit missing file: %q", out)
	}
	if out := run(t, "edit", fmt.Sprintf(`{"path":%q,"old_string":"zzz","new_string":"y"}`, f)); !strings.Contains(out, "not found") {
		t.Fatalf("edit not found: %q", out)
	}
	run(t, "write", fmt.Sprintf(`{"path":%q,"content":"x x x"}`, f))
	if out := run(t, "edit", fmt.Sprintf(`{"path":%q,"old_string":"x","new_string":"y","replace_all":true}`, f)); !strings.Contains(out, "3 occurrence") {
		t.Fatalf("replace_all: %q", out)
	}
}

func TestBashToolFastFailOnTTYRead(t *testing.T) {

	start := time.Now()
	out := run(t, "bash", `{"command":"read -r p < /dev/tty; echo got $p","timeout":5}`)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("bash tool hung %s on /dev/tty read — fast-fail regressed: %q", elapsed, out)
	}
	if strings.Contains(out, "timed out") {
		t.Fatalf("bash tool timed out on /dev/tty read — fast-fail regressed: %q", out)
	}

	if !strings.Contains(out, "/dev/tty") {
		t.Fatalf("expected a /dev/tty error in output: %q", out)
	}
}

type mockInteractiveRunner struct {
	gotCommand string
	gotTimeout time.Duration
	gotKeys    <-chan []byte
	returnThis string
}

func (m *mockInteractiveRunner) Run(_ context.Context, command string, timeout time.Duration, keys <-chan []byte) string {
	m.gotCommand = command
	m.gotTimeout = timeout
	m.gotKeys = keys
	return m.returnThis
}

func TestBashToolInteractiveHook(t *testing.T) {
	mock := &mockInteractiveRunner{returnThis: "PASSWORD_ACCEPTED\n(exit: 0)"}
	prev := InteractiveBash
	InteractiveBash = mock
	defer func() { InteractiveBash = prev }()

	out := run(t, "bash", `{"command":"sudo apt install -y sl","interactive":true,"timeout":20}`)
	if out != "PASSWORD_ACCEPTED\n(exit: 0)" {
		t.Fatalf("interactive bash should return runner output verbatim: %q", out)
	}
	if mock.gotCommand != "sudo apt install -y sl" {
		t.Fatalf("runner got wrong command: %q", mock.gotCommand)
	}
	if mock.gotTimeout != 20*time.Second {
		t.Fatalf("runner got wrong timeout: %v", mock.gotTimeout)
	}
	if mock.gotKeys == nil {
		t.Fatalf("runner must receive a keys channel")
	}

	mock.gotCommand = ""
	out = run(t, "bash", `{"command":"echo nohook"}`)
	if mock.gotCommand != "" {
		t.Fatalf("non-interactive call should not reach the runner: %q", mock.gotCommand)
	}
	if !strings.Contains(out, "nohook") {
		t.Fatalf("non-interactive output wrong: %q", out)
	}
}

func TestEditDiffLineNumbers(t *testing.T) {
	d := editDiff("ctx\nold\ntail", "ctx\nnew\ntail", 10)
	want := "10   ctx\n11 - old\n11 + new\n12   tail"
	if d != want {
		t.Fatalf("numbered diff:\n%s\nwant:\n%s", d, want)
	}
	if d := editDiff("old", "new", 0); d != "- old\n+ new" {
		t.Fatalf("unnumbered diff: %q", d)
	}
	if editDiff("same", "same", 5) != "" {
		t.Fatal("identical strings should yield no diff")
	}
	big := strings.Repeat("x\n", editDiffMaxLines+50)
	if d := editDiff("", big, 1); !strings.Contains(d, "more lines") {
		t.Fatal("oversized diff should carry the cap marker")
	}
}

func TestWriteToolDiffOnOverwrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	w := writeTool()
	out, err := w.Run(context.Background(), json.RawMessage(`{"path":"`+p+`","content":"a\nb\n"}`))
	if err != nil || strings.Contains(out, "```diff") {
		t.Fatalf("fresh write should carry no diff: %q, %v", out, err)
	}
	out, err = w.Run(context.Background(), json.RawMessage(`{"path":"`+p+`","content":"a\nc\n"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "```diff") || !strings.Contains(out, "2 - b") || !strings.Contains(out, "2 + c") {
		t.Fatalf("overwrite should diff with absolute line numbers: %q", out)
	}
}

func TestBinaryOutputPlaceholder(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want bool
	}{
		{name: "empty text", in: nil, want: false},
		{name: "plain text", in: []byte("package main\nimport \"fmt\"\nfunc main() { fmt.Println(\"hi\") }\n"), want: false},
		{name: "utf8 text", in: []byte("héllo wörld → ütf8 ✓\n"), want: false},
		{name: "single nul", in: []byte{0x00}, want: true},
		{name: "nul in text", in: append([]byte("abc"), 0x00, 'd', 'e', 'f'), want: true},
		{name: "invalid utf8", in: []byte{0xff, 0xfe, 0x00, 'x'}, want: true},
		{name: "control heavy", in: bytes.Repeat([]byte{0x01}, 100), want: true},
		{name: "whitespace controls ok", in: []byte("line1\n\tline2\rline3\f\v"), want: false},

		{name: "utf8 multi-byte deep in buffer", in: append(bytes.Repeat([]byte("a"), 1023), []byte("é世界")...), want: false},

		{name: "ansi colored output", in: []byte("\x1b[31mred\x1b[0m \x1b[32mgreen\x1b[0m \x1b[1mBold\x1b[0m normal text here\n"), want: false},

		{name: "text then binary deep in buffer", in: append(bytes.Repeat([]byte("a"), 1124), 0x00, 0x01), want: true},

		{name: "latin1 interior bytes stay binary", in: append(bytes.Repeat([]byte("a"), 1023), 0x93, 0x94, 0x92), want: true},

		{name: "invalid overlong stays binary", in: append(bytes.Repeat([]byte("a"), 1022), 0xE0, 0x80), want: true},

		{name: "c1 lead stays binary", in: append(bytes.Repeat([]byte("a"), 1023), 0xC1, 0xBF), want: true},

		{name: "invalid utf8 deep in buffer stays binary", in: append(bytes.Repeat([]byte("a"), 1124), 0x93, 0x94), want: true},

		{name: "control junk tail in buffer", in: append(bytes.Repeat([]byte("a"), 1024), bytes.Repeat([]byte{0x01}, 4096)...), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBinary(tt.in); got != tt.want {
				t.Fatalf("isBinary(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "blob.bin")
	raw := append(bytes.Repeat([]byte{0x01, 0x02, 0x03}, 40), 0x00, 0x00, 0x00)
	if err := os.WriteFile(bin, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	out := run(t, "read", fmt.Sprintf(`{"path":%q}`, bin))
	want := fmt.Sprintf("[binary: %s, %s]", bin, bytesHuman(len(raw)))
	if out != want {
		t.Fatalf("read placeholder:\n got %q\nwant %q", out, want)
	}

	if out := run(t, "bash", fmt.Sprintf(`{"command":"cat %s | head -c 200"}`, bin)); !strings.Contains(out, "not shown") {
		t.Fatalf("bash binary output not replaced: %q", out)
	}

	txt := filepath.Join(dir, "text.txt")
	if err := os.WriteFile(txt, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := run(t, "read", fmt.Sprintf(`{"path":%q}`, txt)); !strings.Contains(out, "2\ttwo") {
		t.Fatalf("text read should stay intact: %q", out)
	}
}
