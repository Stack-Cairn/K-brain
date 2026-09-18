package driver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/computer"
)

func TestDesktopMissingInterpreter(t *testing.T) {
	t.Setenv("K_BRAIN_COMPUTER_PYTHON", "k-brain-nonexistent-python-782184")
	for _, platform := range []string{"darwin", "linux"} {
		var status computer.TCCStatus
		if err := callDesktop(t.Context(), platform, "permissions.status", nil, &status); err != nil {
			t.Fatal(err)
		}
		if status.Accessibility || status.ScreenRecording || status.Hint == "" {
			t.Fatal(status)
		}
		var apps []computer.RunningApp
		var f *Fault
		err := callDesktop(t.Context(), platform, "apps", nil, &apps)
		if !errors.As(err, &f) || f.Code != 6 {
			t.Fatal(err)
		}
	}
}

func TestWindowsReadOnlyBackend(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("K_BRAIN_TEST_DESKTOP") != "1" {
		t.Skip("opt in with K_BRAIN_TEST_DESKTOP=1")
	}
	b := NewBackend()
	var apps []computer.RunningApp
	if err := b.Call(t.Context(), "apps", nil, &apps); err != nil {
		t.Fatal(err)
	}
	var status computer.TCCStatus
	if err := b.Call(t.Context(), "permissions.status", nil, &status); err != nil {
		t.Fatal(err)
	}
	p := Params{}
	put(p, "app", "k-brain-nonexistent-app-782184")
	var state Snapshot
	var f *Fault
	err := b.Call(t.Context(), "state", p, &state)
	if !errors.As(err, &f) || (f.Code != 1 && f.Code != 7) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = b.Call(ctx, "apps", nil, &apps)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestUnicodeParams(t *testing.T) {
	p := Params{}
	value := "中文 '+^%{}()[]\\\"\n"
	put(p, "text", value)
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Params
	if err = json.Unmarshal(b, &decoded); err != nil || text(decoded, "text") != value {
		t.Fatal(string(b), err)
	}
}
