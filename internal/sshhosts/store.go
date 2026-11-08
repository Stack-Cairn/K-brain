// Package sshhosts stores the SSH hosts SSHManager may connect to, the
// projects each host is associated with, and K-brain's own known_hosts file.
//
// Hosts live in <dir>/hosts.json, host keys in <dir>/known_hosts (OpenSSH
// format); dir is created 0700 and both files are written 0600 through a
// staging file, the way config.json (which holds provider API keys) is.
// Secrets (password, private key, private key passphrase, proxy password) are
// stored as written and never leave the package except to open a
// connection: Public projects them to "...Configured" flags.
package sshhosts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Auth types, as LiveAgent names them.
const (
	AuthPassword            = "password"
	AuthPrivateKey          = "privateKey"
	AuthKeyboardInteractive = "keyboardInteractive"
)

// Credential states list_hosts and GET /v1/settings/ssh report.
const (
	CredentialSaved       = "saved"
	CredentialMissing     = "missing"
	CredentialInteractive = "interactive"
)

// Limits on what a client may store.
const (
	MaxHosts           = 256
	MaxHostsPerProject = 64
	maxSecretBytes     = 64 << 10
)

// Host is one stored SSH host, secrets included. It never leaves the backend
// as is: Public is the projection clients see.
type Host struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	Description          string `json:"description,omitempty"`
	Host                 string `json:"host"`
	Port                 int    `json:"port"`
	Username             string `json:"username,omitempty"`
	AuthType             string `json:"authType"`
	Password             string `json:"password,omitempty"`
	PrivateKey           string `json:"privateKey,omitempty"`
	PrivateKeyPath       string `json:"privateKeyPath,omitempty"`
	PrivateKeyPassphrase string `json:"privateKeyPassphrase,omitempty"`
	Proxy                Proxy  `json:"proxy"`
}

// Proxy is the optional proxy a host is reached through.
type Proxy struct {
	// Type is "socks5" (default) or "http"; a scheme in URL wins.
	Type           string `json:"type"`
	URL            string `json:"url,omitempty"`
	Port           int    `json:"port,omitempty"`
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	UseSystemProxy bool   `json:"useSystemProxy,omitempty"`
}

// Configured reports whether a proxy is set up (LiveAgent's rule).
func (p Proxy) Configured() bool {
	return p.UseSystemProxy || strings.TrimSpace(p.URL) != "" || p.Port > 0 || strings.TrimSpace(p.Username) != "" || p.Password != ""
}

// Label is the name to show for the host.
func (h Host) Label() string {
	if h.Name != "" {
		return h.Name
	}
	return h.Host
}

// Endpoint is user@host:port.
func (h Host) Endpoint() string {
	return h.Username + "@" + hostPort(h.Host, h.Port)
}

// Address is the host:port to dial, IPv6 literals bracketed.
func (h Host) Address() string { return hostPort(h.Host, h.Port) }

func hostPort(host string, port int) string {
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"), strconv.Itoa(port))
}

// Credential reports whether connecting needs nothing from the user (saved),
// is impossible until credentials are stored (missing), or needs the user to
// answer the server's prompts (interactive).
func (h Host) Credential() string {
	switch h.AuthType {
	case AuthKeyboardInteractive:
		return CredentialInteractive
	case AuthPrivateKey:
		if h.PrivateKey != "" || strings.TrimSpace(h.PrivateKeyPath) != "" {
			return CredentialSaved
		}
	default:
		if h.Password != "" {
			return CredentialSaved
		}
	}
	return CredentialMissing
}

// connectionFingerprint changes whenever anything used to open a connection
// changes, so a session opened with old settings is not reused.
func (h Host) connectionFingerprint() string {
	h.Name, h.Description = "", ""
	b, _ := json.Marshal(h)
	return string(b)
}

// ConnectionFingerprint is connectionFingerprint for other packages; it is
// an opaque value that includes secrets and must never be shown or logged.
func (h Host) ConnectionFingerprint() string { return h.connectionFingerprint() }

// Document is the stored file.
type Document struct {
	Hosts []Host `json:"hosts"`
	// ProjectHostAssociations maps a ProjectPathKey to the ids of the hosts
	// SSHManager may use in that project.
	ProjectHostAssociations map[string][]string `json:"projectHostAssociations"`
}

func (d Document) clone() Document {
	out := Document{Hosts: slices.Clone(d.Hosts), ProjectHostAssociations: make(map[string][]string, len(d.ProjectHostAssociations))}
	for key, ids := range d.ProjectHostAssociations {
		out.ProjectHostAssociations[key] = slices.Clone(ids)
	}
	return out
}

// Store holds the hosts document and its file.
type Store struct {
	dir      string
	mu       sync.Mutex
	doc      Document
	revision uint64
	known    *KnownHosts
}

// Open loads (or starts) the store in dir.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("ssh settings directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create ssh settings directory: %w", err)
	}
	s := &Store{dir: dir, revision: 1, doc: Document{ProjectHostAssociations: map[string][]string{}}, known: &KnownHosts{path: filepath.Join(dir, "known_hosts")}}
	data, err := os.ReadFile(s.path())
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("read ssh hosts: %w", err)
	default:
		var doc Document
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("parse ssh hosts: %w", err)
		}
		if doc.ProjectHostAssociations == nil {
			doc.ProjectHostAssociations = map[string][]string{}
		}
		s.doc = doc
	}
	return s, nil
}

func (s *Store) path() string { return filepath.Join(s.dir, "hosts.json") }

// KnownHosts is the store's known_hosts file.
func (s *Store) KnownHosts() *KnownHosts { return s.known }

// Revision increases with every saved change.
func (s *Store) Revision() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revision
}

// Snapshot returns a copy of the stored document, secrets included.
func (s *Store) Snapshot() Document {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doc.clone()
}

// Host returns one stored host.
func (s *Store) Host(id string) (Host, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range s.doc.Hosts {
		if h.ID == id {
			return h, true
		}
	}
	return Host{}, false
}

// ProjectHosts returns the stored hosts associated with the project whose
// path (or key) is project, in association order.
func (s *Store) ProjectHosts(project string) []Host {
	key := ProjectPathKey(project)
	if key == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Host
	for _, id := range s.doc.ProjectHostAssociations[key] {
		for _, h := range s.doc.Hosts {
			if h.ID == id {
				out = append(out, h)
				break
			}
		}
	}
	return out
}

// HasProjectHosts reports whether project has any associated host.
func (s *Store) HasProjectHosts(project string) bool {
	key := ProjectPathKey(project)
	if key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.doc.ProjectHostAssociations[key]) > 0
}

// ErrConflict is returned when an update names a base revision that is no
// longer current.
var ErrConflict = errors.New("ssh settings changed; reload and retry")

// ErrSave is returned when the hosts file cannot be written.
var ErrSave = errors.New("save ssh hosts")

// ErrNotFound is returned for an unknown host id.
var ErrNotFound = errors.New("ssh host not found")

// Update applies fn to a copy of the document and saves the result. base, if
// non-zero, must be the current revision. fn's error is returned as is.
func (s *Store) Update(base uint64, fn func(*Document) error) (Document, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if base != 0 && base != s.revision {
		return Document{}, s.revision, ErrConflict
	}
	next := s.doc.clone()
	if err := fn(&next); err != nil {
		return Document{}, s.revision, err
	}
	pruneAssociations(&next)
	if err := writeFile(s.dir, s.path(), next); err != nil {
		return Document{}, s.revision, fmt.Errorf("%w: %w", ErrSave, err)
	}
	s.doc = next
	s.revision++
	return next.clone(), s.revision, nil
}

func pruneAssociations(doc *Document) {
	known := map[string]bool{}
	for _, h := range doc.Hosts {
		known[h.ID] = true
	}
	for key, ids := range doc.ProjectHostAssociations {
		ids = slices.DeleteFunc(ids, func(id string) bool { return !known[id] })
		if len(ids) == 0 {
			delete(doc.ProjectHostAssociations, key)
			continue
		}
		doc.ProjectHostAssociations[key] = ids
	}
}

func writeFile(dir, filename string, doc Document) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(dir, filename, append(data, '\n'))
}

// writeAtomic replaces filename with data through a 0600 staging file in dir.
func writeAtomic(dir, filename string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".staging-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filename)
}

// HostInput is a host as a client sends it. Secret fields follow the
// provider API key rules: empty keeps the stored value, a value replaces it,
// and the matching clear flag removes it. The "...Configured", credential and
// hostKeys fields GET returns are accepted and ignored, so a client can send
// back what it read.
type HostInput struct {
	ID                        string      `json:"id"`
	Name                      string      `json:"name"`
	Description               string      `json:"description,omitempty"`
	Host                      string      `json:"host"`
	Port                      PortValue   `json:"port,omitempty"`
	Username                  string      `json:"username,omitempty"`
	AuthType                  string      `json:"authType,omitempty"`
	Password                  string      `json:"password,omitempty"`
	ClearPassword             bool        `json:"clearPassword,omitempty"`
	PrivateKey                string      `json:"privateKey,omitempty"`
	ClearPrivateKey           bool        `json:"clearPrivateKey,omitempty"`
	PrivateKeyPath            string      `json:"privateKeyPath,omitempty"`
	PrivateKeyPassphrase      string      `json:"privateKeyPassphrase,omitempty"`
	ClearPrivateKeyPassphrase bool        `json:"clearPrivateKeyPassphrase,omitempty"`
	Proxy                     *ProxyInput `json:"proxy,omitempty"`

	PasswordConfigured             any `json:"passwordConfigured,omitempty"`
	PrivateKeyConfigured           any `json:"privateKeyConfigured,omitempty"`
	PrivateKeyPassphraseConfigured any `json:"privateKeyPassphraseConfigured,omitempty"`
	Credential                     any `json:"credential,omitempty"`
	HostKeys                       any `json:"hostKeys,omitempty"`
}

// ProxyInput is a host's proxy as a client sends it; Password follows the
// secret rules of HostInput.
type ProxyInput struct {
	Type               string    `json:"type,omitempty"`
	URL                string    `json:"url,omitempty"`
	Port               PortValue `json:"port,omitempty"`
	Username           string    `json:"username,omitempty"`
	Password           string    `json:"password,omitempty"`
	ClearPassword      bool      `json:"clearPassword,omitempty"`
	UseSystemProxy     bool      `json:"useSystemProxy,omitempty"`
	PasswordConfigured any       `json:"passwordConfigured,omitempty"`
}

// PortValue accepts a port as a JSON number, a numeric string, "" or null.
type PortValue int

func (p *PortValue) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if string(data) == "null" {
		*p = 0
		return nil
	}
	var text string
	if json.Unmarshal(data, &text) == nil {
		text = strings.TrimSpace(text)
		if text == "" {
			*p = 0
			return nil
		}
		data = []byte(text)
	}
	n, err := strconv.Atoi(string(data))
	if err != nil {
		return errors.New("port must be an integer")
	}
	*p = PortValue(n)
	return nil
}

// Apply merges in into stored (the zero Host when the id is new) and
// validates the result.
func Apply(stored Host, in HostInput) (Host, error) {
	label := "host"
	if id := strings.TrimSpace(in.ID); id != "" {
		label = "host " + strconv.Quote(id)
	}
	h := Host{
		ID:             strings.TrimSpace(in.ID),
		Name:           strings.TrimSpace(in.Name),
		Description:    strings.TrimSpace(in.Description),
		Host:           strings.TrimSpace(in.Host),
		Port:           int(in.Port),
		Username:       strings.TrimSpace(in.Username),
		AuthType:       strings.TrimSpace(in.AuthType),
		PrivateKeyPath: strings.TrimSpace(in.PrivateKeyPath),
	}
	if h.ID == "" || len(h.ID) > 128 || strings.ContainsAny(h.ID, "/\\?#%") {
		return Host{}, fmt.Errorf("%s: id is required and must not contain / \\ ? # %%", label)
	}
	if h.Name == "" {
		return Host{}, fmt.Errorf("%s: name is required", label)
	}
	if h.Host == "" || strings.ContainsAny(h.Host, " /@\t\r\n") {
		return Host{}, fmt.Errorf("%s: host must be a host name or IP address", label)
	}
	if h.Port == 0 {
		h.Port = 22
	}
	if h.Port < 1 || h.Port > 65535 {
		return Host{}, fmt.Errorf("%s: port must be 1-65535", label)
	}
	if h.AuthType == "" {
		h.AuthType = AuthPassword
	}
	if h.AuthType != AuthPassword && h.AuthType != AuthPrivateKey && h.AuthType != AuthKeyboardInteractive {
		return Host{}, fmt.Errorf("%s: authType must be password, privateKey or keyboardInteractive", label)
	}
	// Secrets carry over only while the auth type stays the same.
	if stored.ID == h.ID && stored.AuthType == h.AuthType {
		h.Password, h.PrivateKey, h.PrivateKeyPassphrase = stored.Password, stored.PrivateKey, stored.PrivateKeyPassphrase
	}
	var err error
	if h.Password, err = applySecret(label+": password", h.Password, in.Password, in.ClearPassword || cleared(in.PasswordConfigured, in.Password)); err != nil {
		return Host{}, err
	}
	if h.PrivateKey, err = applySecret(label+": privateKey", h.PrivateKey, in.PrivateKey, in.ClearPrivateKey || cleared(in.PrivateKeyConfigured, in.PrivateKey)); err != nil {
		return Host{}, err
	}
	if h.PrivateKeyPassphrase, err = applySecret(label+": privateKeyPassphrase", h.PrivateKeyPassphrase, in.PrivateKeyPassphrase, in.ClearPrivateKeyPassphrase || cleared(in.PrivateKeyPassphraseConfigured, in.PrivateKeyPassphrase)); err != nil {
		return Host{}, err
	}
	switch h.AuthType {
	case AuthKeyboardInteractive:
		h.Password, h.PrivateKey, h.PrivateKeyPassphrase, h.PrivateKeyPath = "", "", "", ""
	case AuthPassword:
		h.PrivateKey, h.PrivateKeyPassphrase, h.PrivateKeyPath = "", "", ""
	case AuthPrivateKey:
		h.Password = ""
		if in.PrivateKey != "" {
			if _, err := ssh.ParseRawPrivateKey([]byte(h.PrivateKey)); err != nil {
				var missing *ssh.PassphraseMissingError
				if !errors.As(err, &missing) {
					return Host{}, fmt.Errorf("%s: privateKey is not a supported private key", label)
				}
			}
		}
	}
	var storedProxy Proxy
	if stored.ID == h.ID {
		storedProxy = stored.Proxy
	}
	if h.Proxy, err = applyProxy(label, storedProxy, in.Proxy); err != nil {
		return Host{}, err
	}
	return h, nil
}

// cleared reports a "...Configured": false sent with an empty value: the
// client removed the saved secret (what a settings form does when the user
// clears it), as opposed to leaving the write-only field empty.
func cleared(configured any, value string) bool {
	b, ok := configured.(bool)
	return ok && !b && value == ""
}

func applySecret(label, stored, value string, clear bool) (string, error) {
	if clear && value != "" {
		return "", fmt.Errorf("%s: cannot replace and clear together", label)
	}
	if len(value) > maxSecretBytes {
		return "", fmt.Errorf("%s: too long", label)
	}
	switch {
	case clear:
		return "", nil
	case value != "":
		return value, nil
	}
	return stored, nil
}

func applyProxy(label string, stored Proxy, in *ProxyInput) (Proxy, error) {
	if in == nil {
		return Proxy{Type: "socks5"}, nil
	}
	p := Proxy{
		Type:           strings.TrimSpace(in.Type),
		URL:            strings.TrimSpace(in.URL),
		Port:           int(in.Port),
		Username:       strings.TrimSpace(in.Username),
		UseSystemProxy: in.UseSystemProxy,
	}
	switch p.Type {
	case "", "socks5":
		p.Type = "socks5"
	case "http":
	default:
		return Proxy{}, fmt.Errorf("%s: proxy.type must be socks5 or http", label)
	}
	if p.Port < 0 || p.Port > 65535 {
		return Proxy{}, fmt.Errorf("%s: proxy.port must be 0 or 1-65535", label)
	}
	if p.URL != "" {
		if _, _, err := ParseProxyURL(p.URL, p.Type); err != nil {
			return Proxy{}, fmt.Errorf("%s: proxy.url: %v", label, err)
		}
	}
	var err error
	if p.Password, err = applySecret(label+": proxy.password", stored.Password, in.Password, in.ClearPassword || cleared(in.PasswordConfigured, in.Password)); err != nil {
		return Proxy{}, err
	}
	return p, nil
}

// ParseProxyURL splits a proxy address ("host", "host:port" or
// "scheme://host[:port]") into its kind ("socks5" or "http") and authority.
// Credentials belong in the username and password fields, so an address with
// user info is refused.
func ParseProxyURL(raw, fallbackType string) (kind, authority string, err error) {
	raw = strings.TrimSpace(raw)
	scheme := ""
	if i := strings.Index(raw, "://"); i >= 0 {
		scheme, raw = strings.ToLower(raw[:i]), raw[i+3:]
	}
	if scheme == "" {
		scheme = fallbackType
	}
	switch scheme {
	case "", "socks5", "socks5h", "socks":
		kind = "socks5"
	case "http":
		kind = "http"
	default:
		return "", "", fmt.Errorf("unsupported proxy scheme %q", scheme)
	}
	if i := strings.IndexAny(raw, "/?#"); i >= 0 {
		raw = raw[:i]
	}
	if strings.Contains(raw, "@") {
		return "", "", errors.New("put proxy credentials in username and password, not in the address")
	}
	u, err := url.Parse("x://" + raw)
	if err != nil || u.Hostname() == "" {
		return "", "", errors.New("proxy host is required")
	}
	return kind, raw, nil
}
