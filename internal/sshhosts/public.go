package sshhosts

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// PublicHost is a host as GET /v1/settings/ssh returns it: LiveAgent's
// SshHostConfig shape with every secret blanked and reported through its
// "...Configured" flag, plus credential and the stored host keys.
type PublicHost struct {
	ID                             string      `json:"id"`
	Name                           string      `json:"name"`
	Description                    string      `json:"description"`
	Host                           string      `json:"host"`
	Port                           int         `json:"port"`
	Username                       string      `json:"username"`
	AuthType                       string      `json:"authType"`
	Password                       string      `json:"password"`
	PasswordConfigured             bool        `json:"passwordConfigured"`
	PrivateKey                     string      `json:"privateKey"`
	PrivateKeyPath                 string      `json:"privateKeyPath"`
	PrivateKeyConfigured           bool        `json:"privateKeyConfigured"`
	PrivateKeyPassphrase           string      `json:"privateKeyPassphrase"`
	PrivateKeyPassphraseConfigured bool        `json:"privateKeyPassphraseConfigured"`
	Proxy                          PublicProxy `json:"proxy"`
	Credential                     string      `json:"credential"`
	HostKeys                       []KnownKey  `json:"hostKeys"`
}

// PublicProxy is a host's proxy with its password blanked.
type PublicProxy struct {
	Type               string `json:"type"`
	URL                string `json:"url"`
	Port               int    `json:"port"`
	Username           string `json:"username"`
	Password           string `json:"password"`
	PasswordConfigured bool   `json:"passwordConfigured"`
	UseSystemProxy     bool   `json:"useSystemProxy"`
}

// PublicDocument is the body of GET /v1/settings/ssh.
type PublicDocument struct {
	Revision                uint64              `json:"revision"`
	Hosts                   []PublicHost        `json:"hosts"`
	ProjectHostAssociations map[string][]string `json:"projectHostAssociations"`
	KnownHostsPath          string              `json:"knownHostsPath"`
}

// Public projects h for clients.
func (k *KnownHosts) Public(h Host) PublicHost {
	keys := k.Keys(h.Address())
	if keys == nil {
		keys = []KnownKey{}
	}
	proxyType := h.Proxy.Type
	if proxyType == "" {
		proxyType = "socks5"
	}
	return PublicHost{
		ID: h.ID, Name: h.Name, Description: h.Description, Host: h.Host, Port: h.Port, Username: h.Username, AuthType: h.AuthType,
		PasswordConfigured:             h.Password != "",
		PrivateKeyPath:                 h.PrivateKeyPath,
		PrivateKeyConfigured:           h.PrivateKey != "" || h.PrivateKeyPath != "",
		PrivateKeyPassphraseConfigured: h.PrivateKeyPassphrase != "",
		Proxy: PublicProxy{Type: proxyType, URL: h.Proxy.URL, Port: h.Proxy.Port, Username: h.Proxy.Username,
			PasswordConfigured: h.Proxy.Password != "", UseSystemProxy: h.Proxy.UseSystemProxy},
		Credential: h.Credential(),
		HostKeys:   keys,
	}
}

// Public projects the whole store for clients.
func (s *Store) Public() PublicDocument {
	s.mu.Lock()
	doc, revision := s.doc.clone(), s.revision
	s.mu.Unlock()
	out := PublicDocument{Revision: revision, Hosts: make([]PublicHost, 0, len(doc.Hosts)), ProjectHostAssociations: doc.ProjectHostAssociations, KnownHostsPath: s.known.Path()}
	for _, h := range doc.Hosts {
		out.Hosts = append(out.Hosts, s.known.Public(h))
	}
	return out
}

// NormalizeAssociations keys associations by ProjectPathKey, drops ids of
// hosts not in hosts, duplicates and empty lists, and keeps at most
// MaxHostsPerProject ids per project. When two spellings of one project
// collide, the one already in canonical form wins (LiveAgent's rule).
func NormalizeAssociations(in map[string][]string, hosts []Host) map[string][]string {
	known := map[string]bool{}
	for _, h := range hosts {
		known[h.ID] = true
	}
	raw := make([]string, 0, len(in))
	for key := range in {
		raw = append(raw, key)
	}
	sort.Strings(raw)
	out := map[string][]string{}
	canonical := map[string]bool{}
	for _, rawKey := range raw {
		key := ProjectPathKey(rawKey)
		if key == "" {
			continue
		}
		seen := map[string]bool{}
		var ids []string
		for _, id := range in[rawKey] {
			if id == "" || !known[id] || seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
			if len(ids) == MaxHostsPerProject {
				break
			}
		}
		if len(ids) == 0 {
			continue
		}
		isCanonical := rawKey == key
		if isCanonical || !canonical[key] {
			out[key] = ids
		}
		if isCanonical {
			canonical[key] = true
		}
	}
	return out
}

// DocumentInput is the body of PUT /v1/settings/ssh. A field that is absent
// keeps what is stored: Hosts, when present, is the complete host list (hosts
// left out are deleted, secrets of the others follow HostInput's rules);
// ProjectHostAssociations, when present, replaces every association.
// BaseRevision, when non-zero, must be the current revision. Revision and
// knownHostsPath from GET are accepted and ignored.
type DocumentInput struct {
	BaseRevision            uint64               `json:"baseRevision,omitempty"`
	Hosts                   *[]HostInput         `json:"hosts,omitempty"`
	ProjectHostAssociations *map[string][]string `json:"projectHostAssociations,omitempty"`
	Revision                any                  `json:"revision,omitempty"`
	KnownHostsPath          any                  `json:"knownHostsPath,omitempty"`
}

// ApplyTo merges in into doc.
func (in DocumentInput) ApplyTo(doc *Document) error {
	if in.Hosts != nil {
		if len(*in.Hosts) > MaxHosts {
			return fmt.Errorf("at most %d SSH hosts can be stored", MaxHosts)
		}
		stored := map[string]Host{}
		for _, h := range doc.Hosts {
			stored[h.ID] = h
		}
		next := make([]Host, 0, len(*in.Hosts))
		seen := map[string]bool{}
		for _, input := range *in.Hosts {
			id := strings.TrimSpace(input.ID)
			if seen[id] {
				return fmt.Errorf("duplicate host id %q", id)
			}
			seen[id] = true
			h, err := Apply(stored[id], input)
			if err != nil {
				return err
			}
			next = append(next, h)
		}
		doc.Hosts = next
	}
	if in.ProjectHostAssociations != nil {
		doc.ProjectHostAssociations = NormalizeAssociations(*in.ProjectHostAssociations, doc.Hosts)
	}
	return nil
}

// PutHost adds or replaces the host in.ID.
func PutHost(doc *Document, in HostInput) (Host, error) {
	id := strings.TrimSpace(in.ID)
	for i, stored := range doc.Hosts {
		if stored.ID == id {
			h, err := Apply(stored, in)
			if err != nil {
				return Host{}, err
			}
			doc.Hosts[i] = h
			return h, nil
		}
	}
	if len(doc.Hosts) >= MaxHosts {
		return Host{}, fmt.Errorf("at most %d SSH hosts can be stored", MaxHosts)
	}
	h, err := Apply(Host{}, in)
	if err != nil {
		return Host{}, err
	}
	doc.Hosts = append(doc.Hosts, h)
	return h, nil
}

// DeleteHost removes the host id and its associations.
func DeleteHost(doc *Document, id string) error {
	n := len(doc.Hosts)
	doc.Hosts = slices.DeleteFunc(doc.Hosts, func(h Host) bool { return h.ID == id })
	if len(doc.Hosts) == n {
		return ErrNotFound
	}
	return nil
}
