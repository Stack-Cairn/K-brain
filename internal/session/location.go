package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func FindTranscript(root, id string) (string, error) {
	if !validID(id) {
		return "", fmt.Errorf("invalid session id %q", id)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	paths := []string{filepath.Join(root, id, "session.jsonl")}
	for _, entry := range entries {
		if entry.IsDir() && validProjectDir(entry.Name()) {
			paths = append(paths, filepath.Join(root, entry.Name(), id, "session.jsonl"))
		}
	}
	var found string
	for _, path := range paths {
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if found != "" {
			return "", fmt.Errorf("session id %q is ambiguous", id)
		}
		found = path
	}
	if found == "" {
		return "", ErrNotFound
	}
	return found, nil
}
