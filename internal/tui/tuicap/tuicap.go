//go:build tuicap

package tuicap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// Keys, as a terminal in its default modes sends them.
const (
	Enter     = "\r"
	Esc       = "\x1b"
	Tab       = "\t"
	ShiftTab  = "\x1b[Z"
	Backspace = "\x7f"
	Delete    = "\x1b[3~"
	Up        = "\x1b[A"
	Down      = "\x1b[B"
	Right     = "\x1b[C"
	Left      = "\x1b[D"
	PgUp      = "\x1b[5~"
	PgDown    = "\x1b[6~"
	CtrlC     = "\x03"
)

// Ctrl returns the byte Ctrl+<letter> sends, e.g. Ctrl('k').
func Ctrl(letter rune) string {
	return string(rune(letter & 0x1f))
}

// Options configure a session. The zero value is a 100×30 terminal with no
// scripted replies.
type Options struct {
	Width, Height int
	// Replies are streamed back, in order, to the turns kn sends. Once they
	// run out every further turn gets Text("ok").
	Replies []Reply
	// Args are passed to kn.
	Args []string
	// Env adds or overrides environment variables, as KEY=VALUE.
	Env []string
	// Config is merged over the generated config.json (top-level keys).
	Config map[string]any
	// Timeout bounds every WaitFor; default 10s.
	Timeout time.Duration
}

// Session is one running kn.
type Session struct {
	t        testing.TB
	cmd      *exec.Cmd
	pty      *os.File
	provider *Provider
	timeout  time.Duration

	// Home is the throwaway K_BRAIN_HOME; Dir is the working directory kn was
	// started in; Clipboard is the file the stub clipboard tools write to.
	Home, Dir, Clipboard string

	mu   sync.Mutex
	term *vt.Emulator
	raw  bytes.Buffer
	done chan struct{}
	err  error
}

// Start builds kn if needed and runs it in a PTY. The session is closed when
// the test ends.
func Start(t testing.TB, opts Options) *Session {
	t.Helper()
	if opts.Width == 0 {
		opts.Width = 100
	}
	if opts.Height == 0 {
		opts.Height = 30
	}
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Second
	}
	bin := knBinary(t)

	root := t.TempDir()
	home := filepath.Join(root, "home")
	kbHome := filepath.Join(root, "k-brain")
	dir := filepath.Join(root, "project")
	stubs := filepath.Join(root, "bin")
	for _, d := range []string{home, kbHome, dir, stubs} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}

	provider := NewProvider(opts.Replies)
	t.Cleanup(provider.Close)
	writeConfig(t, kbHome, provider.URL, opts.Config)
	trust(t, kbHome, dir)

	// The clipboard never leaves the session: kn finds these stubs before the
	// real tools on PATH and they write to a file instead.
	clip := filepath.Join(root, "clipboard.txt")
	for _, name := range []string{"pbcopy", "wl-copy", "xclip", "xsel"} {
		stub := "#!/bin/sh\ncat > " + strconv.Quote(clip) + "\n"
		if err := os.WriteFile(filepath.Join(stubs, name), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command(bin, opts.Args...)
	cmd.Dir = dir
	cmd.Env = append([]string{
		"PATH=" + stubs + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + home,
		"K_BRAIN_HOME=" + kbHome,
		"LIVEAGENT_HOME=",
		"PWD=" + dir,
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"TZ=UTC",
		"K_BRAIN_CLIPBOARD_NO_OSC52=1",
	}, opts.Env...)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(opts.Width), Rows: uint16(opts.Height)})
	if err != nil {
		t.Fatalf("start kn in a pty: %v", err)
	}
	s := &Session{
		t: t, cmd: cmd, pty: ptmx, provider: provider, timeout: opts.Timeout,
		Home: kbHome, Dir: dir, Clipboard: clip,
		term: vt.NewEmulator(opts.Width, opts.Height),
		done: make(chan struct{}),
	}
	go s.pump()
	go s.answer()
	go func() {
		s.err = cmd.Wait()
		close(s.done)
	}()
	t.Cleanup(s.Close)
	return s
}

// pump feeds everything kn writes into the emulator and the raw log.
func (s *Session) pump() {
	buf := make([]byte, 32<<10)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.raw.Write(buf[:n])
			_, _ = s.term.Write(buf[:n])
			s.mu.Unlock()
		}
		if err != nil {
			if pw, ok := s.term.InputPipe().(*io.PipeWriter); ok {
				_ = pw.Close()
			}
			return
		}
	}
}

// answer sends the emulator's replies to terminal queries (cursor position,
// device attributes, background colour) back to kn.
func (s *Session) answer() {
	buf := make([]byte, 1024)
	for {
		n, err := s.term.Read(buf)
		if n > 0 {
			_, _ = s.pty.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// Type sends text as typed characters.
func (s *Session) Type(text string) {
	s.t.Helper()
	s.write(text)
}

// Press sends keys one at a time, with a short gap so the terminal reader
// does not merge them into one sequence (Esc then a letter would read as
// Alt+letter).
func (s *Session) Press(keys ...string) {
	s.t.Helper()
	for _, k := range keys {
		s.write(k)
		time.Sleep(40 * time.Millisecond)
	}
}

// Paste sends text as a bracketed paste.
func (s *Session) Paste(text string) {
	s.t.Helper()
	s.write("\x1b[200~" + text + "\x1b[201~")
}

func (s *Session) write(b string) {
	s.t.Helper()
	if _, err := io.WriteString(s.pty, b); err != nil {
		s.t.Fatalf("write to kn: %v", err)
	}
}

// Resize changes the terminal size, as a window resize would.
func (s *Session) Resize(width, height int) {
	s.t.Helper()
	s.mu.Lock()
	s.term.Resize(width, height)
	s.mu.Unlock()
	if err := pty.Setsize(s.pty, &pty.Winsize{Cols: uint16(width), Rows: uint16(height)}); err != nil {
		s.t.Fatalf("resize pty: %v", err)
	}
}

// Snapshot is the screen as text: one line per row, trailing blanks trimmed.
func (s *Session) Snapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, h := s.term.Width(), s.term.Height()
	lines := make([]string, h)
	var row strings.Builder
	for y := range h {
		row.Reset()
		for x := 0; x < w; x++ {
			c := s.term.CellAt(x, y)
			switch {
			case c == nil:
				row.WriteByte(' ')
			case c.Width == 0 && c.Content == "":
				// The second half of a wide character.
			case c.Content == "":
				row.WriteByte(' ')
			default:
				row.WriteString(c.Content)
			}
		}
		lines[y] = strings.TrimRight(row.String(), " ")
	}
	return strings.Join(lines, "\n") + "\n"
}

// Raw is every byte kn has written so far, escapes included.
func (s *Session) Raw() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Clone(s.raw.Bytes())
}

// WaitFor waits until text is on screen and returns that screen. It fails the
// test, printing the screen, if the text does not appear in time.
func (s *Session) WaitFor(text string) string {
	s.t.Helper()
	return s.WaitUntil("screen shows "+strconv.Quote(text), func(screen string) bool {
		return strings.Contains(screen, text)
	})
}

// WaitGone waits until text is no longer on screen.
func (s *Session) WaitGone(text string) string {
	s.t.Helper()
	return s.WaitUntil("screen no longer shows "+strconv.Quote(text), func(screen string) bool {
		return !strings.Contains(screen, text)
	})
}

// WaitUntil waits until ok accepts the screen and returns that screen.
func (s *Session) WaitUntil(what string, ok func(screen string) bool) string {
	s.t.Helper()
	deadline := time.Now().Add(s.timeout)
	for {
		screen := s.Snapshot()
		if ok(screen) {
			return screen
		}
		select {
		case <-s.done:
			s.t.Fatalf("kn exited (%v) while waiting until %s; last screen:\n%s", s.err, what, screen)
		default:
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("timed out after %s waiting until %s; screen:\n%s", s.timeout, what, screen)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Requests are the streamed turns kn has sent the fake provider so far.
func (s *Session) Requests() []Request { return s.provider.Requests() }

// Exited reports whether kn has exited, and with what.
func (s *Session) Exited() (bool, error) {
	select {
	case <-s.done:
		return true, s.err
	default:
		return false, nil
	}
}

// Close quits kn with Ctrl+C Ctrl+C, killing it if it does not exit, and
// releases the PTY. It is safe to call more than once.
func (s *Session) Close() {
	if exited, _ := s.Exited(); !exited {
		_, _ = io.WriteString(s.pty, CtrlC)
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(s.pty, CtrlC)
		select {
		case <-s.done:
		case <-time.After(3 * time.Second):
			_ = s.cmd.Process.Kill()
			<-s.done
		}
	}
	_ = s.pty.Close()
}

func writeConfig(t testing.TB, home, url string, extra map[string]any) {
	t.Helper()
	cfg := map[string]any{
		"defaultModel": "tuicap-model",
		"providers": map[string]any{
			"fake": map[string]any{
				"baseUrl": url,
				"api":     "openai-completions",
				"apiKey":  "tuicap-fake-key",
				"models":  []any{map[string]any{"id": "tuicap-model", "maxTokens": 4096}},
			},
		},
		"theme": "dark",
	}
	for k, v := range extra {
		cfg[k] = v
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func trust(t testing.TB, home, dir string) {
	t.Helper()
	toml := fmt.Sprintf("[folders.%s]\ntrusted = true\ndecided_at = %d\n", strconv.Quote(dir), time.Now().Unix())
	if err := os.WriteFile(filepath.Join(home, "trusted_folders.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
}

var (
	buildOnce sync.Once
	builtBin  string
	buildErr  error
)

// knBinary returns TUICAP_KN or a kn built from this checkout, once per test
// binary. The build lands in the OS temp directory; Cleanup removes it.
func knBinary(t testing.TB) string {
	t.Helper()
	if bin := os.Getenv("TUICAP_KN"); bin != "" {
		return bin
	}
	buildOnce.Do(func() {
		_, here, _, _ := runtime.Caller(0)
		root := filepath.Join(filepath.Dir(here), "..", "..", "..")
		dir, err := os.MkdirTemp("", "tuicap-kn-")
		if err != nil {
			buildErr = err
			return
		}
		builtBin = filepath.Join(dir, "kn")
		cmd := exec.Command("go", "build", "-o", builtBin, "./cmd/kn")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build ./cmd/kn: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return builtBin
}

// Cleanup removes the kn binary Start built. Call it from TestMain after
// m.Run if the temp directory should not keep it.
func Cleanup() {
	if builtBin != "" {
		_ = os.RemoveAll(filepath.Dir(builtBin))
	}
}
