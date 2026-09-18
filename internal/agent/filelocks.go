package agent

import (
	"encoding/json"
	"path/filepath"
	"sync"
)

type fileLocks struct {
	mu     sync.Mutex
	locks  map[string]chan struct{}
	global chan struct{}
}

func newFileLocks() *fileLocks {
	return &fileLocks{
		locks:  map[string]chan struct{}{},
		global: make(chan struct{}, 1),
	}
}

func (f *fileLocks) acquirePath(path string) func() {
	key := canonicalPathKey(path)
	f.mu.Lock()
	ch, ok := f.locks[key]
	if !ok {
		ch = make(chan struct{}, 1)
		f.locks[key] = ch
	}
	f.mu.Unlock()
	ch <- struct{}{}
	return func() { <-ch }
}

func (f *fileLocks) acquireGlobal() func() {
	f.global <- struct{}{}
	return func() { <-f.global }
}

func canonicalPathKey(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func toolMutationPath(toolName, args string) (string, bool) {
	switch toolName {
	case "write", "edit":
		var a struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(args), &a); err == nil && a.Path != "" {
			return a.Path, true
		}
	}
	return "", false
}
