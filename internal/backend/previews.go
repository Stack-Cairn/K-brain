package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// HTML previews: with options.html_previews a run tells the model it may
// answer with a fenced block whose info string is "html preview", and keeps
// each such block, and each .html file the Write tool writes, as a preview
// served to a sandboxed frame.

const (
	// previewInfoString is the fenced block info string that marks a preview.
	previewInfoString = "html preview"
	maxPreviewBytes   = 2 << 20
	maxPreviewsPerRun = 16
	previewIDLength   = 16
)

// htmlPreviewHint is appended to the system prompt of runs that ask for
// previews.
const htmlPreviewHint = "\n\n## HTML previews\n" +
	"The user's app can render a self-contained HTML document as a visual preview card. When seeing a result helps more than reading code " +
	"(a chart, a UI mockup, a diagram, a styled report), put one complete HTML document in a fenced code block whose info string is exactly `html preview`:\n\n" +
	"```html preview\n<!doctype html>\n<html><head><title>Short title</title><style>/* inline CSS */</style></head><body>...</body></html>\n```\n\n" +
	"The preview runs in a sandbox without network access: inline all CSS and JavaScript, embed images as data: URIs, and stay under 2 MB. " +
	"An .html file you write with the Write tool is shown as a preview too. Use a plain ```html block for code the user should read rather than see rendered."

var previewIDPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)
var titleTag = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// previewFenceScanner finds "html preview" fenced blocks in streamed text.
type previewFenceScanner struct {
	line    strings.Builder
	inside  bool
	fence   byte
	length  int
	body    strings.Builder
	tooBig  bool
	pending []string
}

// write consumes a text delta; complete blocks are collected in pending.
func (f *previewFenceScanner) write(delta string) {
	for delta != "" {
		i := strings.IndexByte(delta, '\n')
		if i < 0 {
			f.line.WriteString(delta)
			return
		}
		f.line.WriteString(delta[:i])
		delta = delta[i+1:]
		f.endLine()
	}
}

// flush ends the current message: a last line without a newline may close a
// block; an unclosed block is dropped.
func (f *previewFenceScanner) flush() {
	if f.line.Len() > 0 {
		f.endLine()
	}
	f.inside, f.tooBig = false, false
	f.body.Reset()
}

func (f *previewFenceScanner) endLine() {
	line := strings.TrimSuffix(f.line.String(), "\r")
	f.line.Reset()
	char, length, info, ok := fenceLine(line)
	if !f.inside {
		if ok && strings.Join(strings.Fields(info), " ") == previewInfoString {
			f.inside, f.fence, f.length, f.tooBig = true, char, length, false
			f.body.Reset()
		}
		return
	}
	if ok && char == f.fence && length >= f.length && strings.TrimSpace(info) == "" {
		if !f.tooBig && strings.TrimSpace(f.body.String()) != "" {
			f.pending = append(f.pending, f.body.String())
		}
		f.inside = false
		f.body.Reset()
		return
	}
	if f.tooBig {
		return
	}
	if f.body.Len()+len(line)+1 > maxPreviewBytes {
		f.tooBig = true
		f.body.Reset()
		return
	}
	f.body.WriteString(line)
	f.body.WriteByte('\n')
}

// fenceLine parses a code fence line: up to three spaces of indentation, at
// least three backticks or tildes, then the info string.
func fenceLine(line string) (char byte, length int, info string, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return 0, 0, "", false
	}
	char = trimmed[0]
	for length < len(trimmed) && trimmed[length] == char {
		length++
	}
	if length < 3 {
		return 0, 0, "", false
	}
	info = strings.TrimSpace(trimmed[length:])
	if char == '`' && strings.Contains(info, "`") {
		return 0, 0, "", false
	}
	return char, length, info, true
}

// previewTracker collects a run's previews from its text and tool calls.
type previewTracker struct {
	s     *Server
	rt    *runtimeSession
	mu    sync.Mutex
	text  previewFenceScanner
	files map[string]string // tool call id -> path, for pending .html writes
	data  map[string]string // tool call id -> content
	count int
}

func newPreviewTracker(s *Server, rt *runtimeSession) *previewTracker {
	return &previewTracker{s: s, rt: rt, files: map[string]string{}, data: map[string]string{}}
}

func (p *previewTracker) onText(delta string) {
	p.mu.Lock()
	p.text.write(delta)
	blocks := p.takeLocked()
	p.mu.Unlock()
	for _, block := range blocks {
		p.s.savePreview(p.rt, protocol.PreviewCreated{Source: "message"}, block)
	}
}

// endMessage closes the assistant text that preceded a tool call or the end
// of the run.
func (p *previewTracker) endMessage() {
	p.mu.Lock()
	p.text.flush()
	blocks := p.takeLocked()
	p.mu.Unlock()
	for _, block := range blocks {
		p.s.savePreview(p.rt, protocol.PreviewCreated{Source: "message"}, block)
	}
}

func (p *previewTracker) takeLocked() []string {
	blocks := p.text.pending
	p.text.pending = nil
	if room := maxPreviewsPerRun - p.count; len(blocks) > room {
		blocks = blocks[:max(room, 0)]
	}
	p.count += len(blocks)
	return blocks
}

func (p *previewTracker) onToolStart(id, name, args string) {
	p.endMessage()
	if name != "Write" && name != "write" {
		return
	}
	var a struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	if json.Unmarshal([]byte(args), &a) != nil {
		return
	}
	target := a.Path
	if target == "" {
		target = a.FilePath
	}
	ext := strings.ToLower(path.Ext(filepath.ToSlash(target)))
	if (ext != ".html" && ext != ".htm") || strings.TrimSpace(a.Content) == "" || len(a.Content) > maxPreviewBytes {
		return
	}
	p.mu.Lock()
	p.files[id], p.data[id] = target, a.Content
	p.mu.Unlock()
}

func (p *previewTracker) onToolResult(id string, result tools.Result) {
	p.mu.Lock()
	target, content, ok := p.files[id], p.data[id], false
	if target != "" {
		delete(p.files, id)
		delete(p.data, id)
		ok = !result.Failed && !result.Cancelled && p.count < maxPreviewsPerRun
		if ok {
			p.count++
		}
	}
	p.mu.Unlock()
	if ok {
		p.s.savePreview(p.rt, protocol.PreviewCreated{Source: "file", Path: target, ToolCallID: id}, content)
	}
}

func (s *Server) previewDir(sessionID string) string {
	if s.eventDir == "" {
		return ""
	}
	return filepath.Join(s.eventDir, "previews", sessionID)
}

// savePreview stores htmlText under its content hash and publishes
// preview.created.
func (s *Server) savePreview(rt *runtimeSession, preview protocol.PreviewCreated, htmlText string) {
	dir := s.previewDir(rt.id)
	if dir == "" || len(htmlText) > maxPreviewBytes {
		return
	}
	sum := sha256.Sum256([]byte(htmlText))
	preview.PreviewID = hex.EncodeToString(sum[:])[:previewIDLength]
	preview.Bytes = len(htmlText)
	preview.URL = "/v1/sessions/" + url.PathEscape(rt.id) + "/previews/" + preview.PreviewID
	preview.CreatedAt = time.Now().UTC()
	preview.Title = previewTitle(htmlText, preview.Path)
	meta, err := json.Marshal(preview)
	if err != nil {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	base := filepath.Join(dir, preview.PreviewID)
	if writePreviewFile(base+".html", []byte(htmlText)) != nil || writePreviewFile(base+".json", meta) != nil {
		return
	}
	_, _ = rt.publish(protocol.EventPreviewCreated, preview, "")
}

func writePreviewFile(name string, data []byte) error {
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}

func previewTitle(htmlText, written string) string {
	title := ""
	if m := titleTag.FindStringSubmatch(htmlText); m != nil {
		title = strings.Join(strings.Fields(html.UnescapeString(m[1])), " ")
	}
	if title == "" && written != "" {
		title = path.Base(filepath.ToSlash(written))
	}
	if r := []rune(title); len(r) > 120 {
		title = string(r[:120])
	}
	return title
}

// handlePreviews serves GET /v1/sessions/{id}/previews (the session's
// previews, oldest first) and GET /v1/sessions/{id}/previews/{previewId}
// (one preview's HTML for a sandboxed frame).
func (s *Server) handlePreviews(w http.ResponseWriter, r *http.Request, id string, rest []string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, _, err := s.store.Load(id); err != nil {
		writeJSONError(w, http.StatusNotFound, "session not found")
		return
	}
	dir := s.previewDir(id)
	switch {
	case len(rest) == 0:
		list := []protocol.PreviewCreated{}
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".json") || !previewIDPattern.MatchString(strings.TrimSuffix(name, ".json")) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			var preview protocol.PreviewCreated
			if err != nil || json.Unmarshal(data, &preview) != nil {
				continue
			}
			list = append(list, preview)
		}
		sort.SliceStable(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
		writeJSON(w, http.StatusOK, map[string]any{"version": protocol.Version, "previews": list})
	case len(rest) == 1 && previewIDPattern.MatchString(rest[0]) && dir != "":
		data, err := os.ReadFile(filepath.Join(dir, rest[0]+".html"))
		if err != nil || len(data) > maxPreviewBytes {
			writeJSONError(w, http.StatusNotFound, "preview not found")
			return
		}
		writeSandboxedHTML(w, r, string(data), sandboxPolicy(nil, nil))
	default:
		writeJSONError(w, http.StatusNotFound, "preview not found")
	}
}
