package sshhosts

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// HostKeyStatus is how a presented host key compares with known_hosts.
type HostKeyStatus int

const (
	// HostKeyTrusted: known_hosts has this key for the host.
	HostKeyTrusted HostKeyStatus = iota
	// HostKeyUnknown: known_hosts has no key for the host.
	HostKeyUnknown
	// HostKeyChanged: known_hosts has other keys for the host, or the key is
	// revoked.
	HostKeyChanged
)

// KnownKey describes a stored host key without its bytes.
type KnownKey struct {
	Algorithm   string `json:"algorithm"`
	Fingerprint string `json:"fingerprint"`
}

// Describe returns the algorithm and SHA256 fingerprint of key.
func Describe(key ssh.PublicKey) KnownKey {
	return KnownKey{Algorithm: key.Type(), Fingerprint: ssh.FingerprintSHA256(key)}
}

// KnownHosts is K-brain's own known_hosts file (OpenSSH format). It is never
// the user's ~/.ssh/known_hosts: K-brain trusts only keys the user accepted
// through K-brain.
type KnownHosts struct {
	path string
	mu   sync.Mutex
}

// NewKnownHosts uses the file at path.
func NewKnownHosts(path string) *KnownHosts { return &KnownHosts{path: path} }

// Path is the file's location.
func (k *KnownHosts) Path() string { return k.path }

// Check compares key, presented by address (host:port), with the file. For
// HostKeyChanged, known lists the keys stored for the host.
func (k *KnownHosts) Check(address string, key ssh.PublicKey) (HostKeyStatus, []KnownKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, err := os.Stat(k.path); errors.Is(err, os.ErrNotExist) {
		return HostKeyUnknown, nil, nil
	}
	callback, err := knownhosts.New(k.path)
	if err != nil {
		return HostKeyChanged, nil, fmt.Errorf("read known_hosts: %w", err)
	}
	// The remote address is the TCP peer, which is the proxy when one is
	// used; known_hosts is keyed by the configured host, so pass that.
	remote, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		return HostKeyChanged, nil, err
	}
	err = callback(address, remote, key)
	if err == nil {
		return HostKeyTrusted, nil, nil
	}
	var keyErr *knownhosts.KeyError
	if errors.As(err, &keyErr) {
		if len(keyErr.Want) == 0 {
			return HostKeyUnknown, nil, nil
		}
		known := make([]KnownKey, len(keyErr.Want))
		for i, want := range keyErr.Want {
			known[i] = Describe(want.Key)
		}
		return HostKeyChanged, known, nil
	}
	var revoked *knownhosts.RevokedError
	if errors.As(err, &revoked) {
		return HostKeyChanged, nil, nil
	}
	return HostKeyChanged, nil, err
}

// Add trusts key for address.
func (k *KnownHosts) Add(address string, key ssh.PublicKey) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	data, err := os.ReadFile(k.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		data = append(data, '\n')
	}
	data = append(data, knownhosts.Line([]string{knownhosts.Normalize(address)}, key)+"\n"...)
	return writeAtomic(filepath.Dir(k.path), k.path, data)
}

// Keys lists the keys stored for address.
func (k *KnownHosts) Keys(address string) []KnownKey {
	var out []KnownKey
	k.each(address, func(key ssh.PublicKey) { out = append(out, Describe(key)) })
	return out
}

// Algorithms lists the host key algorithms to offer for address so the
// server presents a key of a stored type; nil when none is stored.
func (k *KnownHosts) Algorithms(address string) []string {
	var out []string
	k.each(address, func(key ssh.PublicKey) {
		algos := []string{key.Type()}
		if key.Type() == ssh.KeyAlgoRSA {
			algos = []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
		}
		for _, a := range algos {
			if !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	})
	return out
}

// Remove forgets every key stored for address, so the next connection asks
// again. It is how a user re-trusts a host whose key changed.
func (k *KnownHosts) Remove(address string) (int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	data, err := os.ReadFile(k.path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	want := knownhosts.Normalize(address)
	var out bytes.Buffer
	removed := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if lineHosts(line, want) {
			removed++
			continue
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	if removed == 0 {
		return 0, nil
	}
	return removed, writeAtomic(filepath.Dir(k.path), k.path, out.Bytes())
}

// lineHosts reports whether a known_hosts line names want among its
// (unhashed) host patterns.
func lineHosts(line, want string) bool {
	fields := strings.Fields(line)
	if len(fields) < 3 || strings.HasPrefix(fields[0], "#") {
		return false
	}
	hosts := fields[0]
	if strings.HasPrefix(hosts, "@") {
		if len(fields) < 4 {
			return false
		}
		hosts = fields[1]
	}
	return slices.Contains(strings.Split(hosts, ","), want)
}

func (k *KnownHosts) each(address string, fn func(ssh.PublicKey)) {
	k.mu.Lock()
	data, err := os.ReadFile(k.path)
	k.mu.Unlock()
	if err != nil {
		return
	}
	want := knownhosts.Normalize(address)
	rest := data
	for len(rest) > 0 {
		marker, hosts, key, _, next, err := ssh.ParseKnownHosts(rest)
		if err != nil {
			return
		}
		rest = next
		if marker == "" && slices.Contains(hosts, want) {
			fn(key)
		}
	}
}
