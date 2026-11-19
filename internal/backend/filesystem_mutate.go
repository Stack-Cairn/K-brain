package backend

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func mutateFilesystem(root *os.Root, path string, in filesystemInput) (any, error) {
	if path == "." {
		return nil, errors.New("cannot modify workspace root")
	}
	info, err := root.Lstat(path)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if exists && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("cannot modify a symbolic link")
	}
	kind := "file"
	if exists && info.IsDir() {
		kind = "dir"
	}
	switch in.Action {
	case "mkdir":
		if err := root.Mkdir(path, 0755); err != nil {
			return nil, err
		}
		return map[string]any{"path": in.Path, "kind": "dir"}, nil
	case "delete":
		// Remove is deliberately nonrecursive; a populated directory needs explicit handling.
		if err := root.Remove(path); err != nil {
			return nil, err
		}
		return map[string]any{"path": in.Path, "kind": kind}, nil
	case "rename":
		target := filepath.Clean(filepath.FromSlash(in.ToPath))
		if in.ToPath == "" || target == "." || filepath.IsAbs(target) || strings.Contains(in.ToPath, "\\") || target == ".." || strings.HasPrefix(target, ".."+string(filepath.Separator)) {
			return nil, errors.New("rename destination must be workspace-relative")
		}
		if _, err := root.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("rename destination exists or is inaccessible")
		}
		if err := renameFilesystemExclusive(root, path, target); err != nil {
			return nil, err
		}
		return map[string]any{"from_path": in.Path, "path": in.ToPath, "kind": kind}, nil
	case "write_text":
		if in.Mode != "" && in.Mode != "rewrite" {
			return nil, errors.New("write mode only supports rewrite")
		}
		if !utf8.ValidString(in.Content) || strings.ContainsRune(in.Content, 0) || len(in.Content) > filesystemReadLimit {
			return nil, errors.New("write requires UTF-8 text within 16 MiB")
		}
		if (in.ExpectedHash == nil) != (in.ExpectedMtime == nil) {
			return nil, errors.New("expected modification time and content hash must be supplied together")
		}
		mode := os.FileMode(0644)
		if exists {
			if !info.Mode().IsRegular() || info.Size() > filesystemReadLimit {
				return nil, errors.New("target is not editable regular text")
			}
			if in.ExpectedHash == nil {
				return nil, errors.New("file must be read before overwrite")
			}
			if err := checkFilesystemVersion(root, path, in); err != nil {
				return nil, err
			}
			mode = info.Mode().Perm()
		} else if in.ExpectedHash != nil {
			return nil, errors.New("file changed since read: target was removed")
		}
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		temporary := filepath.Join(filepath.Dir(path), ".kbrain-write-"+hex.EncodeToString(nonce[:]))
		file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return nil, err
		}
		defer root.Remove(temporary)
		if _, err := io.WriteString(file, in.Content); err != nil {
			file.Close()
			return nil, err
		}
		if err := file.Sync(); err != nil {
			file.Close()
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
		if exists {
			if err := checkFilesystemVersion(root, path, in); err != nil {
				return nil, err
			}
			if err := root.Rename(temporary, path); err != nil {
				return nil, err
			}
		} else {
			// Hard-link creation is exclusive, so a concurrent creator is not overwritten.
			if err := root.Link(temporary, path); err != nil {
				return nil, err
			}
		}
		updated, err := root.Stat(path)
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256([]byte(in.Content))
		lines := strings.Count(in.Content, "\n")
		if in.Content != "" && !strings.HasSuffix(in.Content, "\n") {
			lines++
		}
		return map[string]any{"path": in.Path, "mode": "rewrite", "existed_before": exists, "bytes_written": len(in.Content), "mtime_ms": updated.ModTime().UnixMilli(), "content_hash": hex.EncodeToString(hash[:]), "total_lines": lines}, nil
	}
	return nil, errors.New("unsupported filesystem mutation")
}

func checkFilesystemVersion(root *os.Root, path string, in filesystemInput) error {
	info, err := root.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > filesystemReadLimit || info.ModTime().UnixMilli() != *in.ExpectedMtime {
		return errors.New("file changed since read; reload before saving")
	}
	file, err := root.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, filesystemReadLimit+1))
	if err != nil {
		return err
	}
	hash := sha256.Sum256(body)
	if len(body) > filesystemReadLimit || hex.EncodeToString(hash[:]) != *in.ExpectedHash {
		return errors.New("file changed since read; reload before saving")
	}
	return nil
}
