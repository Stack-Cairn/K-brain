package backend

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

// filesystemTextPreview seeks directly to a bounded byte window. It does not
// hash or count lines in the rest of the file, and deliberately returns no
// content_hash: a partial read must never serve as an overwrite version token.
func filesystemTextPreview(file io.ReadSeeker, info os.FileInfo, path string, offset int64) (any, error) {
	if offset < 0 || offset > info.Size() {
		return nil, errors.New("preview_offset is outside file")
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	// Up to three continuation bytes may precede the next rune. Keep one
	// further byte beyond the returned page to detect and trim its last rune.
	body, err := io.ReadAll(io.LimitReader(file, filesystemTextPreviewLimit+utf8.UTFMax))
	if err != nil {
		return nil, err
	}
	start := 0
	for start < len(body) && !utf8.RuneStart(body[start]) {
		start++
	}
	if start > 0 {
		if offset == 0 || start >= utf8.UTFMax {
			return nil, errors.New("file is not UTF-8 text")
		}
		// Validate the intersected rune with at most three bytes of lookbehind.
		// This also permits alignment to EOF inside the file's final rune.
		before := make([]byte, min(offset, utf8.UTFMax-1))
		if _, err := file.Seek(offset-int64(len(before)), io.SeekStart); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(file, before); err != nil {
			return nil, err
		}
		lead := len(before) - 1
		for lead > 0 && !utf8.RuneStart(before[lead]) {
			lead--
		}
		intersected := append(before[lead:], body[:start]...)
		r, width := utf8.DecodeRune(intersected)
		if (r == utf8.RuneError && width == 1) || width != len(intersected) {
			return nil, errors.New("file is not UTF-8 text")
		}
	}
	offset += int64(start)
	body = body[start:]
	end := min(len(body), filesystemTextPreviewLimit)
	if end < len(body) {
		for end > 0 && !utf8.RuneStart(body[end]) {
			end--
		}
		if filesystemTextPreviewLimit-end >= utf8.UTFMax {
			return nil, errors.New("file is not UTF-8 text")
		}
	}
	if !utf8.Valid(body[:end]) || bytes.IndexByte(body[:end], 0) >= 0 {
		return nil, errors.New("file is not UTF-8 text")
	}
	var next *int64
	if end < len(body) {
		n := offset + int64(end)
		next = &n
	}
	return map[string]any{
		"path": filepath.ToSlash(filepath.Clean(path)), "content": string(body[:end]),
		"mtime_ms": info.ModTime().UnixMilli(), "size_bytes": info.Size(),
		"preview_offset": offset, "preview_next_offset": next,
	}, nil
}
