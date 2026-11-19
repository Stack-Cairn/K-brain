package backend

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	filesystemReadLimit        = 16 << 20
	filesystemTextPreviewLimit = 256 << 10
	filesystemAssetReadLimit   = 2 << 20
	// A UTF-8 byte can require six JSON bytes (for example, \u0001).
	// Leave 64 KiB for paths, version tokens and the request envelope.
	filesystemRequestLimit = 6*filesystemReadLimit + (64 << 10)
)

type filesystemInput struct {
	Action        string  `json:"action"`
	Workdir       string  `json:"workdir"`
	Path          string  `json:"path"`
	Depth         uint32  `json:"depth"`
	Offset        uint32  `json:"offset"`
	PreviewOffset *int64  `json:"preview_offset,omitempty"`
	MaxResults    uint32  `json:"max_results"`
	ShowHidden    bool    `json:"show_hidden"`
	Content       string  `json:"content"`
	Mode          string  `json:"mode"`
	ExpectedMtime *int64  `json:"expected_mtime_ms"`
	ExpectedHash  *string `json:"expected_content_hash"`
	ToPath        string  `json:"to_path"`
}

// filesystemRoot confines each operation, including symlink traversal, to an
// administrator-selected root rather than trusting a browser-supplied workdir.
func (s *Server) filesystemRoot(path string) (*os.Root, string, error) {
	if !filepath.IsAbs(path) {
		return nil, "", errors.New("filesystem path must be absolute")
	}
	for _, configured := range s.filesystemRoots {
		root, err := filepath.Abs(configured)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, filepath.Clean(path))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		handle, err := os.OpenRoot(root)
		return handle, rel, err
	}
	return nil, "", errors.New("filesystem path is outside configured roots; configure backend -filesystem-root")
}

func (s *Server) handleFilesystem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSONError(w, 405, "method not allowed")
		return
	}
	var input filesystemInput
	if err := decodeJSONLimit(w, r, &input, filesystemRequestLimit); err != nil {
		return
	}
	result, err := s.filesystem(r, input)
	if err != nil {
		writeJSONError(w, 422, err.Error())
		return
	}
	writeJSON(w, 200, result)
}

func (s *Server) filesystem(r *http.Request, in filesystemInput) (any, error) {
	if in.PreviewOffset != nil && (in.Action != "read_text" || *in.PreviewOffset < 0) {
		return nil, errors.New("preview_offset requires read_text and a nonnegative byte offset")
	}
	if in.Action == "roots" {
		roots := []map[string]string{}
		for i, path := range s.filesystemRoots {
			absolute, err := filepath.Abs(path)
			if err != nil {
				return nil, err
			}
			roots = append(roots, map[string]string{"id": fmt.Sprintf("backend-%d", i), "path": absolute, "kind": "workspace", "label": absolute})
		}
		return map[string]any{"roots": roots}, nil
	}
	if in.Action != "list_dirs" && in.Action != "list" && in.Action != "read_text" && in.Action != "read_preview" && in.Action != "write_text" && in.Action != "mkdir" && in.Action != "rename" && in.Action != "delete" {
		return nil, errors.New("unsupported filesystem action")
	}
	path := in.Path
	if in.Action != "list_dirs" {
		if !filepath.IsAbs(in.Workdir) || filepath.IsAbs(path) || strings.Contains(path, "\\") {
			return nil, errors.New("workdir must be absolute and file path must be workspace-relative")
		}
		rel := filepath.Clean(filepath.FromSlash(path))
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, errors.New("file path is outside workspace")
		}
		path = filepath.Join(in.Workdir, rel)
	}
	rootPath := path
	if in.Action != "list_dirs" {
		rootPath = in.Workdir
	}
	root, rel, err := s.filesystemRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if in.Action != "list_dirs" {
		workspace, err := root.OpenRoot(rel)
		if err != nil {
			return nil, err
		}
		defer workspace.Close()
		root, rel = workspace, filepath.Clean(filepath.FromSlash(in.Path))
	}
	if in.Action == "write_text" || in.Action == "mkdir" || in.Action == "rename" || in.Action == "delete" {
		s.filesystemMu.Lock()
		defer s.filesystemMu.Unlock()
		return mutateFilesystem(root, rel, in)
	}
	if in.Action == "read_text" || in.Action == "read_preview" {
		metadata, err := root.Stat(rel)
		if err != nil {
			return nil, err
		}
		if !metadata.Mode().IsRegular() {
			return nil, errors.New("file is not regular text")
		}
		file, err := root.Open(rel)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("file is not regular text")
		}
		if in.PreviewOffset != nil {
			return filesystemTextPreview(file, info, in.Path, *in.PreviewOffset)
		}
		limit := filesystemReadLimit
		if in.Action == "read_preview" {
			limit = filesystemAssetReadLimit
		}
		if info.Size() > int64(limit) {
			return nil, fmt.Errorf("file exceeds %d MiB", limit>>20)
		}
		body, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
		if err != nil {
			return nil, err
		}
		if len(body) > limit {
			return nil, fmt.Errorf("file exceeds %d MiB", limit>>20)
		}
		hash := sha256.Sum256(body)
		if in.Action == "read_preview" {
			contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(in.Path)))
			if contentType == "" {
				contentType = http.DetectContentType(body)
			}
			return map[string]any{"path": filepath.ToSlash(filepath.Clean(in.Path)), "data": base64.StdEncoding.EncodeToString(body), "mime_type": contentType, "mtime_ms": info.ModTime().UnixMilli(), "content_hash": hex.EncodeToString(hash[:]), "size_bytes": len(body)}, nil
		}
		if !utf8.Valid(body) || strings.ContainsRune(string(body), 0) {
			return nil, errors.New("file is not UTF-8 text")
		}
		lines := 0
		if len(body) > 0 {
			lines = strings.Count(string(body), "\n")
			if body[len(body)-1] != '\n' {
				lines++
			}
		}
		return map[string]any{"path": filepath.ToSlash(filepath.Clean(in.Path)), "content": string(body), "mtime_ms": info.ModTime().UnixMilli(), "content_hash": hex.EncodeToString(hash[:]), "size_bytes": len(body), "total_lines": lines}, nil
	}
	base, err := root.OpenRoot(rel)
	if err != nil {
		return nil, err
	}
	defer base.Close()
	if in.MaxResults == 0 {
		in.MaxResults = 200
	}
	if in.MaxResults > 10000 || in.Offset > 100000 || in.Depth > 20 {
		return nil, errors.New("filesystem listing exceeds limits")
	}
	if in.Action == "list_dirs" {
		file, err := base.Open(".")
		if err != nil {
			return nil, err
		}
		defer file.Close()
		entries, err := file.ReadDir(10001)
		if err != nil && err != io.EOF {
			return nil, err
		}
		if len(entries) > 10000 {
			return nil, errors.New("directory exceeds 10000 entries")
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		out := []map[string]string{}
		truncated := false
		for _, entry := range entries {
			if entry.IsDir() {
				if len(out) >= int(in.MaxResults) {
					truncated = true
					break
				}
				out = append(out, map[string]string{"name": entry.Name(), "path": filepath.Join(path, entry.Name())})
			}
		}
		return map[string]any{"path": path, "entries": out, "truncated": truncated}, nil
	}
	if in.Depth == 0 {
		in.Depth = 1
	}
	entries := []map[string]any{}
	err = fs.WalkDir(base.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := r.Context().Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if len(entries) >= 10000 {
			return errors.New("filesystem tree exceeds 10000 entries; narrow path or depth")
		}
		hidden := strings.HasPrefix(entry.Name(), ".")
		if hidden && !in.ShowHidden {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		kind := "file"
		if entry.IsDir() {
			kind = "dir"
		}
		entries = append(entries, map[string]any{"path": filepath.ToSlash(filepath.Join(in.Path, name)), "kind": kind, "hidden": hidden})
		if entry.IsDir() && uint32(strings.Count(name, "/")+1) >= in.Depth {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	total := uint32(len(entries))
	start := min(in.Offset, total)
	end := min(start+in.MaxResults, total)
	return map[string]any{"path": in.Path, "has_path": in.Path != "", "depth": in.Depth, "offset": in.Offset, "max_results": in.MaxResults, "total": total, "has_more": end < total, "entries": entries[start:end]}, nil
}
