package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func liveagentTerminalSession(ctx context.Context, manager *TerminalManager, args map[string]any) (string, error) {
	if err := Authorize(ctx, "TerminalSession", laString(args, "command")); err != nil {
		return "", err
	}
	id, err := manager.Start(ctx, laString(args, "command"), WorkingDir(ctx))
	if err != nil {
		return "", err
	}
	reportProcessStarted(ctx, id)
	return fmt.Sprintf("TerminalSession started\nsession_id=%s\n\nRead output with ReadTerminal(session_id=\"%s\").", id, id), nil
}

func liveagentReadTerminal(ctx context.Context, manager *TerminalManager, args map[string]any) (string, error) {
	id := strings.TrimSpace(laString(args, "session_id"))
	if err := AuthorizePoll(ctx, "ReadTerminal", id); err != nil {
		return "", err
	}
	if id == "" {
		return "", errors.New("session_id is required")
	}
	maxBytes := laInt(args, "max_bytes", terminalDefaultTail)
	output, err := manager.Read(ctx, id, maxBytes)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ReadTerminal session_id=%s\n\n%s", id, output), nil
}

func terminalTool(name string, manager *TerminalManager) Tool {
	definition := liveagentDefinitions[name]
	return Tool{Def: definition, Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
		args, err := liveagentArguments(definition, raw)
		if err != nil {
			return "", err
		}
		if name == "TerminalSession" {
			return liveagentTerminalSession(ctx, manager, args)
		}
		return liveagentReadTerminal(ctx, manager, args)
	}}
}
