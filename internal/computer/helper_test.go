package computer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fakeHelper(t *testing.T, script string) *Helper {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-helper"+func() string {
		if runtime.GOOS == "windows" {
			return ".exe"
		}
		return ""
	}())
	src := fakeHelperSource
	if script != "" {

		src = strings.Replace(src, "func init() {}", "func init() {\n"+script+"\n}", 1)
	}

	main := "package main\n\n" + src
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fakehelper\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if out, err := runGo(t, dir, "build", "-o", bin, "."); err != nil {
		t.Fatalf("build fake helper: %v\n%s", err, out)
	}
	t.Setenv("K_BRAIN_COMPUTER_BIN", bin)
	h := &Helper{}
	if err := h.spawn(); err != nil {
		t.Fatalf("spawn fake helper: %v", err)
	}
	t.Cleanup(h.kill)
	return h
}

func goBin(t *testing.T) string {
	t.Helper()
	if goroot, err := runCmd("", "go", "env", "GOROOT"); err == nil {
		goroot = strings.TrimSpace(goroot)
		cand := filepath.Join(goroot, "bin", "go")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return "go"
}

func runGo(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	return runCmd(dir, goBin(t), args...)
}

func TestHandshakeAndCall(t *testing.T) {
	h := fakeHelper(t, `handle = func(req map[string]any) (any, *rpcErr) {
		if req["method"] == "ping" {
			return map[string]any{"pong": true}, nil
		}
		return nil, &rpcErr{Code: -32601, Message: "nope"}
	}`)
	var out struct {
		Pong bool `json:"pong"`
	}
	if err := h.Call(context.Background(), "ping", nil, &out); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if !out.Pong {
		t.Fatal("want pong")
	}

	err := h.Call(context.Background(), "bogus", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("want rpc error, got %v", err)
	}
}

func TestVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-helper"+func() string {
		if runtime.GOOS == "windows" {
			return ".exe"
		}
		return ""
	}())
	src := "package main\n\n" + strings.Replace(fakeHelperSource, `versionLine = "k-brain-computer/1"`, `versionLine = "k-brain-computer/0"`, 1)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fakehelper\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runGo(t, dir, "build", "-o", bin, "."); err != nil {
		t.Fatalf("build fake helper: %v\n%s", err, out)
	}
	t.Setenv("K_BRAIN_COMPUTER_BIN", bin)
	h := &Helper{}
	err := h.spawn()
	if err == nil || !strings.Contains(err.Error(), "protocol mismatch") {
		t.Fatalf("want protocol mismatch, got %v", err)
	}
}

func TestStaleGenerationMapping(t *testing.T) {
	h := fakeHelper(t, `handle = func(req map[string]any) (any, *rpcErr) {
		return nil, &rpcErr{Code: 4, Message: "state changed since generation 1"}
	}`)
	err := h.Call(context.Background(), "click", nil, nil)
	if _, ok := errors.AsType[*StaleError](err); !ok {
		t.Fatalf("want StaleError, got %T: %v", err, err)
	}
}

func TestTokenEnforced(t *testing.T) {
	h := fakeHelper(t, `handle = func(req map[string]any) (any, *rpcErr) {
		params, _ := req["params"].(map[string]any)
		tok, _ := params["token"].(string)
		if tok == "" {
			return nil, &rpcErr{Code: 8, Message: "missing token"}
		}
		return map[string]any{"ok": true}, nil
	}`)
	var out map[string]any
	if err := h.Call(context.Background(), "anything", nil, &out); err != nil {
		t.Fatalf("token-bearing call must pass: %v", err)
	}
}

func TestCrashRestart(t *testing.T) {

	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-helper"+func() string {
		if runtime.GOOS == "windows" {
			return ".exe"
		}
		return ""
	}())
	src := "package main\n\n" + strings.Replace(fakeHelperSource, "func init() {}",
		`func init() {
		handle = func(req map[string]any) (any, *rpcErr) {
			if req["method"] == "boom" {
				os.Exit(1)
			}
			return map[string]any{"ok": true}, nil
		}
		}`, 1)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fakehelper\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runGo(t, dir, "build", "-o", bin, "."); err != nil {
		t.Fatalf("build fake helper: %v\n%s", err, out)
	}
	t.Setenv("K_BRAIN_COMPUTER_BIN", bin)
	h := &Helper{}
	if err := h.spawn(); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer h.kill()
	var out map[string]any

	if err := h.Call(context.Background(), "boom", nil, &out); err != nil {

		t.Logf("boom errored as expected: %v", err)
	}

	if err := h.Call(context.Background(), "ok-call", nil, &out); err != nil {
		t.Fatalf("post-restart call: %v", err)
	}
}

func TestSharedSpawnCacheAndReset(t *testing.T) {

	fakeHelper(t, "").kill()
	ResetShared()
	t.Cleanup(ResetShared)
	h1, err := Shared()
	if err != nil {
		t.Fatalf("Shared: %v", err)
	}
	h2, err := Shared()
	if err != nil || h2 != h1 {
		t.Fatalf("Shared must cache the helper: %v %p %p", err, h1, h2)
	}
	var out map[string]any
	if err := h1.Call(context.Background(), "ping", nil, &out); err != nil {
		t.Fatalf("shared helper call: %v", err)
	}
	ResetShared()
	h3, err := Shared()
	if err != nil {
		t.Fatalf("Shared after reset: %v", err)
	}
	if h3 == h1 {
		t.Fatal("ResetShared must drop the cached helper")
	}
}

func TestSharedCachesSpawnError(t *testing.T) {
	t.Setenv("K_BRAIN_COMPUTER_BIN", filepath.Join(t.TempDir(), "does-not-exist"))
	ResetShared()
	t.Cleanup(ResetShared)
	_, err1 := Shared()
	if err1 == nil {
		t.Fatal("Shared must fail for a missing binary")
	}
	_, err2 := Shared()
	if !errors.Is(err2, err1) {
		t.Fatalf("spawn error must be cached, got %v then %v", err1, err2)
	}
}

func TestScreenshotDecode(t *testing.T) {
	var nilShot *Screenshot
	if b, err := nilShot.Decode(); b != nil || err != nil {
		t.Errorf("nil screenshot: %v %v", b, err)
	}
	if b, err := (&Screenshot{}).Decode(); b != nil || err != nil {
		t.Errorf("empty screenshot: %v %v", b, err)
	}
	b, err := (&Screenshot{JPEGBase64: "aGVsbG8="}).Decode()
	if err != nil || string(b) != "hello" {
		t.Errorf("decode: %q %v", b, err)
	}
	if _, err := (&Screenshot{JPEGBase64: "!!!"}).Decode(); err == nil {
		t.Error("bad base64 must error")
	}
}

func TestStaleErrorMessage(t *testing.T) {
	if got := (&StaleError{Msg: "state changed"}).Error(); got != "state changed" {
		t.Errorf("StaleError.Error: %q", got)
	}
}

func TestMutationTransportFailureIsNotReplayed(t *testing.T) {
	h := fakeHelper(t, `handle = func(req map[string]any) (any, *rpcErr) { os.Exit(0); return nil, nil }`)
	err := h.Call(t.Context(), "click", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("got %v", err)
	}
	if h.cmd != nil {
		t.Fatal("mutation transport failure must close helper without replay")
	}
}

func TestCanceledCallDoesNotSend(t *testing.T) {
	h := fakeHelper(t, "")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	id := h.nextID
	if err := h.Call(ctx, "click", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if h.nextID != id {
		t.Fatal("canceled call was sent")
	}
}
