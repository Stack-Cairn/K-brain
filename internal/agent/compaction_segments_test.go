package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// summaryServer answers every completion with "summary N" and records the requests.
func summaryServer(t *testing.T) (*httptest.Server, func() []ai.Request) {
	t.Helper()
	var mu sync.Mutex
	var requests []ai.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ai.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		mu.Lock()
		requests = append(requests, request)
		n := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"summary ` + string(rune('0'+n)) + `"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(server.Close)
	return server, func() []ai.Request {
		mu.Lock()
		defer mu.Unlock()
		return append([]ai.Request(nil), requests...)
	}
}

func addTurns(a *Agent, label string, n int) {
	for i := range n {
		a.Messages = append(a.Messages,
			ai.Message{Role: "user", Content: label + " question " + string(rune('a'+i)) + " " + strings.Repeat("word ", 4000)},
			ai.Message{Role: "assistant", Content: label + " answer " + string(rune('a'+i))})
	}
}

func TestCompactionWritesSegmentsAndPointsAtThem(t *testing.T) {
	server, requests := summaryServer(t)
	dir := filepath.Join(t.TempDir(), "session", "compaction")
	a := New(ai.New(server.URL, "fixture"), "fixture", 100, "stable")
	// A dedicated compaction client uses the text projection, which exposes the prior summary.
	a.CompactClient = ai.New(server.URL, "fixture")
	a.CompactionDir = func() string { return dir }
	a.SetSessionID("sess-1")
	addTurns(a, "first", 6)

	if _, _, _, err := a.compact(t.Context()); err != nil {
		t.Fatal(err)
	}
	segment, err := os.ReadFile(filepath.Join(dir, "segment_001.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Compacted transcript — segment 001", "- Session: sess-1", "## Summary written at compaction\n\nsummary 1", "### [1] user", "first question a", "first answer a"} {
		if !strings.Contains(string(segment), want) {
			t.Fatalf("segment missing %q:\n%.600s", want, segment)
		}
	}
	index, err := os.ReadFile(filepath.Join(dir, "INDEX.md"))
	if err != nil || !strings.Contains(string(index), "| 001 | segment_001.md |") {
		t.Fatalf("INDEX.md = %q, %v", index, err)
	}

	stored := a.MessagesSnapshot()
	if stored[1].Content != summaryPrefix+"summary 1" {
		t.Fatalf("stored summary must stay plain: %q", stored[1].Content)
	}
	sent := a.requestMessages(stored)
	if !strings.Contains(sent[1].Content, "<compacted_transcript>") || !strings.Contains(sent[1].Content, filepath.Join(dir, "INDEX.md")) {
		t.Fatalf("request summary lacks the transcript pointer: %q", sent[1].Content)
	}
	if stored[1].Content != summaryPrefix+"summary 1" {
		t.Fatal("requestMessages mutated stored history")
	}

	addTurns(a, "second", 6)
	if _, _, _, err := a.compact(t.Context()); err != nil {
		t.Fatal(err)
	}
	all := requests()
	prompt := all[len(all)-1].Messages[1].Content
	if !strings.Contains(prompt, "<summary>\nsummary 1\n</summary>") || strings.Contains(prompt, "compacted_transcript") {
		t.Fatalf("prior summary must be merged without the pointer:\n%.400s", prompt)
	}
	second, err := os.ReadFile(filepath.Join(dir, "segment_002.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(second), "first question a") || !strings.Contains(string(second), "second question a") {
		t.Fatal("segments must hold disjoint, sequential turns")
	}
	if strings.Contains(string(second), summaryPrefix) {
		t.Fatal("the previous summary message is not part of a segment's transcript")
	}
	index, _ = os.ReadFile(filepath.Join(dir, "INDEX.md"))
	if strings.Count(string(index), "# Compacted transcript index") != 1 || !strings.Contains(string(index), "| 002 | segment_002.md |") {
		t.Fatalf("INDEX.md should gain one row per segment:\n%s", index)
	}
}

func TestCompactionWithoutDirectoryAddsNoPointer(t *testing.T) {
	server, _ := summaryServer(t)
	a := New(ai.New(server.URL, "fixture"), "fixture", 100, "stable")
	addTurns(a, "only", 4)
	if _, _, _, err := a.compact(t.Context()); err != nil {
		t.Fatal(err)
	}
	if sent := a.requestMessages(a.MessagesSnapshot()); strings.Contains(sent[1].Content, "compacted_transcript") {
		t.Fatalf("no archive directory, no pointer: %q", sent[1].Content)
	}
}

func TestTranscriptPointerFollowsReloadedSummary(t *testing.T) {
	dir := t.TempDir()
	a := New(nil, "fixture", 100, "stable")
	a.CompactionDir = func() string { return dir }
	msgs := []ai.Message{{Role: "system", Content: "stable"}, {Role: "system", Content: summaryPrefix + "restored"}, {Role: "user", Content: "hi"}}
	if got := a.requestMessages(msgs); strings.Contains(got[1].Content, "compacted_transcript") {
		t.Fatal("pointer needs an INDEX.md on disk")
	}
	if _, err := writeCompactionSegment(dir, "", msgs[2:], "restored", time.Now()); err != nil {
		t.Fatal(err)
	}
	a.resetTranscriptPointer()
	if got := a.requestMessages(msgs); !strings.Contains(got[1].Content, "compacted_transcript") {
		t.Fatal("a session reloaded with segments on disk should get the pointer")
	}
}

func TestCompactionSegmentRendering(t *testing.T) {
	image := ai.ContentPart{Type: "image_url", MimeType: "image/png", W: 640, H: 480}
	image.ImageURL = &struct {
		URL string `json:"url"`
	}{URL: "data:image/png;base64," + strings.Repeat("QUJD", 5000)}
	blob := strings.Repeat("A", 4096)
	call := ai.ToolCall{ID: "call_1"}
	call.Function.Name, call.Function.Arguments = "read", `{"path":"a.go"}`
	msgs := []ai.Message{
		{Role: "user", Content: "look at this\x00 picture", Parts: []ai.ContentPart{image}},
		{Role: "assistant", Content: "reading", ToolCalls: []ai.ToolCall{call}},
		{Role: "tool", ToolCallID: "call_1", Name: "read", Content: "```go\npackage a\n```\ninline data:image/jpeg;base64,/9j/4AAQSkZJRg== and " + blob},
		{Role: "user", Content: strings.Repeat("y ", compactionMessageMaxBytes/2+50)},
	}
	md := renderCompactionSegment(3, "", msgs, "sum", time.Unix(0, 0))
	for _, want := range []string{
		"[image_url: image/png, 640x480, inline payload omitted]",
		"look at this picture",
		"**Tool call** `read` (call_1)",
		"```json\n{\"path\":\"a.go\"}\n```",
		"### [3] tool result · read (call_1)",
		"````\n```go\npackage a\n```",
		"[image/jpeg base64 payload omitted,",
		"[base64-like payload omitted, 4096 bytes]",
		"[... 100 bytes omitted ...]",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("segment missing %q:\n%s", want, md[:min(len(md), 1500)])
		}
	}
	if strings.Contains(md, "QUJDQUJD") || strings.Contains(md, blob) || strings.ContainsRune(md, 0) {
		t.Fatal("binary payloads must not reach the segment")
	}
}

func TestCompactionSegmentCapsSize(t *testing.T) {
	var msgs []ai.Message
	for range 100 {
		msgs = append(msgs, ai.Message{Role: "user", Content: strings.Repeat("z ", 30000)})
	}
	md := renderCompactionSegment(1, "", msgs, "", time.Now())
	if len(md) > compactionSegmentMaxBytes {
		t.Fatalf("segment is %d bytes, cap %d", len(md), compactionSegmentMaxBytes)
	}
	if !strings.Contains(md, "later messages omitted ...]") {
		t.Fatal("truncated segment should say how many messages were dropped")
	}
}

func TestCompactionSegmentsPruneOldestPastBudget(t *testing.T) {
	old := compactionArchiveMaxBytes
	compactionArchiveMaxBytes = 30 << 10
	t.Cleanup(func() { compactionArchiveMaxBytes = old })
	dir := t.TempDir()
	msgs := []ai.Message{{Role: "user", Content: strings.Repeat("p ", 6000)}}
	for range 4 {
		if _, err := writeCompactionSegment(dir, "", msgs, "s", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	segments := compactionSegments(dir)
	if len(segments) == 0 || segments[len(segments)-1].index != 4 || segments[0].index == 1 {
		t.Fatalf("segments = %+v, want oldest pruned and newest kept", segments)
	}
	var total int64
	for _, s := range segments {
		total += s.size
	}
	if total > compactionArchiveMaxBytes {
		t.Fatalf("archive is %d bytes, budget %d", total, compactionArchiveMaxBytes)
	}
}
