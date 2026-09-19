package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
)

type fileLocks struct {
	mu     sync.Mutex
	locks  map[string]*pathLock
	global chan struct{}
}

type pathLock struct {
	ch   chan struct{}
	refs int
}

func newFileLocks() *fileLocks {
	return &fileLocks{
		locks:  map[string]*pathLock{},
		global: make(chan struct{}, 1),
	}
}

func (f *fileLocks) acquirePath(ctx context.Context, path string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := canonicalPathKey(path)
	f.mu.Lock()
	l, ok := f.locks[key]
	if !ok {
		l = &pathLock{ch: make(chan struct{}, 1)}
		f.locks[key] = l
	}
	l.refs++
	f.mu.Unlock()
	drop := func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		l.refs--
		if l.refs == 0 {
			delete(f.locks, key)
		}
	}
	release, err := acquireLock(ctx, l.ch)
	if err != nil {
		drop()
		return nil, err
	}
	return sync.OnceFunc(func() { release(); drop() }), nil
}

func (f *fileLocks) acquireGlobal(ctx context.Context) (func(), error) {
	return acquireLock(ctx, f.global)
}

func acquireLock(ctx context.Context, ch chan struct{}) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case ch <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-ch
			return nil, err
		}
		return sync.OnceFunc(func() { <-ch }), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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
