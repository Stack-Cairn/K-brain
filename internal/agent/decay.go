package agent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

const (
	decayHotWindow = 24_000

	decayMinBytes = 8_000
)

const decayedMarker = "⟨"

func (a *Agent) decay() int {
	a.msgsMu.Lock()
	defer a.msgsMu.Unlock()

	boundary := hotBoundary(a.Messages)
	rewritten := 0

	type readKey struct {
		path, args string
	}
	seen := map[readKey]int{}
	for i := range boundary {
		m := &a.Messages[i]
		if m.Role != "tool" || m.Name != "read" || strings.HasPrefix(m.Content, decayedMarker) {
			continue
		}
		k := readKey{readPathFromCall(a.Messages, i), callArgs(a.Messages, i)}
		if k.path == "" {
			continue
		}
		first, dup := seen[k]
		if !dup {
			seen[k] = i
			continue
		}
		if a.Messages[first].Content == m.Content {
			m.Content = fmt.Sprintf("%sduplicate read of %s — same content as the first read above⟩",
				decayedMarker, filepath.Base(k.path))
			rewritten++
		} else {
			seen[k] = i
		}
	}

	latest := map[string]sighting{}
	type readRef struct {
		idx  int
		path string
	}
	var reads []readRef

	for i := len(a.Messages) - 1; i > 0; i-- {
		m := a.Messages[i]
		if m.Role != "tool" {
			continue
		}
		switch m.Name {
		case "read":
			p := readPathFromCall(a.Messages, i)
			if p == "" || strings.HasPrefix(m.Content, decayedMarker) {
				continue
			}
			reads = append(reads, readRef{i, p})
			if _, ok := latest[p]; !ok {
				latest[p] = sighting{idx: i, lines: readLineCount(m.Content)}
			}
		case "write", "edit":
			p := writePathFromCall(a.Messages, i)
			if p == "" {
				continue
			}
			if _, ok := latest[p]; !ok {
				latest[p] = sighting{idx: i, write: true}
			}
		}
	}

	for _, r := range reads {
		s := latest[r.path]
		if s.idx == r.idx || r.idx >= boundary {
			continue
		}
		a.Messages[r.idx].Content = supersededNotice(r.path, s)
		rewritten++
	}

	turns := 0
	for i := len(a.Messages) - 1; i >= 0; i-- {
		m := &a.Messages[i]
		if m.Role == "user" && m.Authored {
			turns++
		}
		if i >= boundary {
			continue
		}
		if m.Role != "tool" || len(m.Content) <= decayMinBytes || strings.HasPrefix(m.Content, decayedMarker) {
			continue
		}
		m.Content = decayNotice(a.Messages, i, turns)
		rewritten++
	}

	for i := boundary - 1; i > 0; i-- {
		rewritten += stripImageParts(&a.Messages[i])
	}
	return rewritten
}

func stripImageParts(m *ai.Message) int {
	stripped := 0
	var kept []ai.ContentPart
	var notes []string
	for _, p := range m.Parts {
		if p.Type != "image_url" || p.ImageURL == nil {
			kept = append(kept, p)
			continue
		}
		path := spillImage(p.ImageURL.URL)
		size := "image"
		if p.W > 0 {
			size = fmt.Sprintf("%d×%d image", p.W, p.H)
		}
		note := decayedMarker + size + " omitted"
		if path != "" {
			note += " — bytes at " + path + " (re-attach with @" + path + " if needed)"
		}
		note += "⟩"
		notes = append(notes, note)
		stripped++
	}
	if stripped == 0 {
		return 0
	}

	texts := []string{}
	if m.Content != "" {
		texts = append(texts, m.Content)
	}
	for _, p := range kept {
		if p.Type == "text" && p.Text != "" && p.Text != m.Content {
			texts = append(texts, p.Text)
		}
	}
	m.Content = strings.Join(append(texts, notes...), "\n")
	m.Parts = nil
	return stripped
}

func spillImage(dataURL string) string {
	const prefix = ";base64,"
	i := strings.Index(dataURL, prefix)
	if !strings.HasPrefix(dataURL, "data:") || i < len("data:") {
		return ""
	}
	mime := dataURL[len("data:"):i]
	ext := "png"
	switch mime {
	case "image/jpeg":
		ext = "jpg"
	case "image/gif":
		ext = "gif"
	case "image/webp":
		ext = "webp"
	case "image/bmp":
		ext = "bmp"
	}
	raw, err := base64.StdEncoding.DecodeString(dataURL[i+len(prefix):])
	if err != nil {
		return ""
	}
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("k-brain-img-%d", os.Getpid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	f, err := os.CreateTemp(dir, "*."+ext)
	if err != nil {
		return ""
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return ""
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return ""
	}
	return f.Name()
}

func hotBoundary(msgs []ai.Message) int {
	budget := decayHotWindow
	for i := len(msgs) - 1; i > 0; i-- {
		t := msgTokens(msgs[i])
		if t > budget {
			return i
		}
		budget -= t
	}
	return 1
}

func msgTokens(m ai.Message) int {
	n := len(m.Content) + len(m.ToolCallID) + len(m.Name)
	for _, tc := range m.ToolCalls {
		n += len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	t := n / 4
	for _, p := range m.Parts {

		t += ai.PartTokens(p)
	}
	return t
}

func readPathFromCall(msgs []ai.Message, i int) string {
	return toolArgFromCall(msgs, i, "path")
}

func writePathFromCall(msgs []ai.Message, i int) string {
	return toolArgFromCall(msgs, i, "path")
}

func toolArgFromCall(msgs []ai.Message, i int, key string) string {
	id := msgs[i].ToolCallID
	if id == "" {
		return ""
	}
	for j := i - 1; j >= 0; j-- {
		if msgs[j].Role != "assistant" {
			continue
		}
		for _, tc := range msgs[j].ToolCalls {
			if tc.ID != id {
				continue
			}
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				return ""
			}
			if v, ok := args[key].(string); ok {
				return v
			}
			return ""
		}
		return ""
	}
	return ""
}

func callArgs(msgs []ai.Message, i int) string {
	id := msgs[i].ToolCallID
	if id == "" {
		return ""
	}
	for j := i - 1; j >= 0; j-- {
		if msgs[j].Role != "assistant" {
			continue
		}
		for _, tc := range msgs[j].ToolCalls {
			if tc.ID == id {
				return tc.Function.Arguments
			}
		}
		return ""
	}
	return ""
}

func readLineCount(content string) int {
	n := 0
	for l := range strings.Lines(content) {
		if l != "" {
			n++
		}
	}
	return n
}

type sighting struct {
	idx   int
	write bool
	lines int
}

func supersededNotice(path string, s sighting) string {
	if s.write {
		return fmt.Sprintf("%sread of %s superseded — file changed by a later write/edit⟩", decayedMarker, filepath.Base(path))
	}
	return fmt.Sprintf("%sread of %s superseded by newer read (%d lines)⟩", decayedMarker, filepath.Base(path), s.lines)
}

func decayNotice(msgs []ai.Message, i, turnsAgo int) string {
	m := msgs[i]
	what := m.Name
	switch m.Name {
	case "bash":
		if cmd := toolArgFromCall(msgs, i, "command"); cmd != "" {
			what = fmt.Sprintf("bash %q", firstWords(cmd, 60))
		}
	case "read", "write", "edit":
		if p := toolArgFromCall(msgs, i, "path"); p != "" {
			what = fmt.Sprintf("%s %s", m.Name, filepath.Base(p))
		}
	}
	spill := spillPathOf(m.Content)
	if spill == "" {
		spill = bashrun.Spill(m.Content)
	}
	age := ""
	if turnsAgo > 0 {
		age = fmt.Sprintf(" — ran here %d turn(s) ago", turnsAgo)
	}
	size := fmt.Sprintf("%dk bytes", len(m.Content)/1024)
	if spill != "" {
		return fmt.Sprintf("%s%s output, %s%s; full output: %s⟩", decayedMarker, what, size, age, spill)
	}
	return fmt.Sprintf("%s%s output, %s%s⟩", decayedMarker, what, size, age)
}

func firstWords(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func spillPathOf(content string) string {
	i := strings.LastIndex(content, "full output (")
	if i < 0 {
		return ""
	}
	j := strings.Index(content[i:], "): ")
	if j < 0 {
		return ""
	}
	start := i + j + 3

	end := strings.IndexAny(content[start:], "]\n")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(content[start : start+end])
}
