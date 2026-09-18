package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

const (
	maxEntries = 50

	maxEntryLength = 2000
)

type Entry struct {
	N    int
	Text string
	Done bool
}

type Scope struct {
	Path string
	Name string
}

func Installation() Scope {
	dir, err := config.Dir()
	if err != nil {
		return Scope{}
	}
	return Scope{Path: filepath.Join(dir, "memory.md"), Name: "installation"}
}

func Session(id string) Scope {
	if id == "" {
		return Scope{}
	}
	dir, err := config.Dir()
	if err != nil {
		return Scope{}
	}
	return Scope{Path: filepath.Join(dir, "sessions", id+".memory.md"), Name: "session"}
}

func (s Scope) Entries() []Entry {
	if s.Path == "" {
		return nil
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return nil
	}
	var out []Entry
	for line := range strings.Lines(string(data)) {
		line = strings.TrimRight(line, "\n")
		rest, ok := strings.CutPrefix(line, "- [ ] ")
		done := false
		if !ok {
			if rest, ok = strings.CutPrefix(line, "- [x] "); !ok {
				continue
			}
			done = true
		}
		out = append(out, Entry{N: len(out) + 1, Text: rest, Done: done})
	}
	return out
}

func (s Scope) Remember(text string) error {
	if s.Path == "" {
		return errors.New("no memory scope for this session yet")
	}
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return errors.New("text is required")
	}
	if len(text) > maxEntryLength {
		return fmt.Errorf("keep it under %d chars; summarize it first", maxEntryLength)
	}
	open := 0
	for _, e := range s.Entries() {
		if !e.Done {
			open++
		}
	}
	if open >= maxEntries {
		return fmt.Errorf("memory is full (%d entries); forget something stale first", maxEntries)
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(s.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := fmt.Fprintf(f, "- [ ] %s\n", text)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

func (s Scope) Forget(n int) error {
	if s.Path == "" {
		return errors.New("no memory scope for this session yet")
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return errors.New("no memories yet")
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	seen := 0
	for i, line := range lines {
		if strings.HasPrefix(line, "- [ ] ") || strings.HasPrefix(line, "- [x] ") {
			seen++
			if seen == n {
				if strings.HasPrefix(line, "- [x] ") {
					return fmt.Errorf("entry %d is already marked done", n)
				}
				lines[i] = "- [x] " + strings.TrimPrefix(line, "- [ ] ")
				return os.WriteFile(s.Path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
			}
		}
	}
	return fmt.Errorf("no memory entry %d", n)
}

func PromptBlock(scopes ...Scope) string {
	var b strings.Builder
	for _, s := range scopes {
		var lines []string
		for _, e := range s.Entries() {
			if !e.Done {
				lines = append(lines, fmt.Sprintf("- %d. %s", e.N, e.Text))
			}
		}
		if len(lines) > 0 {
			fmt.Fprintf(&b, "\n\nSaved %s memory (%s — edit or delete lines there directly; forget marks an entry done):\n%s",
				s.Name, s.Path, strings.Join(lines, "\n"))
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "\n\n<memory>" + b.String() + "\n</memory>"
}
