// Package sshtest runs an SSH server inside the test process, on 127.0.0.1,
// for SSHManager tests. It logs users in with a password, a public key or
// keyboard-interactive questions, answers "exec" with a tiny built-in
// command set, echoes a PTY shell's input and serves SFTP from memory. It
// never runs a real command and never touches the real filesystem, so the
// tests behave the same on every OS.
package sshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Question is one keyboard-interactive prompt and the answer it expects.
type Question struct {
	Prompt string
	Echo   bool
	Answer string
}

// Config sets how the server logs users in.
type Config struct {
	User string
	// Password, when set, is accepted by password logins.
	Password string
	// Questions, when set, are asked in one keyboard-interactive round.
	Questions []Question
	// PublicKey accepts logins with ClientKey.
	PublicKey bool
}

// Server is a running test server.
type Server struct {
	Host string
	Port int
	User string
	// ClientKey is the key PublicKey logins accept; ClientKeyPEM is its
	// unencrypted OpenSSH private key.
	ClientKey    ed25519.PrivateKey
	ClientKeyPEM []byte

	cfg      Config
	ln       net.Listener
	handlers sftp.Handlers
	wg       sync.WaitGroup

	mu        sync.Mutex
	hostKey   ssh.Signer
	conns     map[*ssh.ServerConn]bool
	logins    int
	authTries []string
	execs     []string
	ptys      [][2]int
	resizes   [][2]int
	signals   []string
	readGate  chan struct{}
	blocked   chan struct{}
	closed    bool
}

// New starts a server and stops it when t ends.
func New(t testing.TB, cfg Config) *Server {
	t.Helper()
	if cfg.User == "" {
		cfg.User = "tester"
	}
	_, clientKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(clientKey, "sshtest client")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, User: cfg.User, ClientKey: clientKey, ClientKeyPEM: pem.EncodeToMemory(block),
		cfg: cfg, ln: ln, conns: map[*ssh.ServerConn]bool{}}
	s.hostKey = NewHostKey(t)
	s.handlers = sftp.InMemHandler()
	s.handlers.FileGet = gatedGet{s: s, inner: s.handlers.FileGet}
	s.wg.Add(1)
	go s.accept()
	t.Cleanup(s.Close)
	return s
}

// NewHostKey returns a fresh ed25519 host key.
func NewHostKey(t testing.TB) ssh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// EncryptedClientKey returns ClientKey as an OpenSSH private key encrypted
// with passphrase.
func (s *Server) EncryptedClientKey(t testing.TB, passphrase string) []byte {
	t.Helper()
	block, err := ssh.MarshalPrivateKeyWithPassphrase(s.ClientKey, "sshtest client", []byte(passphrase))
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block)
}

// Address is host:port.
func (s *Server) Address() string { return net.JoinHostPort(s.Host, strconv.Itoa(s.Port)) }

// HostKey is the key the server presents.
func (s *Server) HostKey() ssh.PublicKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hostKey.PublicKey()
}

// SetHostKey makes later connections present key.
func (s *Server) SetHostKey(key ssh.Signer) {
	s.mu.Lock()
	s.hostKey = key
	s.mu.Unlock()
}

// Logins counts successful logins.
func (s *Server) Logins() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logins
}

// AuthTries lists the auth methods clients tried, in order.
func (s *Server) AuthTries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.authTries...)
}

// Execs lists the exec commands received, as sent.
func (s *Server) Execs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.execs...)
}

// PTYs lists the cols x rows of every PTY requested.
func (s *Server) PTYs() [][2]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][2]int(nil), s.ptys...)
}

// Resizes lists every window change, cols x rows.
func (s *Server) Resizes() [][2]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][2]int(nil), s.resizes...)
}

// Signals lists the signals clients sent to commands.
func (s *Server) Signals() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.signals...)
}

// Connections counts open connections.
func (s *Server) Connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// BlockReads makes SFTP file reads wait until release is called; blocked
// is closed once a read is waiting.
func (s *Server) BlockReads() (blocked <-chan struct{}, release func()) {
	gate, waiting := make(chan struct{}), make(chan struct{})
	s.mu.Lock()
	s.readGate, s.blocked = gate, waiting
	s.mu.Unlock()
	var once sync.Once
	return waiting, func() {
		once.Do(func() {
			s.mu.Lock()
			if s.readGate == gate {
				s.readGate, s.blocked = nil, nil
			}
			s.mu.Unlock()
			close(gate)
		})
	}
}

// Close stops the server and every connection.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	gate := s.readGate
	s.readGate, s.blocked = nil, nil
	conns := make([]*ssh.ServerConn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	if gate != nil {
		close(gate)
	}
	s.ln.Close()
	for _, c := range conns {
		c.Close()
	}
	s.wg.Wait()
}

// DropConnections closes every open connection, as a server restart would.
func (s *Server) DropConnections() {
	s.mu.Lock()
	conns := make([]*ssh.ServerConn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

func (s *Server) config() *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{}
	record := func(method string) {
		s.mu.Lock()
		s.authTries = append(s.authTries, method)
		s.mu.Unlock()
	}
	if s.cfg.Password != "" {
		cfg.PasswordCallback = func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			record("password")
			if meta.User() == s.cfg.User && string(password) == s.cfg.Password {
				return nil, nil
			}
			return nil, errors.New("wrong password")
		}
	}
	if s.cfg.PublicKey {
		want, _ := ssh.NewPublicKey(s.ClientKey.Public())
		cfg.PublicKeyCallback = func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			record("publickey")
			if meta.User() == s.cfg.User && string(key.Marshal()) == string(want.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("unknown key")
		}
	}
	if len(s.cfg.Questions) > 0 {
		cfg.KeyboardInteractiveCallback = func(meta ssh.ConnMetadata, client ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			record("keyboard-interactive")
			prompts := make([]string, len(s.cfg.Questions))
			echos := make([]bool, len(s.cfg.Questions))
			for i, q := range s.cfg.Questions {
				prompts[i], echos[i] = q.Prompt, q.Echo
			}
			answers, err := client(meta.User(), "Test login", prompts, echos)
			if err != nil {
				return nil, err
			}
			if meta.User() != s.cfg.User || len(answers) != len(prompts) {
				return nil, errors.New("wrong answers")
			}
			for i, q := range s.cfg.Questions {
				if answers[i] != q.Answer {
					return nil, errors.New("wrong answers")
				}
			}
			return nil, nil
		}
	}
	s.mu.Lock()
	cfg.AddHostKey(s.hostKey)
	s.mu.Unlock()
	return cfg
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go s.serve(conn)
	}
}

func (s *Server) serve(nc net.Conn) {
	defer s.wg.Done()
	_ = nc.SetDeadline(time.Now().Add(time.Minute))
	conn, chans, reqs, err := ssh.NewServerConn(nc, s.config())
	if err != nil {
		nc.Close()
		return
	}
	_ = nc.SetDeadline(time.Time{})
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		conn.Close()
		return
	}
	s.conns[conn] = true
	s.logins++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		conn.Close()
	}()
	go func() {
		for req := range reqs {
			if req.WantReply {
				_ = req.Reply(req.Type == "keepalive@openssh.com", nil)
			}
		}
	}()
	var chWG sync.WaitGroup
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		chWG.Add(1)
		go func() {
			defer chWG.Done()
			s.session(ch, chReqs)
		}()
	}
	chWG.Wait()
}

// session serves one session channel.
func (s *Server) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	signals := make(chan string, 4)
	var cmdDone chan struct{}
	for req := range reqs {
		switch req.Type {
		case "exec":
			var payload struct{ Command string }
			if ssh.Unmarshal(req.Payload, &payload) != nil {
				_ = req.Reply(false, nil)
				continue
			}
			s.mu.Lock()
			s.execs = append(s.execs, payload.Command)
			s.mu.Unlock()
			_ = req.Reply(true, nil)
			cmdDone = make(chan struct{})
			go func(done chan struct{}) {
				defer close(done)
				code := s.run(ch, payload.Command, signals)
				if code >= 0 {
					_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
				}
				_ = ch.CloseWrite()
				ch.Close()
			}(cmdDone)
		case "pty-req":
			var payload struct {
				Term                      string
				Cols, Rows, Width, Height uint32
				Modes                     string
			}
			if ssh.Unmarshal(req.Payload, &payload) != nil {
				_ = req.Reply(false, nil)
				continue
			}
			s.mu.Lock()
			s.ptys = append(s.ptys, [2]int{int(payload.Cols), int(payload.Rows)})
			s.mu.Unlock()
			_ = req.Reply(true, nil)
		case "window-change":
			var payload struct{ Cols, Rows, Width, Height uint32 }
			if ssh.Unmarshal(req.Payload, &payload) == nil {
				s.mu.Lock()
				s.resizes = append(s.resizes, [2]int{int(payload.Cols), int(payload.Rows)})
				s.mu.Unlock()
			}
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
		case "shell":
			_ = req.Reply(true, nil)
			go func() {
				_, _ = io.WriteString(ch, "welcome to sshtest\r\n$ ")
				buf := make([]byte, 4096)
				for {
					n, err := ch.Read(buf)
					if n > 0 {
						_, _ = ch.Write([]byte(strings.ReplaceAll(string(buf[:n]), "\n", "\r\n")))
					}
					if err != nil {
						return
					}
				}
			}()
		case "subsystem":
			var payload struct{ Name string }
			if ssh.Unmarshal(req.Payload, &payload) != nil || payload.Name != "sftp" {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			go func() {
				server := sftp.NewRequestServer(ch, s.handlers)
				_ = server.Serve()
				server.Close()
				ch.Close()
			}()
		case "signal":
			var payload struct{ Signal string }
			_ = ssh.Unmarshal(req.Payload, &payload)
			s.mu.Lock()
			s.signals = append(s.signals, payload.Signal)
			s.mu.Unlock()
			select {
			case signals <- payload.Signal:
			default:
			}
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
	if cmdDone != nil {
		select {
		case signals <- "":
		default:
		}
		<-cmdDone
	}
}

// run interprets command and returns its exit status, or -1 for none (it
// was stopped by a signal or the channel closed).
//
//	echo TEXT     prints TEXT
//	pwd           prints the working directory ("cd 'DIR' && " prefix sets it)
//	stderr TEXT   prints TEXT on stderr
//	exit N        exits with N
//	yes N         prints N bytes of "y"
//	sleep         waits for a signal or the channel to close
func (s *Server) run(ch ssh.Channel, command string, signals <-chan string) int {
	cwd := "/home/" + s.cfg.User
	if rest, ok := strings.CutPrefix(command, "cd '"); ok {
		if i := strings.Index(rest, "' && "); i >= 0 {
			cwd, command = strings.ReplaceAll(rest[:i], `'"'"'`, "'"), rest[i+len("' && "):]
		}
	}
	name, arg, _ := strings.Cut(command, " ")
	switch name {
	case "echo":
		fmt.Fprintln(ch, arg)
	case "pwd":
		fmt.Fprintln(ch, cwd)
	case "stderr":
		fmt.Fprintln(ch.Stderr(), arg)
	case "exit":
		n, _ := strconv.Atoi(arg)
		return n
	case "yes":
		n, _ := strconv.Atoi(arg)
		_, _ = ch.Write([]byte(strings.Repeat("y", n)))
	case "sleep":
		// Stdin may be closed at once; only a signal or the channel closing
		// (session sends "" then) ends the wait.
		select {
		case <-signals:
		case <-time.After(time.Minute):
		}
		return -1
	default:
		fmt.Fprintf(ch.Stderr(), "%s: command not found\n", name)
		return 127
	}
	return 0
}

// gatedGet holds SFTP reads while a BlockReads gate is closed.
type gatedGet struct {
	s     *Server
	inner sftp.FileReader
}

func (g gatedGet) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	ra, err := g.inner.Fileread(r)
	if err != nil {
		return nil, err
	}
	return gatedReaderAt{s: g.s, inner: ra}, nil
}

type gatedReaderAt struct {
	s     *Server
	inner io.ReaderAt
}

func (g gatedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	g.s.mu.Lock()
	gate, blocked := g.s.readGate, g.s.blocked
	if blocked != nil {
		g.s.blocked = nil
		close(blocked)
	}
	g.s.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return g.inner.ReadAt(p, off)
}
