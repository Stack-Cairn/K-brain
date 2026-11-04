package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// Compaction keeps the model context small by replacing old turns with a summary. The
// replaced turns are also written as readable markdown under the session directory, so
// the model can read or grep the exact text the summary left out:
//
//	<session dir>/compaction/INDEX.md          one row per segment
//	<session dir>/compaction/segment_001.md    the turns removed by the first compaction
//
// The summary message sent to the model carries a pointer to that directory.
const (
	compactionIndexFile = "INDEX.md"
	compactionSegPrefix = "segment_"

	// compactionSegmentMaxBytes caps one segment file; later turns are dropped at a
	// whole-message boundary with a notice.
	compactionSegmentMaxBytes = 4 << 20
	// compactionMessageMaxBytes caps the text of one message or tool result.
	compactionMessageMaxBytes = 64 << 10
)

// compactionArchiveMaxBytes caps all segments of a session; the oldest segment files
// are removed first once a new segment pushes the total past it. A var for tests.
var compactionArchiveMaxBytes int64 = 64 << 20

const compactionIndexHeader = "# Compacted transcript index\n\n" +
	"Turns removed from the model context by compaction, oldest first. Each segment file holds the\n" +
	"full text of those turns (images and other binary payloads are replaced by placeholders).\n" +
	"Read or grep them for details; do not edit. Old segments may be pruned to keep this directory under " +
	"64 MiB.\n\n" +
	"| Segment | File | Compacted at | Messages | First user message |\n" +
	"|---|---|---|---|---|\n"

// archiveCompaction writes the compacted-away messages as the next segment. It is best
// effort: a failure leaves compaction itself untouched and only omits the pointer.
func (a *Agent) archiveCompaction(msgs []ai.Message, summary string) {
	if a.CompactionDir == nil || len(msgs) == 0 {
		return
	}
	dir := a.CompactionDir()
	if dir == "" || !filepath.IsAbs(dir) {
		return
	}
	_, _ = writeCompactionSegment(dir, a.SessionIDValue(), msgs, summary, time.Now())
}

// writeCompactionSegment renders msgs into dir/segment_NNN.md, appends a row to
// dir/INDEX.md and prunes the oldest segments past the directory budget.
func writeCompactionSegment(dir, sessionID string, msgs []ai.Message, summary string, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	segments := compactionSegments(dir)
	n := 1
	if len(segments) > 0 {
		n = segments[len(segments)-1].index + 1
	}
	name := fmt.Sprintf("%s%03d.md", compactionSegPrefix, n)
	path := filepath.Join(dir, name)
	body := renderCompactionSegment(n, sessionID, msgs, summary, now)
	if err := writeFileAtomic(path, []byte(body)); err != nil {
		return "", err
	}
	indexPath := filepath.Join(dir, compactionIndexFile)
	index, err := os.ReadFile(indexPath)
	if err != nil || !strings.HasPrefix(string(index), "# ") {
		index = []byte(compactionIndexHeader)
	}
	row := fmt.Sprintf("| %03d | %s | %s | %d | %s |\n", n, name, now.UTC().Format(time.RFC3339), len(msgs), indexExcerpt(msgs))
	if err := writeFileAtomic(indexPath, append(index, row...)); err != nil {
		return "", err
	}
	pruneCompactionSegments(dir, n)
	return path, nil
}

type compactionSegment struct {
	index int
	path  string
	size  int64
}

func compactionSegments(dir string) []compactionSegment {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []compactionSegment
	for _, e := range entries {
		name := e.Name()
		digits, ok := strings.CutPrefix(name, compactionSegPrefix)
		if !ok || e.IsDir() {
			continue
		}
		digits, ok = strings.CutSuffix(digits, ".md")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(digits)
		if err != nil || n <= 0 {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, compactionSegment{index: n, path: filepath.Join(dir, name), size: info.Size()})
	}
	slices.SortFunc(out, func(a, b compactionSegment) int { return a.index - b.index })
	return out
}

// pruneCompactionSegments removes the oldest segments until the directory fits the
// budget. The newest segment (keep) always stays.
func pruneCompactionSegments(dir string, keep int) {
	segments := compactionSegments(dir)
	var total int64
	for _, s := range segments {
		total += s.size
	}
	for _, s := range segments {
		if total <= compactionArchiveMaxBytes || s.index == keep {
			break
		}
		if os.Remove(s.path) == nil {
			total -= s.size
		}
	}
}

func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func indexExcerpt(msgs []ai.Message) string {
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		text := strings.Join(strings.Fields(sanitizeTranscriptText(m.TextContent())), " ")
		if text == "" {
			continue
		}
		text = strings.ReplaceAll(text, "|", "\\|")
		if len(text) > 80 {
			text = truncateUTF8(text, 79) + "…"
		}
		return text
	}
	return "—"
}

func renderCompactionSegment(n int, sessionID string, msgs []ai.Message, summary string, now time.Time) string {
	var head strings.Builder
	fmt.Fprintf(&head, "# Compacted transcript — segment %03d\n\n", n)
	head.WriteString("Historical record of turns removed from the model context by compaction. Read or grep it for details; do not edit.\n\n")
	if sessionID != "" {
		fmt.Fprintf(&head, "- Session: %s\n", sessionID)
	}
	fmt.Fprintf(&head, "- Compacted at: %s\n", now.UTC().Format(time.RFC3339))
	fmt.Fprintf(&head, "- Messages: %d\n\n", len(msgs))
	if summary = strings.TrimSpace(summary); summary != "" {
		head.WriteString("## Summary written at compaction\n\n")
		head.WriteString(sanitizeTranscriptText(summary))
		head.WriteString("\n\n")
	}
	head.WriteString("## Transcript\n\n")

	const notice = "\n[... segment truncated at %d bytes: %d later messages omitted ...]\n"
	budget := compactionSegmentMaxBytes - head.Len() - len(notice) - 32
	var body strings.Builder
	for i, m := range msgs {
		block := renderTranscriptMessage(i+1, m)
		if body.Len()+len(block) > budget {
			fmt.Fprintf(&body, notice, compactionSegmentMaxBytes, len(msgs)-i)
			break
		}
		body.WriteString(block)
	}
	return head.String() + body.String()
}

func renderTranscriptMessage(n int, m ai.Message) string {
	var b strings.Builder
	role := m.Role
	if role == "tool" {
		role = "tool result"
		if m.Name != "" {
			role += " · " + m.Name
		}
		if m.ToolCallID != "" {
			role += " (" + m.ToolCallID + ")"
		}
	}
	fmt.Fprintf(&b, "### [%d] %s", n, role)
	if m.SentAt != nil {
		fmt.Fprintf(&b, " · %s", m.SentAt.UTC().Format(time.RFC3339))
	}
	b.WriteString("\n\n")
	text := capTranscriptText(sanitizeTranscriptText(m.Content))
	if m.Role == "tool" {
		if text != "" {
			b.WriteString(fenced(text, ""))
		}
	} else if strings.TrimSpace(text) != "" {
		b.WriteString(text)
		b.WriteString("\n\n")
	}
	for _, p := range m.Parts {
		switch {
		case p.Type == "text":
			if t := capTranscriptText(sanitizeTranscriptText(p.Text)); strings.TrimSpace(t) != "" {
				b.WriteString(t)
				b.WriteString("\n\n")
			}
		default:
			b.WriteString(partPlaceholder(p))
			b.WriteString("\n\n")
		}
	}
	for _, tc := range m.ToolCalls {
		fmt.Fprintf(&b, "**Tool call** `%s`", tc.Function.Name)
		if tc.ID != "" {
			fmt.Fprintf(&b, " (%s)", tc.ID)
		}
		b.WriteString("\n\n")
		if args := capTranscriptText(sanitizeTranscriptText(tc.Function.Arguments)); strings.TrimSpace(args) != "" {
			b.WriteString(fenced(args, "json"))
		}
	}
	return b.String()
}

// partPlaceholder describes a non-text part without its payload.
func partPlaceholder(p ai.ContentPart) string {
	kind := p.Type
	if kind == "" {
		kind = "attachment"
	}
	var details []string
	if p.MimeType != "" {
		details = append(details, p.MimeType)
	}
	if p.W > 0 && p.H > 0 {
		details = append(details, fmt.Sprintf("%dx%d", p.W, p.H))
	}
	if p.FileURL != nil && p.FileURL.Filename != "" {
		details = append(details, p.FileURL.Filename)
	}
	url := ""
	if p.ImageURL != nil {
		url = p.ImageURL.URL
	} else if p.FileURL != nil {
		url = p.FileURL.URL
	}
	switch {
	case strings.HasPrefix(url, "data:"):
		details = append(details, "inline payload omitted")
	case url != "" && len(url) <= 512:
		details = append(details, url)
	}
	if len(details) == 0 {
		return "[" + kind + "]"
	}
	return "[" + kind + ": " + strings.Join(details, ", ") + "]"
}

var (
	dataURLPattern = regexp.MustCompile(`data:([A-Za-z0-9.+/-]+)?;base64,[A-Za-z0-9+/=_-]+`)
	// base64RunPattern finds candidate base64 runs (encoded files or images pasted into
	// tool output). RE2 caps repeat counts at 1000, so runs shorter than minBase64Run
	// are filtered in code.
	base64RunPattern = regexp.MustCompile(`[A-Za-z0-9+/]{1000,}={0,2}`)
)

const minBase64Run = 2048

// sanitizeTranscriptText keeps text readable and greppable: invalid UTF-8 and control
// characters go, and inline base64 payloads become placeholders.
func sanitizeTranscriptText(s string) string {
	if s == "" {
		return s
	}
	s = strings.ToValidUTF8(s, string(utf8.RuneError))
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r == '\r' {
			return -1
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	s = dataURLPattern.ReplaceAllStringFunc(s, func(m string) string {
		mime, _, _ := strings.Cut(strings.TrimPrefix(m, "data:"), ";")
		if mime == "" {
			mime = "application/octet-stream"
		}
		return fmt.Sprintf("[%s base64 payload omitted, %d bytes]", mime, len(m))
	})
	return base64RunPattern.ReplaceAllStringFunc(s, func(m string) string {
		if len(m) < minBase64Run {
			return m
		}
		return fmt.Sprintf("[base64-like payload omitted, %d bytes]", len(m))
	})
}

func capTranscriptText(s string) string {
	if len(s) <= compactionMessageMaxBytes {
		return s
	}
	return truncateUTF8(s, compactionMessageMaxBytes) + fmt.Sprintf("\n[... %d bytes omitted ...]", len(s)-compactionMessageMaxBytes)
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// fenced wraps text in a code fence longer than any backtick run inside it.
func fenced(text, lang string) string {
	longest, run := 0, 0
	for _, r := range text {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	return fence + lang + "\n" + strings.TrimRight(text, "\n") + "\n" + fence + "\n\n"
}

const transcriptPointerOpen = "\n\n<compacted_transcript>\n"

// transcriptPointer is appended to the summary message in model requests when this
// session has compaction segments on disk.
func transcriptPointerText(dir string) string {
	return transcriptPointerOpen +
		"The turns this summary replaces are saved verbatim as markdown in " + dir + " — " +
		filepath.Join(dir, compactionIndexFile) + " lists the segments and segment_NNN.md files hold the turns in order. " +
		"When you need exact details the summary left out (earlier file contents, commands, errors, the user's wording), " +
		"read or grep those files instead of guessing. They are read-only history; do not edit them.\n" +
		"</compacted_transcript>"
}

// requestMessages prepares stored history for a model request.
func (a *Agent) requestMessages(msgs []ai.Message) []ai.Message {
	return a.modeMessages(a.withTranscriptPointer(msgs))
}

// withTranscriptPointer returns msgs with the compaction pointer appended to the
// summary message. The stored history keeps the plain summary, so the pointer never
// leaks into the next summary request's prior text or into the persisted session.
func (a *Agent) withTranscriptPointer(msgs []ai.Message) []ai.Message {
	i := summaryMessageIndex(msgs)
	if i < 0 {
		return msgs
	}
	pointer := a.transcriptPointer(msgs[i].Content)
	if pointer == "" {
		return msgs
	}
	out := slices.Clone(msgs)
	out[i].Content += pointer
	return out
}

func summaryMessageIndex(msgs []ai.Message) int {
	for i := 0; i < len(msgs) && i < 3; i++ {
		if msgs[i].Role == "system" && strings.HasPrefix(msgs[i].Content, summaryPrefix) {
			return i
		}
	}
	return -1
}

// transcriptPointer resolves the pointer once per summary: the directory lookup can
// scan the session store, so it is not repeated for every request.
func (a *Agent) transcriptPointer(summary string) string {
	a.pointerMu.Lock()
	defer a.pointerMu.Unlock()
	if a.pointerResolved && a.pointerSummary == summary {
		return a.pointerText
	}
	a.pointerResolved, a.pointerSummary, a.pointerText = true, summary, ""
	if a.CompactionDir == nil {
		return ""
	}
	dir := a.CompactionDir()
	if dir == "" {
		return ""
	}
	if info, err := os.Stat(filepath.Join(dir, compactionIndexFile)); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	a.pointerText = transcriptPointerText(dir)
	return a.pointerText
}

func (a *Agent) resetTranscriptPointer() {
	a.pointerMu.Lock()
	a.pointerResolved = false
	a.pointerMu.Unlock()
}
