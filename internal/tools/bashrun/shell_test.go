package bashrun

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestShellCommand(t *testing.T) {
	for _, tt := range []struct {
		name  string
		shell string
		args  []string
	}{
		{"posix", "sh", []string{"-c", "echo hello"}},
		{"cmd", "cmd.exe", []string{"/D", "/S", "/C", "echo hello"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := shellCommand(t.Context(), tt.shell, "echo hello")
			if err != nil || !reflect.DeepEqual(cmd.Args[1:], tt.args) {
				t.Fatalf("cmd=%v err=%v", cmd, err)
			}
		})
	}
}

func TestPowerShellEncoding(t *testing.T) {
	command := "Write-Output '中文 and \"quotes\"';\nexit 7"
	for _, shell := range []string{"powershell", "pwsh", "powershell7"} {
		t.Run(shell, func(t *testing.T) {
			cmd, err := shellCommand(t.Context(), shell, command)
			if err != nil {
				t.Fatal(err)
			}
			data, err := base64.StdEncoding.DecodeString(cmd.Args[len(cmd.Args)-1])
			if err != nil {
				t.Fatal(err)
			}
			words := make([]uint16, len(data)/2)
			for i := range words {
				words[i] = binary.LittleEndian.Uint16(data[i*2:])
			}
			if got := string(utf16.Decode(words)); !strings.Contains(got, command) {
				t.Fatalf("command changed: %q", got)
			}
		})
	}
}

func TestShellOverride(t *testing.T) {
	t.Setenv("K_BRAIN_SHELL", "k-brain-shell-that-does-not-exist")
	if DefaultShell() != "k-brain-shell-that-does-not-exist" {
		t.Fatal("K_BRAIN_SHELL ignored")
	}
	res := Run(context.Background(), Options{Command: "echo test"})
	if res.Exit == "" {
		t.Fatal("missing shell should fail")
	}
}
