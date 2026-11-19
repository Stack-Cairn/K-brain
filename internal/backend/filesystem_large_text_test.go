package backend

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFilesystemFullText16MiBAndConflict(t *testing.T) {
	dir := t.TempDir()
	s := &Server{filesystemRoots: []string{dir}}
	r := httptest.NewRequest("POST", "/v1/fs", nil)
	content := strings.Repeat("界\n", filesystemReadLimit/4)
	created, err := s.filesystem(r, filesystemInput{Action: "write_text", Workdir: dir, Path: "large.txt", Content: content})
	if err != nil || created.(map[string]any)["bytes_written"] != filesystemReadLimit {
		t.Fatalf("create: %v, %v", created, err)
	}
	out, err := s.filesystem(r, filesystemInput{Action: "read_text", Workdir: dir, Path: "large.txt"})
	if err != nil {
		t.Fatal(err)
	}
	read := out.(map[string]any)
	hash := sha256.Sum256([]byte(content))
	if read["content"] != content || read["size_bytes"] != filesystemReadLimit || read["total_lines"] != filesystemReadLimit/4 || read["content_hash"] != hex.EncodeToString(hash[:]) {
		t.Fatal("full text or metadata did not survive read")
	}
	if _, ok := read["preview_offset"]; ok {
		t.Fatal("full read was marked as a range")
	}
	mtime, token := read["mtime_ms"].(int64), read["content_hash"].(string)
	write := filesystemInput{Action: "write_text", Workdir: dir, Path: "large.txt", Content: strings.Repeat("z", filesystemReadLimit), ExpectedMtime: &mtime, ExpectedHash: &token}
	if _, err := s.filesystem(r, write); err != nil {
		t.Fatal(err)
	}
	if _, err := s.filesystem(r, write); err == nil {
		t.Fatal("stale hash accepted for large overwrite")
	}
	write.Content += "x"
	if _, err := s.filesystem(r, write); err == nil {
		t.Fatal("write above 16 MiB accepted")
	}
	f, err := os.OpenFile(filepath.Join(dir, "large.txt"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.filesystem(r, filesystemInput{Action: "read_text", Workdir: dir, Path: "large.txt"}); err == nil {
		t.Fatal("read above 16 MiB accepted")
	}
}

func TestFilesystemUTF8BytePreviewPages(t *testing.T) {
	dir := t.TempDir()
	// Both the first page boundary and arbitrary offsets intersect a four-byte rune.
	content := strings.Repeat("a", filesystemTextPreviewLimit-1) + "😀界\n" + strings.Repeat("b", filesystemTextPreviewLimit)
	if err := os.WriteFile(filepath.Join(dir, "text"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Server{filesystemRoots: []string{dir}}
	r := httptest.NewRequest("POST", "/v1/fs", nil)
	var reconstructed strings.Builder
	var offset int64
	for {
		out, err := s.filesystem(r, filesystemInput{Action: "read_text", Workdir: dir, Path: "text", PreviewOffset: &offset})
		if err != nil {
			t.Fatal(err)
		}
		page := out.(map[string]any)
		text := page["content"].(string)
		if !utf8.ValidString(text) || len(text) > filesystemTextPreviewLimit || page["preview_offset"] != offset || page["size_bytes"] != int64(len(content)) {
			t.Fatal("invalid page bounds or metadata")
		}
		if _, ok := page["content_hash"]; ok {
			t.Fatal("partial read exposed an overwrite token")
		}
		if _, ok := page["total_lines"]; ok {
			t.Fatal("partial read returned a whole-file line count")
		}
		reconstructed.WriteString(text)
		next := page["preview_next_offset"].(*int64)
		if next == nil {
			break
		}
		if *next != offset+int64(len(text)) || *next <= offset {
			t.Fatal("page cursor failed to advance by returned UTF-8 bytes")
		}
		offset = *next
	}
	if reconstructed.String() != content {
		t.Fatal("paging dropped or replaced characters")
	}
	for _, offset := range []int64{filesystemTextPreviewLimit, filesystemTextPreviewLimit + 1, filesystemTextPreviewLimit + 2} {
		out, err := s.filesystem(r, filesystemInput{Action: "read_text", Workdir: dir, Path: "text", PreviewOffset: &offset})
		if err != nil {
			t.Fatal(err)
		}
		page := out.(map[string]any)
		if page["preview_offset"] != int64(filesystemTextPreviewLimit+3) || !strings.HasPrefix(page["content"].(string), "界\n") {
			t.Fatal("offset inside rune did not align forward")
		}
	}
	offset = int64(len(content))
	out, err := s.filesystem(r, filesystemInput{Action: "read_text", Workdir: dir, Path: "text", PreviewOffset: &offset})
	if err != nil || out.(map[string]any)["content"] != "" || out.(map[string]any)["preview_next_offset"].(*int64) != nil {
		t.Fatalf("EOF: %v, %v", out, err)
	}
	for _, offset := range []int64{-1, int64(len(content) + 1)} {
		if _, err := s.filesystem(r, filesystemInput{Action: "read_text", Workdir: dir, Path: "text", PreviewOffset: &offset}); err == nil {
			t.Fatal("invalid byte offset accepted")
		}
	}
}

func TestFilesystemPreviewAlignsInsideFinalRune(t *testing.T) {
	dir := t.TempDir()
	s := &Server{filesystemRoots: []string{dir}}
	r := httptest.NewRequest("POST", "/v1/fs", nil)
	for _, text := range []string{"😀", "界", "�", "a😀"} {
		if err := os.WriteFile(filepath.Join(dir, "last"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		for offset := int64(0); offset < int64(len(text)); offset++ {
			if utf8.RuneStart(text[offset]) {
				continue
			}
			out, err := s.filesystem(r, filesystemInput{Action: "read_text", Workdir: dir, Path: "last", PreviewOffset: &offset})
			if err != nil {
				t.Fatalf("text=%q offset=%d: %v", text, offset, err)
			}
			page := out.(map[string]any)
			if page["preview_offset"] != int64(len(text)) || page["content"] != "" || page["preview_next_offset"].(*int64) != nil {
				t.Fatal("final rune did not align to EOF")
			}
		}
	}
	for _, text := range []string{"a\x80", "\xf0\x9f\x98", "\xc0\x80"} {
		if err := os.WriteFile(filepath.Join(dir, "last"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		offset := int64(1)
		if _, err := s.filesystem(r, filesystemInput{Action: "read_text", Workdir: dir, Path: "last", PreviewOffset: &offset}); err == nil {
			t.Fatalf("malformed final rune %q accepted", text)
		}
	}
}

type countedPreviewReader struct {
	*bytes.Reader
	read int
}

func (r *countedPreviewReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

func TestFilesystemPreviewSeeksAndBoundsIO(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "info")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(filesystemReadLimit + 100); err != nil {
		t.Fatal(err)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	// Invalid bytes before the requested offset must not be scanned or indexed.
	prefix := bytes.Repeat([]byte{0xff}, filesystemTextPreviewLimit)
	body := append(prefix, bytes.Repeat([]byte("x"), filesystemTextPreviewLimit+100)...)
	r := &countedPreviewReader{Reader: bytes.NewReader(body)}
	out, err := filesystemTextPreview(r, info, "large", int64(len(prefix)))
	if err != nil {
		t.Fatal(err)
	}
	page := out.(map[string]any)
	if r.read != filesystemTextPreviewLimit+utf8.UTFMax || len(page["content"].(string)) != filesystemTextPreviewLimit || page["size_bytes"] != info.Size() {
		t.Fatalf("unbounded read or incorrect whole-file metadata: bytes read=%d", r.read)
	}
	for _, bad := range [][]byte{[]byte("\x00text"), []byte("text\xff"), []byte("text\xf0\x9f"), bytes.Repeat([]byte{0x80}, filesystemTextPreviewLimit+10)} {
		if _, err := filesystemTextPreview(bytes.NewReader(bad), info, "bad", 0); err == nil {
			t.Fatal("invalid UTF-8 or binary preview accepted")
		}
	}
}

func TestFilesystemHTTPAllowsWorstCaseEscaped16MiB(t *testing.T) {
	dir := t.TempDir()
	s := &Server{token: "secret", filesystemRoots: []string{dir}}
	// Each decoded ASCII control byte occupies six bytes on the wire. A global
	// 4 MiB request limit or a 16 MiB raw-body limit would reject this valid save.
	prefix := fmt.Sprintf(`{"action":"write_text","workdir":%q,"path":"escaped.txt","content":"`, filepath.ToSlash(dir))
	body := io.MultiReader(strings.NewReader(prefix), strings.NewReader(strings.Repeat(`\u0001`, filesystemReadLimit)), strings.NewReader(`"}`))
	r := httptest.NewRequest("POST", "/v1/fs", body)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("escaped 16 MiB save: %d %s", w.Code, w.Body.String())
	}
	info, err := os.Stat(filepath.Join(dir, "escaped.txt"))
	if err != nil || info.Size() != filesystemReadLimit {
		t.Fatalf("saved metadata: %v, %v", info, err)
	}
}

func TestFilesystemHTTPRejectsOverLimitAndKeepsGenericLimit(t *testing.T) {
	dir := t.TempDir()
	s := &Server{token: "secret", filesystemRoots: []string{dir}}
	prefix := fmt.Sprintf(`{"action":"write_text","workdir":%q,"path":"too-large.txt","content":"`, filepath.ToSlash(dir))
	for _, tc := range []struct {
		name string
		body io.Reader
		want int
	}{
		{"decoded", io.MultiReader(strings.NewReader(prefix), strings.NewReader(strings.Repeat("x", filesystemReadLimit+1)), strings.NewReader(`"}`)), http.StatusUnprocessableEntity},
		{"wire", io.MultiReader(strings.NewReader(prefix), strings.NewReader(strings.Repeat("x", filesystemRequestLimit))), http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/fs", tc.body)
			r.Header.Set("Authorization", "Bearer secret")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
			}
			if _, err := os.Stat(filepath.Join(dir, "too-large.txt")); !os.IsNotExist(err) {
				t.Fatal("oversized save modified disk")
			}
		})
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/sessions", strings.NewReader(`{"content":"`+strings.Repeat("x", maxBodyBytes)+`"}`))
	var in struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(w, r, &in); err == nil || w.Code != http.StatusRequestEntityTooLarge {
		t.Fatal("generic JSON limit changed")
	}
}
