package sshmgr

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/elicit"
	"github.com/Stack-Cairn/K-brain/internal/sshhosts"
	"github.com/Stack-Cairn/K-brain/internal/tools"
	"golang.org/x/crypto/ssh"
)

// Interaction says what a call may ask the user.
type Interaction struct {
	// Unattended: nobody can answer (scheduled runs). Connecting then needs a
	// saved credential and an already trusted host key; anything else fails
	// at once.
	Unattended bool
	// Ask asks a question (host key confirmation); nil when nobody can be
	// asked.
	Ask tools.AskFunc
	// Elicit shows a form (keyboard-interactive prompts); nil when no front
	// end can show one.
	Elicit elicit.Func
}

const (
	// handshakeTimeout bounds the TCP connect plus SSH handshake, not
	// counting time spent waiting for the user.
	handshakeTimeout = 30 * time.Second
	// maxInteractiveRounds bounds keyboard-interactive challenges per login.
	maxInteractiveRounds = 5
	hostKeyTrustOnce     = "Trust once"
	hostKeyTrustSave     = "Trust and save"
	hostKeyReject        = "Reject"
)

// Errors that tell the model (and user) what to do next.
var (
	errHostKeyUnknown = errors.New("host key is not trusted")
)

// HostKeyChangedError refuses a host whose key differs from the stored one.
type HostKeyChangedError struct {
	Host      string
	Presented sshhosts.KnownKey
	Known     []sshhosts.KnownKey
}

func (e *HostKeyChangedError) Error() string {
	var known []string
	for _, k := range e.Known {
		known = append(known, k.Algorithm+" "+k.Fingerprint)
	}
	return fmt.Sprintf("REMOTE HOST KEY CHANGED for %s: it presented %s %s, but K-brain trusts %s. This can mean someone is intercepting the connection. SSHManager refuses to connect. If the server was really reinstalled or re-keyed, the user must remove the old key in Settings > SSH (DELETE /v1/settings/ssh/hosts/{id}/host-keys) and confirm the new fingerprint on the next connection.",
		e.Host, e.Presented.Algorithm, e.Presented.Fingerprint, strings.Join(known, ", "))
}

// dialer opens the transport to a host (direct or through its proxy).
type dialer func(ctx context.Context, h sshhosts.Host) (net.Conn, error)

// connector opens one SSH connection.
type connector struct {
	host  sshhosts.Host
	known *sshhosts.KnownHosts
	inter Interaction
	dial  dialer
	ctx   context.Context

	// pinned is the key the user accepted for this connection after it was
	// first presented.
	pinned ssh.PublicKey
	// presented is the unknown key the last attempt saw.
	presented    ssh.PublicKey
	rounds       int
	passwordUsed bool
	// password is a password the user typed for this connection.
	password string
	key      ssh.Signer
	watch    *watchdog
	askErr   error
}

// preflight refuses, before any network traffic, what cannot succeed.
func preflight(h sshhosts.Host, known *sshhosts.KnownHosts, inter Interaction) error {
	if strings.TrimSpace(h.Username) == "" {
		return fmt.Errorf("SSH host %q has no username; set one in Settings > SSH", h.Label())
	}
	switch h.Credential() {
	case sshhosts.CredentialMissing:
		if h.AuthType == sshhosts.AuthPrivateKey {
			return fmt.Errorf("SSH host %q has no saved private key (credential=missing). Ask the user to add the private key or privateKeyPath in Settings > SSH; never ask for it in chat", h.Label())
		}
		if inter.Unattended {
			return fmt.Errorf("SSH host %q has no saved password (credential=missing), and an unattended run cannot ask the user for it", h.Label())
		}
		if inter.Elicit == nil {
			return fmt.Errorf("SSH host %q has no saved password (credential=missing), and no client that can show login forms is attached (the run needs options.mcp_elicitation). Ask the user to save the password in Settings > SSH; never ask for it in chat", h.Label())
		}
	case sshhosts.CredentialInteractive:
		if inter.Unattended {
			return fmt.Errorf("SSH host %q logs in with keyboard-interactive prompts, which an unattended run cannot answer", h.Label())
		}
		if inter.Elicit == nil {
			return fmt.Errorf("SSH host %q logs in with keyboard-interactive prompts, but no client that can show login forms is attached (the run needs options.mcp_elicitation)", h.Label())
		}
	}
	if inter.Unattended && len(known.Keys(h.Address())) == 0 {
		return fmt.Errorf("SSH host %q has no trusted host key yet; an unattended run cannot ask the user to confirm it. Connect once from an interactive conversation and choose %q", h.Label(), hostKeyTrustSave)
	}
	return nil
}

// connect opens an authenticated client connection to c.host.
func (c *connector) connect() (*ssh.Client, error) {
	if err := preflight(c.host, c.known, c.inter); err != nil {
		return nil, err
	}
	auth, err := c.authMethods()
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		client, err := c.attempt(auth)
		if err == nil {
			return client, nil
		}
		if !errors.Is(err, errHostKeyUnknown) || attempt > 0 {
			return nil, err
		}
		if err := c.confirmHostKey(); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("SSH connection failed")
}

func (c *connector) attempt(auth []ssh.AuthMethod) (*ssh.Client, error) {
	ctx := c.ctx
	conn, err := c.dial(ctx, c.host)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	c.watch = newWatchdog(handshakeTimeout, func() { conn.Close() })
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer func() {
		stop()
		c.watch.stop()
	}()
	config := &ssh.ClientConfig{
		User:              c.host.Username,
		Auth:              auth,
		HostKeyCallback:   c.checkHostKey,
		HostKeyAlgorithms: c.known.Algorithms(c.host.Address()),
		BannerCallback:    func(string) error { return nil },
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, c.host.Address(), config)
	if err != nil {
		conn.Close()
		var changed *HostKeyChangedError
		switch {
		case errors.As(err, &changed):
			return nil, changed
		case errors.Is(err, errHostKeyUnknown):
			return nil, errHostKeyUnknown
		case c.askErr != nil:
			return nil, c.askErr
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case c.watch.fired():
			return nil, fmt.Errorf("SSH connection to %s timed out", c.host.Label())
		}
		return nil, fmt.Errorf("SSH connection to %s failed: %s", c.host.Label(), describeSSHError(err))
	}
	return ssh.NewClient(sshConn, chans, reqs), nil
}

func describeSSHError(err error) string {
	text := err.Error()
	if strings.Contains(text, "unable to authenticate") {
		return "authentication failed (" + text + "); check the credential saved in Settings > SSH"
	}
	return text
}

func (c *connector) checkHostKey(_ string, _ net.Addr, key ssh.PublicKey) error {
	if c.pinned != nil {
		if string(c.pinned.Marshal()) == string(key.Marshal()) {
			return nil
		}
		return &HostKeyChangedError{Host: c.host.Address(), Presented: sshhosts.Describe(key), Known: []sshhosts.KnownKey{sshhosts.Describe(c.pinned)}}
	}
	status, known, err := c.known.Check(c.host.Address(), key)
	if err != nil {
		return err
	}
	switch status {
	case sshhosts.HostKeyTrusted:
		return nil
	case sshhosts.HostKeyChanged:
		return &HostKeyChangedError{Host: c.host.Address(), Presented: sshhosts.Describe(key), Known: known}
	}
	c.presented = key
	return errHostKeyUnknown
}

// confirmHostKey asks the user about the key the last attempt saw. No
// connection is open meanwhile, so the server's login timeout cannot expire
// while the user decides.
func (c *connector) confirmHostKey() error {
	key := c.presented
	if key == nil {
		return errHostKeyUnknown
	}
	k := sshhosts.Describe(key)
	if c.inter.Unattended || c.inter.Ask == nil {
		return fmt.Errorf("SSH host %q presented an untrusted host key (%s %s) and nobody can confirm it in this run", c.host.Label(), k.Algorithm, k.Fingerprint)
	}
	question := fmt.Sprintf("SSHManager is connecting to %s (%s) for the first time. Its host key is not yet trusted:\n\nAlgorithm: %s\nFingerprint: %s\n\nTrust it only if this fingerprint matches the one the server's administrator gave you.",
		c.host.Label(), c.host.Address(), k.Algorithm, k.Fingerprint)
	started := time.Now()
	answers, ok := c.inter.Ask(c.ctx, tools.AskRequest{Question: question, Options: []tools.AskOption{
		{Label: hostKeyTrustOnce, Description: "Connect this time without saving the key; ask again next time."},
		{Label: hostKeyTrustSave, Description: "Save the key to K-brain's known_hosts and connect."},
		{Label: hostKeyReject, Description: "Do not connect.", Recommended: true},
	}})
	tools.ReportUserWait(c.ctx, time.Since(started))
	if c.ctx.Err() != nil {
		return c.ctx.Err()
	}
	choice := ""
	if ok && len(answers) == 1 && len(answers[0].Labels) == 1 && strings.TrimSpace(answers[0].Custom) == "" {
		choice = answers[0].Labels[0]
	}
	switch choice {
	case hostKeyTrustSave:
		if err := c.known.Add(c.host.Address(), key); err != nil {
			return fmt.Errorf("could not save the host key: %v", err)
		}
	case hostKeyTrustOnce:
	default:
		return fmt.Errorf("the user did not trust the host key of %q (%s %s); SSHManager did not connect", c.host.Label(), k.Algorithm, k.Fingerprint)
	}
	c.pinned = key
	return nil
}

func (c *connector) authMethods() ([]ssh.AuthMethod, error) {
	h := c.host
	interactive := ssh.KeyboardInteractive(c.challenge)
	switch h.AuthType {
	case sshhosts.AuthKeyboardInteractive:
		return []ssh.AuthMethod{interactive}, nil
	case sshhosts.AuthPrivateKey:
		pem, err := readPrivateKey(h)
		if err != nil {
			return nil, err
		}
		// The key is parsed when the server asks for it, after the host key
		// is trusted, so a missing passphrase is asked for only then.
		return []ssh.AuthMethod{ssh.PublicKeysCallback(func() ([]ssh.Signer, error) { return c.signer(pem) }), interactive}, nil
	default:
		return []ssh.AuthMethod{ssh.PasswordCallback(c.loginPassword), interactive}, nil
	}
}

// loginPassword is the saved password, or one the user types into a login
// form when none is saved. A typed password is kept only for this
// connection attempt.
func (c *connector) loginPassword() (string, error) {
	if c.host.Password != "" {
		return c.host.Password, nil
	}
	if c.password != "" {
		return c.password, nil
	}
	value, err := c.askSecret("password", "Password", fmt.Sprintf("SSHManager needs the password of %s (%s). It is used for this connection only and is not saved.", c.host.Label(), c.host.Endpoint()))
	if err != nil {
		return "", err
	}
	c.password = value
	return value, nil
}

// signer parses the private key, asking for its passphrase when the key is
// encrypted and none is saved.
func (c *connector) signer(pem []byte) ([]ssh.Signer, error) {
	if c.key != nil {
		return []ssh.Signer{c.key}, nil
	}
	h := c.host
	var signer ssh.Signer
	var err error
	if h.PrivateKeyPassphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(h.PrivateKeyPassphrase))
	} else {
		signer, err = ssh.ParsePrivateKey(pem)
	}
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		if c.inter.Unattended || c.inter.Elicit == nil {
			c.askErr = fmt.Errorf("SSH host %q: the private key is encrypted and no passphrase is saved; save it in Settings > SSH", h.Label())
			return nil, c.askErr
		}
		passphrase, askErr := c.askSecret("passphrase", "Private key passphrase", fmt.Sprintf("SSHManager needs the passphrase of the private key for %s (%s). It is used for this connection only and is not saved.", h.Label(), h.Endpoint()))
		if askErr != nil {
			return nil, askErr
		}
		signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(passphrase))
		if errors.Is(err, x509.IncorrectPasswordError) {
			c.askErr = fmt.Errorf("SSH host %q: the private key passphrase is wrong", h.Label())
			return nil, c.askErr
		}
	}
	switch {
	case errors.Is(err, x509.IncorrectPasswordError):
		c.askErr = fmt.Errorf("SSH host %q: the saved private key passphrase is wrong", h.Label())
		return nil, c.askErr
	case err != nil:
		c.askErr = fmt.Errorf("SSH host %q: the private key cannot be used", h.Label())
		return nil, c.askErr
	}
	c.key = signer
	return []ssh.Signer{signer}, nil
}

// readPrivateKey returns the inline key or the content of privateKeyPath.
func readPrivateKey(h sshhosts.Host) ([]byte, error) {
	if h.PrivateKey != "" {
		return []byte(h.PrivateKey), nil
	}
	path := strings.TrimSpace(h.PrivateKeyPath)
	if rest, ok := strings.CutPrefix(path, "~"); ok && (rest == "" || rest[0] == '/' || rest[0] == '\\') {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("SSH host %q: cannot resolve privateKeyPath", h.Label())
		}
		path = filepath.Join(home, rest)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("SSH host %q: cannot read the private key file set in privateKeyPath", h.Label())
	}
	return data, nil
}

// askSecret asks the user for one sensitive value through a login form.
func (c *connector) askSecret(name, title, message string) (string, error) {
	content, err := c.form(message, []ai.ElicitField{{Name: name, Type: "string", Title: title, Sensitive: true}})
	if err != nil {
		return "", err
	}
	value, _ := content[name].(string)
	if value == "" {
		c.askErr = fmt.Errorf("no %s was entered for SSH host %q", strings.ToLower(title), c.host.Label())
		return "", c.askErr
	}
	return value, nil
}

// looksLikePassword reports a prompt that asks for the account password, so a
// saved password answers it (servers often offer password logins as
// keyboard-interactive).
func looksLikePassword(prompt string) bool {
	p := strings.ToLower(prompt)
	return strings.Contains(p, "password") || strings.Contains(prompt, "密码")
}

// challenge answers a keyboard-interactive round: a lone hidden password
// prompt with the saved password (once), everything else through a form the
// user fills in.
func (c *connector) challenge(name, instruction string, questions []string, echos []bool) ([]string, error) {
	c.rounds++
	if c.rounds > maxInteractiveRounds {
		return nil, fmt.Errorf("SSH login to %q asked more than %d rounds of questions", c.host.Label(), maxInteractiveRounds)
	}
	if len(questions) == 0 {
		return nil, nil
	}
	if c.host.AuthType == sshhosts.AuthPassword && c.host.Password != "" && !c.passwordUsed && len(questions) == 1 && !echos[0] && looksLikePassword(questions[0]) {
		c.passwordUsed = true
		return []string{c.host.Password}, nil
	}
	if c.host.AuthType == sshhosts.AuthPassword && c.host.Password == "" && c.password != "" && !c.passwordUsed && len(questions) == 1 && !echos[0] && looksLikePassword(questions[0]) {
		c.passwordUsed = true
		return []string{c.password}, nil
	}
	fields := make([]ai.ElicitField, len(questions))
	names := make([]string, len(questions))
	for i, q := range questions {
		names[i] = fmt.Sprintf("response_%d", i+1)
		title := strings.TrimSpace(q)
		if title == "" {
			title = fmt.Sprintf("Response %d", i+1)
		}
		fields[i] = ai.ElicitField{Name: names[i], Type: "string", Title: title, Sensitive: i >= len(echos) || !echos[i]}
	}
	message := fmt.Sprintf("The SSH server %s (%s) asks for login input.", c.host.Label(), c.host.Endpoint())
	for _, line := range []string{name, instruction} {
		if line = strings.TrimSpace(line); line != "" {
			message += "\n\n" + line
		}
	}
	content, err := c.form(message, fields)
	if err != nil {
		return nil, err
	}
	answers := make([]string, len(questions))
	for i, n := range names {
		if v, ok := content[n].(string); ok {
			answers[i] = v
		}
	}
	return answers, nil
}

// form shows a login form and returns what the user entered. Every answer
// is a secret: the run's history records only which fields were asked.
func (c *connector) form(message string, fields []ai.ElicitField) (map[string]any, error) {
	fail := func(err error) (map[string]any, error) {
		c.askErr = err
		return nil, err
	}
	if c.inter.Unattended {
		return fail(fmt.Errorf("SSH host %q asked for login input (a password or one-time code), which an unattended run cannot answer", c.host.Label()))
	}
	if c.inter.Elicit == nil {
		return fail(fmt.Errorf("SSH host %q asked for login input (a password or one-time code), but no client that can show login forms is attached (the run needs options.mcp_elicitation)", c.host.Label()))
	}
	server := "ssh:" + c.host.Label()
	c.watch.pause()
	started := time.Now()
	resp := c.inter.Elicit(c.ctx, elicit.Request{Source: elicit.SourceSSH, Server: server, Tool: "SSHManager", Message: message, Fields: fields})
	tools.ReportUserWait(c.ctx, time.Since(started))
	c.watch.resume()
	record := ai.Elicitation{ElicitationID: resp.ID, Source: elicit.SourceSSH, Server: server, Message: message, Fields: fields, Action: resp.Action, Reason: resp.Reason}
	record.Content, record.Redacted = elicit.RedactFor(elicit.SourceSSH, fields, resp.Content)
	tools.ReportElicitation(c.ctx, record)
	if resp.Action != elicit.Accept {
		switch resp.Reason {
		case elicit.ReasonNoClient:
			return fail(fmt.Errorf("SSH host %q asked for login input, but no client that can show login forms is attached (the run needs options.mcp_elicitation)", c.host.Label()))
		case elicit.ReasonUnattended:
			return fail(fmt.Errorf("SSH host %q asked for login input, which an unattended run cannot answer", c.host.Label()))
		case elicit.ReasonTimeout:
			return fail(fmt.Errorf("nobody answered the SSH login form of %q in time", c.host.Label()))
		case elicit.ReasonCancelled:
			return fail(context.Canceled)
		}
		return fail(fmt.Errorf("the user declined the SSH login form of %q", c.host.Label()))
	}
	return resp.Content, nil
}

// watchdog calls fire once d elapses outside paused periods.
type watchdog struct {
	mu      sync.Mutex
	timer   *time.Timer
	d       time.Duration
	done    bool
	expired bool
}

func newWatchdog(d time.Duration, fire func()) *watchdog {
	w := &watchdog{d: d}
	w.timer = time.AfterFunc(d, func() {
		w.mu.Lock()
		if w.done {
			w.mu.Unlock()
			return
		}
		w.expired = true
		w.mu.Unlock()
		fire()
	})
	return w
}

func (w *watchdog) pause() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.timer.Stop()
}

func (w *watchdog) resume() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.done && !w.expired {
		w.timer.Reset(w.d)
	}
}

func (w *watchdog) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.done = true
	w.timer.Stop()
}

func (w *watchdog) fired() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.expired
}
