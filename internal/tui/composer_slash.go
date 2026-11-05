package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// slashMRUMax is how many recently used slash commands are remembered.
const slashMRUMax = 20

// slashMRU is the list of recently used slash commands, newest first, kept
// in slash_recent.json under the user's data directory.
type slashMRU struct {
	loaded bool
	names  []string
}

var slashMRUPath = func() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "slash_recent.json"), nil
}

func (r *slashMRU) load() {
	if r.loaded {
		return
	}
	r.loaded = true
	p, err := slashMRUPath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var names []string
	if json.Unmarshal(data, &names) == nil {
		r.names = names[:min(len(names), slashMRUMax)]
	}
}

func (r *slashMRU) note(name string) {
	r.load()
	out := []string{name}
	for _, n := range r.names {
		if n != name && len(out) < slashMRUMax {
			out = append(out, n)
		}
	}
	r.names = out
	p, err := slashMRUPath()
	if err != nil {
		return
	}
	data, _ := json.Marshal(out)
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

// rank maps each remembered command to its place, 0 for the newest.
func (r *slashMRU) rank() map[string]int {
	if r == nil {
		return nil
	}
	r.load()
	out := make(map[string]int, len(r.names))
	for i, n := range r.names {
		out[n] = i
	}
	return out
}

func (m *model) slashRecent() *slashMRU {
	c := m.composer()
	if c.mru == nil {
		c.mru = &slashMRU{}
	}
	return c.mru
}

// noteSlashUse remembers the command a submitted line starts with, when it
// is a known command or prompt template.
func (m *model) noteSlashUse(text string) {
	f := strings.Fields(text)
	if len(f) == 0 || !strings.HasPrefix(f[0], "/") {
		return
	}
	name := f[0]
	known := registryFind(name) != nil
	if !known {
		for _, tpl := range m.promptCatalog.items {
			if "/"+tpl.Name == name {
				known = true
				break
			}
		}
	}
	if known {
		m.slashRecent().note(name)
	}
}

// rankCommands filters slash commands against the typed token ("/" and what
// follows) and orders them: names that start with the query, then names with
// a dash-separated part that does, then names that contain it, then names
// that hold its letters in order. Within each group recently used commands
// come first, newest first, then the rest alphabetically. A bare "/" lists
// everything, recent first.
func rankCommands(all []cand, token string, recent map[string]int) []cand {
	q := strings.ToLower(strings.TrimPrefix(token, "/"))
	type hit struct {
		c    cand
		tier int
	}
	var hits []hit
	for _, c := range all {
		name := strings.ToLower(strings.TrimPrefix(c.Text, "/"))
		tier := -1
		switch {
		case strings.HasPrefix(name, q):
			tier = 0
		case segmentPrefix(name, q):
			tier = 1
		case len(q) >= 2 && strings.Contains(name, q):
			tier = 2
		case len(q) >= 3 && subseq(name, q):
			tier = 3
		}
		if tier >= 0 {
			hits = append(hits, hit{c, tier})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].tier != hits[b].tier {
			return hits[a].tier < hits[b].tier
		}
		ra, oka := recent[hits[a].c.Text]
		rb, okb := recent[hits[b].c.Text]
		if oka != okb {
			return oka
		}
		if oka && ra != rb {
			return ra < rb
		}
		la, lb := strings.ToLower(hits[a].c.Text), strings.ToLower(hits[b].c.Text)
		if la != lb {
			return la < lb
		}
		return hits[a].c.Text < hits[b].c.Text
	})
	out := make([]cand, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.c)
	}
	return out
}

// segmentPrefix reports whether a dash- or underscore-separated part of name
// after the first starts with q.
func segmentPrefix(name, q string) bool {
	if q == "" {
		return false
	}
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' })
	for _, p := range parts[min(1, len(parts)):] {
		if strings.HasPrefix(p, q) {
			return true
		}
	}
	return false
}

// argChoices lists the fixed words a command's first argument can be, from
// its registry usage: "[light|dark|auto]" gives light, dark and auto.
// Placeholders such as <text>, [id] or NAME are not choices.
func argChoices(name string) []cand {
	e := registryFind(name)
	if e == nil || e.Args == "" {
		return nil
	}
	group := e.Args
	if strings.HasPrefix(group, "[") {
		if end := strings.IndexByte(group, ']'); end > 0 {
			group = group[1:end]
		}
	} else if i := strings.IndexByte(group, ' '); i >= 0 {
		group = group[:i]
	}
	if !strings.Contains(group, "|") {
		return nil
	}
	var out []cand
	for _, alt := range strings.Split(group, "|") {
		word := strings.Fields(alt)
		if len(word) == 0 || !isChoiceWord(word[0]) {
			continue
		}
		out = append(out, cand{Text: word[0]})
	}
	return out
}

func isChoiceWord(w string) bool {
	lower := false
	for _, r := range w {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r) || unicode.IsDigit(r) || r == '_' || r == '-':
		default:
			return false
		}
	}
	return lower
}
