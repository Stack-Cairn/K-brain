package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSecretCommandHelper(t *testing.T) {
	mode := os.Getenv("K_BRAIN_SECRET_COMMAND_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "args":
		for i, arg := range os.Args {
			if arg == "--" {
				_ = json.NewEncoder(os.Stdout).Encode(os.Args[i+1:])
				os.Exit(0)
			}
		}
	case "failure":
		fmt.Fprintln(os.Stdout, "private-output")
		fmt.Fprintln(os.Stderr, "private-stderr")
		os.Exit(9)
	case "timeout":
		time.Sleep(30 * time.Second)
	case "overflow":
		for {
			fmt.Fprint(os.Stdout, strings.Repeat("s", 4096))
		}
	case "limit":
		fmt.Fprint(os.Stdout, strings.Repeat("s", maxSecretCommandOutput))
	}
	os.Exit(0)
}

func secretHelperCommand(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return `!"` + exe + `" -test.run=^TestSecretCommandHelper$ --`
}

func TestSecretCommandHonorsCallerDeadline(t *testing.T) {
	t.Setenv("K_BRAIN_SECRET_COMMAND_HELPER", "timeout")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	out, err := (Provider{APIKey: secretHelperCommand(t)}).ResolveKeyContext(ctx)
	if out != "" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller deadline lost: %q, %v", out, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("credential command waited for its own timeout instead of the caller deadline")
	}
}

func TestMCPSecretsHonorCallerDeadline(t *testing.T) {
	t.Setenv("K_BRAIN_SECRET_COMMAND_HELPER", "timeout")
	for _, name := range []string{"header", "environment"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			var err error
			if name == "header" {
				var value string
				value, err = ResolveHeaderContext(ctx, secretHelperCommand(t))
				if value != "" {
					t.Fatalf("canceled header returned a value: %q", value)
				}
			} else {
				var env map[string]string
				env, err = ResolveEnvMapContext(ctx, map[string]string{"TOKEN": secretHelperCommand(t)})
				if env != nil {
					t.Fatal("canceled environment returned partial values")
				}
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("caller deadline lost: %v", err)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("MCP credentials ignored caller deadline")
			}
		})
	}
}

func TestSecretCommandQuotedArguments(t *testing.T) {
	t.Setenv("K_BRAIN_SECRET_COMMAND_HELPER", "args")
	command := secretHelperCommand(t) + ` "two words" '' '中文值' 'C:\private keys\token' 'literal $TOKEN; | & >'`
	out, err := ResolveSecret(command)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"two words", "", "中文值", `C:\private keys\token`, "literal $TOKEN; | & >"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestSecretCommandExecutableWithSpaces(t *testing.T) {
	t.Setenv("K_BRAIN_SECRET_COMMAND_HELPER", "args")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	name := "secret helper"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(out, in)
	if err := errors.Join(copyErr, out.Close()); err != nil {
		t.Fatal(err)
	}
	value, err := ResolveSecret(`!"` + path + `" -test.run=^TestSecretCommandHelper$ -- "token with spaces"`)
	if err != nil || value != `["token with spaces"]` {
		t.Fatalf("quoted executable: %q, %v", value, err)
	}
}

func TestSecretCommandFailuresAndBounds(t *testing.T) {
	for _, mode := range []string{"failure", "timeout", "overflow", "limit"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("K_BRAIN_SECRET_COMMAND_HELPER", mode)
			start := time.Now()
			value, err := ResolveSecret(secretHelperCommand(t) + " private-argument")
			switch mode {
			case "limit":
				if err != nil || len(value) != maxSecretCommandOutput {
					t.Fatalf("exact limit: length=%d, err=%v", len(value), err)
				}
				return
			case "failure":
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 9 {
					t.Fatalf("exit error = %v", err)
				}
			case "timeout":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("deadline error = %v", err)
				}
			case "overflow":
				if !errors.Is(err, errSecretOutputLimit) {
					t.Fatalf("overflow error = %v", err)
				}
			}
			if value != "" || err == nil || strings.Contains(err.Error(), "private-") {
				t.Fatalf("failed command leaked secret data: %q, %v", value, err)
			}
			if time.Since(start) > SecretCmdTimeout+3*time.Second {
				t.Fatal("command exceeded its deadline")
			}
		})
	}
	for _, command := range []string{"!", "!  ", `!""`, `!tool "private-unclosed`} {
		if value, err := ResolveSecret(command); err == nil || value != "" || strings.Contains(err.Error(), "private-") {
			t.Fatalf("invalid command returned %q, %v", value, err)
		}
	}
}
