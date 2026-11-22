package tui

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// pipeOutput is a syncOutput over a pipe, and a reader for what it wrote.
func pipeOutput(t *testing.T, on bool) (*syncOutput, func() string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return newSyncOutput(w, on), func() string {
		_ = w.Close()
		b, _ := io.ReadAll(r)
		return string(b)
	}
}

func TestSyncOutputBracketsEachWrite(t *testing.T) {
	out, read := pipeOutput(t, true)
	for _, frame := range []string{"\x1b[2Aframe one\r\n", "frame two"} {
		n, err := out.Write([]byte(frame))
		if err != nil || n != len(frame) {
			t.Fatalf("Write(%q) = %d, %v; want %d, nil", frame, n, err, len(frame))
		}
	}
	if n, err := out.Write(nil); n != 0 || err != nil {
		t.Fatalf("empty write = %d, %v", n, err)
	}
	got := read()
	want := syncBegin + "\x1b[2Aframe one\r\n" + syncEnd + syncBegin + "frame two" + syncEnd
	if got != want {
		t.Fatalf("written:\n%q\nwant:\n%q", got, want)
	}
}

func TestSyncOutputOffPassesThrough(t *testing.T) {
	out, read := pipeOutput(t, false)
	_, _ = out.Write([]byte("frame"))
	if got := read(); got != "frame" {
		t.Fatalf("written %q, want the frame untouched", got)
	}
}

// The wrapper must still look like the terminal to bubbletea (size, raw
// mode) and hand an external editor the file itself.
func TestSyncOutputKeepsTheFile(t *testing.T) {
	out, _ := pipeOutput(t, true)
	var f interface {
		io.ReadWriteCloser
		Fd() uintptr
	} = out
	if f.Fd() != out.File.Fd() {
		t.Fatal("Fd must be the wrapped file's")
	}
	if plainOutput(out) != any(out.File) {
		t.Fatal("plainOutput must unwrap to the *os.File")
	}
	var buf bytes.Buffer
	if plainOutput(&buf) != any(&buf) {
		t.Fatal("plainOutput must leave other writers alone")
	}
}

func TestParseSyncReply(t *testing.T) {
	cases := []struct {
		in              string
		supported, done bool
	}{
		{"\x1b[?2026;2$y\x1b[?62;22c", true, true},
		{"\x1b[?2026;1$y", true, true},
		{"\x1b[?2026;0$y\x1b[?1;2c", false, true},
		{"\x1b[?2026;4$y", false, true},
		{"\x1b[?64;1;2;6;22c", false, true},
		{"\x1b[?2026;", false, false},
		{"", false, false},
	}
	for _, c := range cases {
		s, d := parseSyncReply(c.in)
		if s != c.supported || d != c.done {
			t.Errorf("parseSyncReply(%q) = %v, %v; want %v, %v", c.in, s, d, c.supported, c.done)
		}
	}
}

func TestDecideSyncOutput(t *testing.T) {
	prevV, prevQ, prevM := tmuxVersion, syncTermQuery, moshDetect
	t.Cleanup(func() { tmuxVersion, syncTermQuery, moshDetect = prevV, prevQ, prevM })
	moshDetect = func() bool { return false }
	queried := false
	syncTermQuery = func() (bool, bool) { queried = true; return true, true }
	env := func(kv map[string]string) func(string) string {
		return func(k string) string { return kv[k] }
	}

	if on, how := decideSyncOutput(env(map[string]string{"K_BRAIN_SYNC_OUTPUT": "0"})); on || !strings.Contains(how, "K_BRAIN_SYNC_OUTPUT") || queried {
		t.Fatalf("override 0: on=%v how=%q queried=%v", on, how, queried)
	}
	if on, _ := decideSyncOutput(env(map[string]string{"K_BRAIN_SYNC_OUTPUT": "on", "TERM": "screen"})); !on {
		t.Fatal("override on must win over the terminal")
	}

	// tmux decides by its own version and is never asked through a passthrough.
	tmuxVersion = func() string { return "3.4" }
	if on, how := decideSyncOutput(env(map[string]string{"TMUX": "/tmp/t,1,0", "TERM": "tmux-256color"})); !on || queried {
		t.Fatalf("tmux 3.4: on=%v how=%q queried=%v", on, how, queried)
	}
	tmuxVersion = func() string { return "3.3a" }
	if on, how := decideSyncOutput(env(map[string]string{"TMUX": "/tmp/t,1,0"})); on || !strings.Contains(how, "3.4") {
		t.Fatalf("tmux 3.3a: on=%v how=%q", on, how)
	}
	if on, _ := decideSyncOutput(env(map[string]string{"TERM": "screen-256color"})); on || queried {
		t.Fatal("GNU screen gets no synchronized output and no query")
	}

	if on, how := decideSyncOutput(env(map[string]string{"TERM": "xterm-256color"})); !on || !queried || !strings.Contains(how, "2026") {
		t.Fatalf("supporting terminal: on=%v how=%q", on, how)
	}
	syncTermQuery = func() (bool, bool) { return false, true }
	if on, _ := decideSyncOutput(env(map[string]string{"TERM": "xterm-256color"})); on {
		t.Fatal("a terminal that answers without the mode gets none")
	}
	syncTermQuery = func() (bool, bool) { return false, false }
	if on, how := decideSyncOutput(env(nil)); on || !strings.Contains(how, "no answer") {
		t.Fatalf("silent terminal: on=%v how=%q", on, how)
	}
}

func TestTmuxAtLeast(t *testing.T) {
	for v, want := range map[string]bool{"3.4": true, "3.5a": true, "next-3.6": true, "4.0": true, "3.3a": false, "3.10": true, "2.9": false, "": false, "master": false} {
		if got := tmuxAtLeast(v, 3, 4); got != want {
			t.Errorf("tmuxAtLeast(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestDoctorReportsSyncOutput(t *testing.T) {
	prev := syncState
	t.Cleanup(func() { syncState = prev })
	syncState.on, syncState.how = true, "terminal reports mode 2026"
	if out := tasksModel("http://unused").doctorReport(); !strings.Contains(out, "synchronized output  on (terminal reports mode 2026)") {
		t.Fatalf("/doctor should report synchronized output:\n%s", out)
	}
}
