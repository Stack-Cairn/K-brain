package sshhosts

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "ssh"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func put(t *testing.T, s *Store, in DocumentInput) PublicDocument {
	t.Helper()
	if _, _, err := s.Update(in.BaseRevision, in.ApplyTo); err != nil {
		t.Fatal(err)
	}
	return s.Public()
}

func hostsInput(hosts ...HostInput) *[]HostInput { return &hosts }

func testKeyPEM(t *testing.T) string {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block))
}

func TestSecretsAreWriteOnly(t *testing.T) {
	s := openStore(t)
	key := testKeyPEM(t)
	doc := put(t, s, DocumentInput{Hosts: hostsInput(
		HostInput{ID: "pw", Name: "PW", Host: "db.internal", Port: 2222, Username: "ops", AuthType: AuthPassword, Password: "hunter2",
			Proxy: &ProxyInput{Type: "http", URL: "proxy.internal", Port: 3128, Username: "p", Password: "proxy-secret"}},
		HostInput{ID: "key", Name: "Key", Host: "app.internal", Username: "deploy", AuthType: AuthPrivateKey, PrivateKey: key, PrivateKeyPassphrase: "kp-77-secret"},
		HostInput{ID: "ki", Name: "KI", Host: "mfa.internal", Username: "me", AuthType: AuthKeyboardInteractive, Password: "ignored"},
	)})
	raw, _ := json.Marshal(doc)
	for _, secret := range []string{"hunter2", "proxy-secret", "kp-77-secret", "PRIVATE KEY", "ignored"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("public document leaks %q: %s", secret, raw)
		}
	}
	pw, k, ki := doc.Hosts[0], doc.Hosts[1], doc.Hosts[2]
	if !pw.PasswordConfigured || pw.Password != "" || pw.Credential != CredentialSaved || !pw.Proxy.PasswordConfigured || pw.Proxy.Type != "http" || pw.Port != 2222 {
		t.Fatalf("password host = %+v", pw)
	}
	if !k.PrivateKeyConfigured || !k.PrivateKeyPassphraseConfigured || k.PrivateKey != "" || k.Credential != CredentialSaved || k.Port != 22 {
		t.Fatalf("key host = %+v", k)
	}
	if ki.PasswordConfigured || ki.Credential != CredentialInteractive || ki.HostKeys == nil {
		t.Fatalf("ki host = %+v", ki)
	}

	// Sending back what GET returned keeps every secret.
	var echoed DocumentInput
	if err := json.Unmarshal(raw, &echoed); err != nil {
		t.Fatal(err)
	}
	put(t, s, echoed)
	h, _ := s.Host("pw")
	hk, _ := s.Host("key")
	if h.Password != "hunter2" || h.Proxy.Password != "proxy-secret" || hk.PrivateKey != key || hk.PrivateKeyPassphrase != "kp-77-secret" {
		t.Fatalf("round trip lost secrets: %+v %+v", h, hk)
	}

	// A value replaces; clear flags and "...Configured": false remove.
	put(t, s, DocumentInput{Hosts: hostsInput(
		HostInput{ID: "pw", Name: "PW", Host: "db.internal", Port: 2222, Username: "ops", Password: "new-pw", Proxy: &ProxyInput{Type: "http", URL: "proxy.internal", ClearPassword: true}},
		HostInput{ID: "key", Name: "Key", Host: "app.internal", Username: "deploy", AuthType: AuthPrivateKey, PrivateKeyPassphraseConfigured: false},
	)})
	h, _ = s.Host("pw")
	hk, _ = s.Host("key")
	if h.Password != "new-pw" || h.Proxy.Password != "" || hk.PrivateKey != key || hk.PrivateKeyPassphrase != "" {
		t.Fatalf("replace/clear: %+v %+v", h, hk)
	}
	if _, ok := s.Host("ki"); ok {
		t.Fatal("a host left out of hosts was kept")
	}
	put(t, s, DocumentInput{Hosts: hostsInput(HostInput{ID: "pw", Name: "PW", Host: "db.internal", PasswordConfigured: false})})
	if h, _ = s.Host("pw"); h.Password != "" || h.Credential() != CredentialMissing {
		t.Fatalf("configured=false did not clear: %+v", h)
	}
	if _, _, err := s.Update(0, DocumentInput{Hosts: hostsInput(HostInput{ID: "pw", Name: "PW", Host: "db", Password: "x", ClearPassword: true})}.ApplyTo); err == nil || !strings.Contains(err.Error(), "cannot replace and clear together") {
		t.Fatalf("replace+clear = %v", err)
	}
	// Changing the auth type drops the old secrets.
	put(t, s, DocumentInput{Hosts: hostsInput(HostInput{ID: "pw", Name: "PW", Host: "db", Password: "keep"})})
	put(t, s, DocumentInput{Hosts: hostsInput(HostInput{ID: "pw", Name: "PW", Host: "db", AuthType: AuthPrivateKey, PrivateKeyPath: "~/.keys/id"})})
	if h, _ = s.Host("pw"); h.Password != "" || h.PrivateKeyPath != "~/.keys/id" || h.Credential() != CredentialSaved {
		t.Fatalf("auth switch = %+v", h)
	}

	// The file on disk holds the secrets, private to the user.
	data, err := os.ReadFile(filepath.Join(s.dir, "hosts.json"))
	if err != nil || !strings.Contains(string(data), "~/.keys/id") {
		t.Fatalf("hosts.json = %q %v", data, err)
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{s.dir, filepath.Join(s.dir, "hosts.json")} {
			info, err := os.Stat(p)
			if err != nil || info.Mode().Perm()&0o077 != 0 {
				t.Fatalf("%s mode = %v %v", p, info.Mode(), err)
			}
		}
	}
	// A reopened store reads the file back.
	again, err := Open(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if h2, _ := again.Host("pw"); h2 != h {
		t.Fatalf("reopened = %+v", h2)
	}
}

func TestHostValidation(t *testing.T) {
	s := openStore(t)
	for _, tc := range []struct {
		in   HostInput
		want string
	}{
		{HostInput{Name: "x", Host: "h"}, "id is required"},
		{HostInput{ID: "a/b", Name: "x", Host: "h"}, "id is required"},
		{HostInput{ID: "a", Host: "h"}, "name is required"},
		{HostInput{ID: "a", Name: "x", Host: "user@h"}, "host must be"},
		{HostInput{ID: "a", Name: "x", Host: "h", Port: 70000}, "port must be"},
		{HostInput{ID: "a", Name: "x", Host: "h", AuthType: "kerberos"}, "authType must be"},
		{HostInput{ID: "a", Name: "x", Host: "h", AuthType: AuthPrivateKey, PrivateKey: "not a key"}, "not a supported private key"},
		{HostInput{ID: "a", Name: "x", Host: "h", Proxy: &ProxyInput{Type: "ftp"}}, "proxy.type"},
		{HostInput{ID: "a", Name: "x", Host: "h", Proxy: &ProxyInput{URL: "http://u:p@proxy:1"}}, "proxy credentials"},
		{HostInput{ID: "a", Name: "x", Host: "h", Password: strings.Repeat("x", maxSecretBytes+1)}, "too long"},
	} {
		_, _, err := s.Update(0, DocumentInput{Hosts: hostsInput(tc.in)}.ApplyTo)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%+v: err = %v, want %q", tc.in, err, tc.want)
		}
	}
	_, _, err := s.Update(0, DocumentInput{Hosts: hostsInput(HostInput{ID: "a", Name: "x", Host: "h"}, HostInput{ID: "a", Name: "y", Host: "h"})}.ApplyTo)
	if err == nil || !strings.Contains(err.Error(), "duplicate host id") {
		t.Fatalf("duplicate = %v", err)
	}
	// Ports arrive as numbers or strings.
	var in HostInput
	if err := json.Unmarshal([]byte(`{"id":"a","name":"x","host":"h","port":"2200"}`), &in); err != nil || in.Port != 2200 {
		t.Fatalf("string port = %v %v", in.Port, err)
	}
	if err := json.Unmarshal([]byte(`{"port":"abc"}`), &in); err == nil {
		t.Fatal("non-numeric port accepted")
	}
	if s.Revision() != 1 {
		t.Fatalf("failed updates changed the revision: %d", s.Revision())
	}
}

func TestRevisionsAndAssociations(t *testing.T) {
	s := openStore(t)
	doc := put(t, s, DocumentInput{Hosts: hostsInput(HostInput{ID: "a", Name: "A", Host: "a"}, HostInput{ID: "b", Name: "B", Host: "b"}),
		ProjectHostAssociations: &map[string][]string{"/repo/": {"a", "missing", "a", "b"}, `C:\Work\Repo\`: {"b"}, "": {"a"}}})
	if doc.Revision != 2 {
		t.Fatalf("revision = %d", doc.Revision)
	}
	if got := doc.ProjectHostAssociations; !slices.Equal(got["/repo"], []string{"a", "b"}) || !slices.Equal(got["c:/work/repo"], []string{"b"}) || len(got) != 2 {
		t.Fatalf("associations = %v", got)
	}
	if hosts := s.ProjectHosts("/repo"); len(hosts) != 2 || hosts[0].ID != "a" {
		t.Fatalf("project hosts = %+v", hosts)
	}
	if !s.HasProjectHosts(`c:\work\repo`) || s.HasProjectHosts("/other") || s.HasProjectHosts("") {
		t.Fatal("HasProjectHosts")
	}
	if _, _, err := s.Update(1, func(*Document) error { return nil }); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale base = %v", err)
	}
	put(t, s, DocumentInput{BaseRevision: 2, Hosts: hostsInput(HostInput{ID: "b", Name: "B", Host: "b"})})
	if got := s.Public().ProjectHostAssociations; len(got["/repo"]) != 1 || got["/repo"][0] != "b" {
		t.Fatalf("deleting a host kept its associations: %v", got)
	}
	if _, _, err := s.Update(0, func(d *Document) error { return DeleteHost(d, "zzz") }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing = %v", err)
	}
	if _, _, err := s.Update(0, func(d *Document) error { return DeleteHost(d, "b") }); err != nil {
		t.Fatal(err)
	}
	if got := s.Public(); len(got.Hosts) != 0 || len(got.ProjectHostAssociations) != 0 {
		t.Fatalf("after delete = %+v", got)
	}
}

func TestProjectPathKey(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "",
		"/":                      "/",
		"/repo//":                "/repo",
		`C:\Work\Repo\`:          "c:/work/repo",
		`c:\`:                    "c:/",
		`\\?\C:\Work`:            "c:/work",
		`\\server\share\dir\`:    "//server/share/dir",
		`\\?\UNC\server\share\x`: "//server/share/x",
		" /trim ":                "/trim",
	} {
		if got := ProjectPathKey(in); got != want {
			t.Fatalf("ProjectPathKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKnownHosts(t *testing.T) {
	k := NewKnownHosts(filepath.Join(t.TempDir(), "known_hosts"))
	newKey := func() ssh.PublicKey {
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key, err := ssh.NewPublicKey(pub)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	key, other := newKey(), newKey()
	if status, _, err := k.Check("example.test:22", key); status != HostKeyUnknown || err != nil {
		t.Fatalf("empty file = %v %v", status, err)
	}
	if err := k.Add("example.test:22", key); err != nil {
		t.Fatal(err)
	}
	if err := k.Add("10.0.0.5:2222", other); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := k.Check("example.test:22", key); status != HostKeyTrusted {
		t.Fatalf("trusted = %v", status)
	}
	status, known, _ := k.Check("example.test:22", other)
	if status != HostKeyChanged || len(known) != 1 || known[0].Fingerprint != ssh.FingerprintSHA256(key) {
		t.Fatalf("changed = %v %+v", status, known)
	}
	if status, _, _ := k.Check("example.test:2200", key); status != HostKeyUnknown {
		t.Fatalf("other port = %v", status)
	}
	if algos := k.Algorithms("example.test:22"); !slices.Equal(algos, []string{ssh.KeyAlgoED25519}) {
		t.Fatalf("algorithms = %v", algos)
	}
	if k.Algorithms("nowhere:22") != nil {
		t.Fatal("algorithms for an unknown host")
	}
	if keys := k.Keys("10.0.0.5:2222"); len(keys) != 1 || keys[0].Algorithm != ssh.KeyAlgoED25519 {
		t.Fatalf("keys = %+v", keys)
	}
	if n, err := k.Remove("example.test:22"); n != 1 || err != nil {
		t.Fatalf("remove = %d %v", n, err)
	}
	if status, _, _ := k.Check("example.test:22", other); status != HostKeyUnknown {
		t.Fatalf("after remove = %v", status)
	}
	if len(k.Keys("10.0.0.5:2222")) != 1 {
		t.Fatal("remove dropped another host")
	}
	if n, err := k.Remove("example.test:22"); n != 0 || err != nil {
		t.Fatalf("second remove = %d %v", n, err)
	}
}

func TestPublicHostKeysAndProxyParsing(t *testing.T) {
	s := openStore(t)
	put(t, s, DocumentInput{Hosts: hostsInput(HostInput{ID: "a", Name: "A", Host: "::1", Port: 2022, Username: "u"})})
	h, _ := s.Host("a")
	if h.Address() != "[::1]:2022" || h.Endpoint() != "u@[::1]:2022" {
		t.Fatalf("address = %q", h.Address())
	}
	for _, tc := range []struct{ raw, kind, authority string }{
		{"proxy:1080", "socks5", "proxy:1080"},
		{"http://proxy:3128/path", "http", "proxy:3128"},
		{"socks5h://proxy", "socks5", "proxy"},
	} {
		kind, authority, err := ParseProxyURL(tc.raw, "")
		if err != nil || kind != tc.kind || authority != tc.authority {
			t.Fatalf("ParseProxyURL(%q) = %q %q %v", tc.raw, kind, authority, err)
		}
	}
	if _, _, err := ParseProxyURL("ftp://x", ""); err == nil {
		t.Fatal("ftp proxy accepted")
	}
}
