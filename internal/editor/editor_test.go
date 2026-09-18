package editor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandAndPrepare(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", `"`+exe+`" --wait "two words"`)
	t.Setenv("EDITOR", "missing-editor")
	cmd, path, err := Prepare("你好\nsecond line")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if cmd.Args[0] != exe || cmd.Args[1] != "--wait" || cmd.Args[2] != "two words" || cmd.Args[3] != path {
		t.Fatal(cmd.Args)
	}
	text, err := Read(path)
	if err != nil || text != "你好\nsecond line" {
		t.Fatalf("%q %v", text, err)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", exe)
	if _, err := Command(path); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", `"unclosed`)
	if _, _, err := Prepare("keep"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestReadValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "draft.md")
	for _, tc := range []struct {
		name string
		data []byte
		want string
		fail bool
	}{
		{"crlf", []byte("\ufeff你好\r\nworld"), "你好\nworld", false},
		{"empty", nil, "", false},
		{"invalid", []byte{0xff}, "", true},
		{"large", []byte(strings.Repeat("x", MaxDraftSize+1)), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := Read(path)
			if (err != nil) != tc.fail || (!tc.fail && got != tc.want) {
				t.Fatalf("read length=%d err=%v", len(got), err)
			}
		})
	}
	if _, err := Read(t.TempDir()); err == nil {
		t.Fatal("directory accepted")
	}
	if _, _, err := Prepare(strings.Repeat("x", MaxDraftSize+1)); err == nil {
		t.Fatal("large draft accepted")
	}
}

func TestEditorProcessHelper(t *testing.T) {
	if os.Getenv("K_BRAIN_EDITOR_TEST_HELPER") != "1" {
		return
	}
	if err := os.WriteFile(os.Args[len(os.Args)-1], []byte("edited 中文\r\n"), 0600); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestEditorProcessRoundTrip(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("K_BRAIN_EDITOR_TEST_HELPER", "1")
	t.Setenv("VISUAL", `"`+exe+`" -test.run=^TestEditorProcessHelper$ --`)
	cmd, path, err := Prepare("initial")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, output)
	}
	text, err := Read(path)
	if err != nil || text != "edited 中文\n" {
		t.Fatalf("%q %v", text, err)
	}
}
