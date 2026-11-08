package sshmgr

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Shell limits.
const (
	shellBuffer      = 256 << 10
	DefaultReadBytes = 32 << 10
	MinReadBytes     = 4 << 10
	MaxReadBytes     = 128 << 10
	DefaultCols      = 120
	DefaultRows      = 32
)

// shell is a session's interactive PTY shell. Its output is kept in a ring
// of the last shellBuffer bytes.
type shell struct {
	sess  *ssh.Session
	stdin io.WriteCloser

	mu      sync.Mutex
	buf     []byte
	dropped int64
	exited  bool
	cols    int
	rows    int
}

// ShellOutput is a read_session result.
type ShellOutput struct {
	Output    string `json:"output"`
	Truncated bool   `json:"truncated"`
	// StartOffset and EndOffset place Output in everything the shell printed.
	StartOffset int64 `json:"outputStartOffset"`
	EndOffset   int64 `json:"outputEndOffset"`
	Exited      bool  `json:"exited"`
}

func (sh *shell) Write(p []byte) (int, error) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.buf = append(sh.buf, p...)
	if over := len(sh.buf) - shellBuffer; over > 0 {
		sh.dropped += int64(over)
		sh.buf = append(sh.buf[:0], sh.buf[over:]...)
	}
	return len(p), nil
}

func (sh *shell) close() {
	sh.sess.Close()
}

// startShell opens the session's PTY shell when it has none.
func (s *Session) startShell(cols, rows int) (*shell, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false, s.closedError()
	}
	if s.shell != nil {
		sh := s.shell
		sh.mu.Lock()
		exited := sh.exited
		sh.mu.Unlock()
		if !exited {
			return sh, false, nil
		}
	}
	if cols <= 0 {
		cols = s.ptyCols
	}
	if rows <= 0 {
		rows = s.ptyRows
	}
	if cols <= 0 {
		cols = DefaultCols
	}
	if rows <= 0 {
		rows = DefaultRows
	}
	sess, err := s.client.NewSession()
	if err != nil {
		return nil, false, fmt.Errorf("cannot open a shell channel: %v", err)
	}
	sh := &shell{sess: sess, cols: cols, rows: rows}
	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		return nil, false, err
	}
	sh.stdin = stdin
	sess.Stdout, sess.Stderr = sh, sh
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		sess.Close()
		return nil, false, fmt.Errorf("the SSH server refused a terminal: %v", err)
	}
	if err := sess.Shell(); err != nil {
		sess.Close()
		return nil, false, fmt.Errorf("the SSH server refused a shell: %v", err)
	}
	go func() {
		sess.Wait()
		sh.mu.Lock()
		sh.exited = true
		sh.mu.Unlock()
	}()
	s.shell = sh
	return sh, true, nil
}

// ReadShell returns the last maxBytes of the shell's output.
func (s *Session) ReadShell(maxBytes int) (ShellOutput, error) {
	done, err := s.use()
	if err != nil {
		return ShellOutput{}, err
	}
	defer done()
	sh, _, err := s.startShell(0, 0)
	if err != nil {
		return ShellOutput{}, err
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	data := sh.buf
	truncated := sh.dropped > 0
	start := sh.dropped
	if len(data) > maxBytes {
		start += int64(len(data) - maxBytes)
		data = data[len(data)-maxBytes:]
		truncated = true
	}
	return ShellOutput{Output: strings.ToValidUTF8(string(data), "�"), Truncated: truncated, StartOffset: start, EndOffset: sh.dropped + int64(len(sh.buf)), Exited: sh.exited}, nil
}

// SendInput writes data to the shell.
func (s *Session) SendInput(data string) error {
	done, err := s.use()
	if err != nil {
		return err
	}
	defer done()
	sh, _, err := s.startShell(0, 0)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(sh.stdin, data); err != nil {
		return errors.New("the shell no longer accepts input")
	}
	return nil
}

// Resize changes the shell's terminal size.
func (s *Session) Resize(cols, rows int) error {
	done, err := s.use()
	if err != nil {
		return err
	}
	defer done()
	sh, created, err := s.startShell(cols, rows)
	if err != nil || created {
		return err
	}
	if err := sh.sess.WindowChange(rows, cols); err != nil {
		return fmt.Errorf("resize failed: %v", err)
	}
	sh.mu.Lock()
	sh.cols, sh.rows = cols, rows
	sh.mu.Unlock()
	return nil
}

// setPTYSize sets the size the shell starts with; 0 keeps the current one.
func (s *Session) setPTYSize(cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cols > 0 {
		s.ptyCols = cols
	}
	if rows > 0 {
		s.ptyRows = rows
	}
}
