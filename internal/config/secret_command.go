package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/commandline"
)

const maxSecretCommandOutput = 64 << 10

var errSecretOutputLimit = errors.New("secret command output exceeds 64 KiB")

type secretCommandOutput struct {
	buffer  bytes.Buffer
	cancel  context.CancelFunc
	limited bool
}

func (b *secretCommandOutput) Write(p []byte) (int, error) {
	if len(p) > maxSecretCommandOutput-b.buffer.Len() {
		b.limited = true
		b.cancel()
		return 0, errSecretOutputLimit
	}
	return b.buffer.Write(p)
}

func resolveSecretCommand(parent context.Context, command string) (string, error) {
	args, err := commandline.Split(command)
	if err != nil {
		return "", fmt.Errorf("secret command: %w", err)
	}
	if len(args) == 0 || args[0] == "" {
		return "", errors.New("secret reference: empty command")
	}
	ctx, cancel := context.WithTimeout(parent, SecretCmdTimeout)
	defer cancel()
	output := &secretCommandOutput{cancel: cancel}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout, cmd.Stderr = output, io.Discard
	cmd.WaitDelay = 250 * time.Millisecond
	err = cmd.Run()
	if output.limited {
		return "", errSecretOutputLimit
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("secret command: %w", ctx.Err())
	}
	if err != nil {
		return "", fmt.Errorf("secret command: %w", err)
	}
	return strings.TrimSpace(output.buffer.String()), nil
}
