package sshmgr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/sshhosts"
)

// An unending source models a remote file whose reported size is stale.
type repeatedTextReader struct{ read int }

func (r *repeatedTextReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	r.read += len(p)
	return len(p), nil
}

func TestSFTPFullUTF8Bounds(t *testing.T) {
	for _, text := range []string{"", "hello 世界😀", strings.Repeat("😀", MaxFullTextBytes/4)} {
		got, err := readFullUTF8(strings.NewReader(text))
		if err != nil || string(got) != text {
			t.Fatalf("full read of %d bytes: got %d bytes, %v", len(text), len(got), err)
		}
	}
	r := &repeatedTextReader{}
	if _, err := readFullUTF8(r); err == nil || !strings.Contains(err.Error(), "16 MiB") {
		t.Fatalf("unbounded source: %v", err)
	}
	if r.read != MaxFullTextBytes+1 {
		t.Fatalf("consumed %d bytes, want cap + 1", r.read)
	}
	for _, text := range [][]byte{{0xff}, {'a', 0xe4, 0xb8}, {0xc0, 0xaf}, {0xed, 0xa0, 0x80}} {
		if _, err := readFullUTF8(bytes.NewReader(text)); err == nil {
			t.Fatalf("accepted invalid UTF-8: %x", text)
		}
	}
}

func TestSFTPUTF8PageBoundaries(t *testing.T) {
	text := []byte("Aé中😀Z")
	for offset := range len(text) + 1 {
		for limit := 1; limit <= len(text)+1; limit++ {
			got, aligned, more, err := readUTF8Page(bytes.NewReader(text[offset:]), int64(offset), limit)
			if err != nil {
				t.Fatalf("offset=%d limit=%d: %v", offset, limit, err)
			}
			wantStart := offset
			for wantStart < len(text) && text[wantStart]&0xc0 == 0x80 {
				wantStart++
			}
			wantEnd := min(wantStart+limit, len(text))
			for !utf8.Valid(text[wantStart:wantEnd]) {
				wantEnd--
			}
			if aligned != int64(wantStart) || !bytes.Equal(got, text[wantStart:wantEnd]) || len(got) > limit || more != (wantEnd < len(text)) {
				t.Fatalf("offset=%d limit=%d: offset=%d bytes=%x more=%v; want [%d:%d]", offset, limit, aligned, got, more, wantStart, wantEnd)
			}
		}
	}
	for _, text := range [][]byte{{0xff}, {'a', 0xe4, 0xb8}, {'a', 0x80}} {
		if _, _, _, err := readUTF8Page(bytes.NewReader(text), 0, 10); err == nil {
			t.Fatalf("page accepted invalid UTF-8: %x", text)
		}
	}
	for _, text := range []string{
		strings.Repeat("a", MaxReadTextBytes-1) + "😀tail",
		strings.Repeat("😀", MaxReadTextBytes/4+1),
	} {
		got, offset, more, err := readUTF8Page(strings.NewReader(text), 0, MaxFullTextBytes)
		if err != nil || offset != 0 || !more || !utf8.Valid(got) || len(got) > MaxReadTextBytes || !strings.HasPrefix(text, string(got)) {
			t.Fatalf("page at cap: offset=%d bytes=%d more=%v err=%v", offset, len(got), more, err)
		}
	}
	r := &repeatedTextReader{}
	got, _, more, err := readUTF8Page(r, 0, MaxFullTextBytes)
	if err != nil || len(got) != MaxReadTextBytes || !more || r.read != MaxReadTextBytes+utf8.UTFMax {
		t.Fatalf("page I/O cap: read=%d returned=%d more=%v err=%v", r.read, len(got), more, err)
	}
}

func TestSFTPTextFullAndPagedDispatch(t *testing.T) {
	f := trustedFixture(t)
	text := strings.Repeat("a", DefaultReadTextBytes-1) + "😀" + strings.Repeat("中", 30000)
	f.mustCall(map[string]any{"action": "sftp_write_text", "host_id": "web", "path": "/text", "content": text})
	read := func(extra map[string]any) SFTPReadText {
		t.Helper()
		a := map[string]any{"action": "sftp_read_text", "host_id": "web", "path": "/text"}
		for k, v := range extra {
			a[k] = v
		}
		var result SFTPReadText
		if err := json.Unmarshal([]byte(f.mustCall(a)), &result); err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(result.Content) || result.BytesRead != len(result.Content) {
			t.Fatalf("inaccurate or invalid read: bytes=%d actual=%d", result.BytesRead, len(result.Content))
		}
		return result
	}
	page := read(nil)
	if !page.Truncated || page.BytesRead != DefaultReadTextBytes-1 || page.ContentHash != "" {
		t.Fatalf("default page: bytes=%d truncated=%v hash=%q", page.BytesRead, page.Truncated, page.ContentHash)
	}
	page = read(map[string]any{"offset": DefaultReadTextBytes, "limit": MaxFullTextBytes})
	if page.Offset != DefaultReadTextBytes+3 || page.Content != strings.Repeat("中", 30000) || page.Truncated {
		t.Fatalf("aligned page: offset=%d bytes=%d truncated=%v", page.Offset, page.BytesRead, page.Truncated)
	}
	full := read(map[string]any{"full": true})
	hash := sha256.Sum256([]byte(text))
	if full.Content != text || full.ContentHash != hex.EncodeToString(hash[:]) || full.Truncated || full.Offset != 0 || full.SizeBytes != int64(len(text)) || full.Entry == nil {
		t.Fatalf("full: bytes=%d size=%d hash=%q", full.BytesRead, full.SizeBytes, full.ContentHash)
	}
	for _, key := range []string{"offset", "limit", "max_bytes"} {
		f.failCall(map[string]any{"action": "sftp_read_text", "host_id": "web", "path": "/text", "full": true, key: 0}, "cannot be combined")
	}
	// Legacy limit values below the minimum still clamp to 4 KiB.
	page = read(map[string]any{"limit": 1})
	if page.BytesRead != MinReadTextBytes || !page.Truncated {
		t.Fatalf("legacy small limit: bytes=%d truncated=%v", page.BytesRead, page.Truncated)
	}
}

func TestSFTPTextWriteConflict(t *testing.T) {
	f := trustedFixture(t)
	f.mustCall(map[string]any{"action": "sftp_write_text", "host_id": "web", "path": "/text", "content": "before"})
	var read SFTPReadText
	if err := json.Unmarshal([]byte(f.mustCall(map[string]any{"action": "sftp_read_text", "host_id": "web", "path": "/text", "full": true})), &read); err != nil {
		t.Fatal(err)
	}
	write := map[string]any{"action": "sftp_write_text", "host_id": "web", "path": "/text", "content": "updated", "expected_mtime_ms": read.Entry.Mtime, "expected_content_hash": read.ContentHash}
	f.mustCall(write)
	// Same-second, same-size changes must conflict through the content hash.
	f.mustCall(map[string]any{"action": "sftp_write_text", "host_id": "web", "path": "/text", "content": "BEFORE"})
	f.failCall(write, "changed since read")
	var actual SFTPReadText
	if err := json.Unmarshal([]byte(f.mustCall(map[string]any{"action": "sftp_read_text", "host_id": "web", "path": "/text", "full": true})), &actual); err != nil || actual.Content != "BEFORE" {
		t.Fatalf("conflict modified file: %q, %v", actual.Content, err)
	}
	write["expected_mtime_ms"] = read.Entry.Mtime + 1000
	write["expected_content_hash"] = actual.ContentHash
	f.failCall(write, "changed since read")
	f.mustCall(map[string]any{"action": "sftp_delete", "host_id": "web", "path": "/text"})
	f.failCall(write, "target was removed")
	var stat SFTPStat
	if err := json.Unmarshal([]byte(f.mustCall(map[string]any{"action": "sftp_stat", "host_id": "web", "path": "/text"})), &stat); err != nil || stat.Exists {
		t.Fatalf("conflict recreated deleted file: %+v %v", stat, err)
	}
}

func TestSFTPTextWriteValidation(t *testing.T) {
	hash := strings.Repeat("0", 64)
	mtime := int64(1000)
	for _, tc := range []struct {
		name      string
		content   string
		overwrite bool
		opts      SFTPWriteOptions
	}{
		{"oversize", strings.Repeat("x", MaxFullTextBytes+1), true, SFTPWriteOptions{}},
		{"invalid utf8", string([]byte{0xff}), true, SFTPWriteOptions{}},
		{"mtime only", "", true, SFTPWriteOptions{ExpectedMtime: &mtime}},
		{"hash only", "", true, SFTPWriteOptions{ExpectedContentHash: &hash}},
		{"invalid hash", "", true, SFTPWriteOptions{ExpectedMtime: &mtime, ExpectedContentHash: new(string)}},
		{"exclusive with version", "", false, SFTPWriteOptions{ExpectedMtime: &mtime, ExpectedContentHash: &hash}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Invalid writes fail before session use, directory creation or truncation.
			var s Session
			if _, err := s.WriteTextWithOptions(context.Background(), "/text", tc.content, tc.overwrite, true, tc.opts); err == nil {
				t.Fatal("invalid write accepted")
			}
		})
	}
	if err := validateTextWrite(strings.Repeat("😀", MaxFullTextBytes/4), true, SFTPWriteOptions{}); err != nil {
		t.Fatalf("exact cap: %v", err)
	}
	for _, a := range []args{
		{"expected_mtime_ms": "1000", "expected_content_hash": hash},
		{"expected_mtime_ms": 1.5, "expected_content_hash": hash},
		{"expected_mtime_ms": float64(1 << 53), "expected_content_hash": hash},
		{"expected_mtime_ms": float64(1000)},
		{"expected_content_hash": hash},
	} {
		if _, err := a.textWriteOptions(); err == nil {
			t.Fatalf("invalid version accepted: %v", a)
		}
	}
}

func TestSFTPStrictTextRejectsRemoteBinaryAndOversize(t *testing.T) {
	f := trustedFixture(t)
	f.mustCall(map[string]any{"action": "create_session", "host_id": "web"})
	sessions := f.m.Sessions(sshhosts.ProjectPathKey(f.workdir))
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d", len(sessions))
	}
	s := sessions[0]
	c, err := s.sftpClient(f.ctx())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		body io.Reader
		want string
	}{
		{"binary", bytes.NewReader([]byte{'a', 0xff}), "valid UTF-8"},
		{"incomplete", bytes.NewReader([]byte{'a', 0xe4, 0xb8}), "valid UTF-8"},
		{"oversize", io.LimitReader(&repeatedTextReader{}, MaxFullTextBytes+1), "16 MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := c.Create("/" + tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(file, tc.body); err != nil {
				file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ReadFullText(f.ctx(), "/"+tc.name); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("full remote read: %v", err)
			}
		})
	}
}
