package editor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/commandline"
)

const MaxDraftSize = 1 << 20

func Command(path string) (*exec.Cmd, error) {
	value := strings.TrimSpace(os.Getenv("VISUAL"))
	if value == "" {
		value = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if value == "" {
		value = "vi"
		if runtime.GOOS == "windows" {
			value = "notepad.exe"
		}
	}
	var args []string
	if info, err := os.Stat(value); err == nil && !info.IsDir() {
		args = []string{value}
	} else {
		var err error
		args, err = commandline.Split(value)
		if err != nil {
			return nil, fmt.Errorf("editor command: %w", err)
		}
	}
	if len(args) == 0 || args[0] == "" {
		return nil, errors.New("empty editor command")
	}
	cmd := exec.CommandContext(context.Background(), args[0], append(args[1:], path)...)
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	return cmd, nil
}

func Prepare(text string) (*exec.Cmd, string, error) {
	if len(text) > MaxDraftSize {
		return nil, "", errors.New("draft exceeds 1 MiB")
	}
	f, err := os.CreateTemp("", "k-brain-prompt-*.md")
	if err != nil {
		return nil, "", err
	}
	path := f.Name()
	_, writeErr := f.WriteString(text)
	closeErr := f.Close()
	if err = errors.Join(writeErr, closeErr); err != nil {
		os.Remove(path)
		return nil, "", err
	}
	cmd, err := Command(path)
	if err != nil {
		os.Remove(path)
		return nil, "", err
	}
	return cmd, path, nil
}

func Read(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("edited draft is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxDraftSize+1))
	if err != nil {
		return "", err
	}
	if len(data) > MaxDraftSize {
		return "", errors.New("edited draft exceeds 1 MiB")
	}
	if !utf8.Valid(data) {
		return "", errors.New("edited draft must be UTF-8")
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	return strings.ReplaceAll(text, "\r\n", "\n"), nil
}
