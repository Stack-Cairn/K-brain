package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func liveagentManagedProcess(ctx context.Context, manager *liveagentProcessManager, a map[string]any) (string, error) {
	action := strings.ToLower(strings.TrimSpace(laString(a, "action")))
	switch action {
	case "start":
		return liveagentProcessStart(ctx, manager, a)
	case "status":
		id := strings.TrimSpace(laString(a, "process_id"))
		pollID := id
		if pollID == "" {
			pollID = "status"
		}
		if err := AuthorizePoll(ctx, "ManagedProcess", pollID); err != nil {
			return "", err
		}
		if id == "" {
			manager.mu.Lock()
			processes := make([]*liveagentProcess, 0, len(manager.processes))
			for _, p := range manager.processes {
				processes = append(processes, p)
			}
			manager.mu.Unlock()
			if len(processes) == 0 {
				return "ManagedProcess status count=0", nil
			}
			var lines []string
			for _, p := range processes {
				if _, err := manager.getOwned(ctx, p.id); err != nil {
					continue
				}
				lines = append(lines, liveagentProcessText("status", p.snapshot(), "", 0, 0, false, false))
			}
			return fmt.Sprintf("ManagedProcess status count=%d\n\n%s", len(lines), strings.Join(lines, "\n\n")), nil
		}
		p, err := manager.getOwned(ctx, id)
		if err != nil {
			return "", err
		}
		return liveagentProcessText("status", p.snapshot(), "", 0, 0, false, false), nil
	case "read_log":
		if err := AuthorizePoll(ctx, "ManagedProcess", strings.TrimSpace(laString(a, "process_id"))); err != nil {
			return "", err
		}
		p, err := manager.getOwned(ctx, laString(a, "process_id"))
		if err != nil {
			return "", err
		}
		maxBytes := int64(laInt(a, "max_bytes", int(liveagentProcessDefaultLog)))
		if maxBytes < 1 {
			maxBytes = 1
		}
		if maxBytes > liveagentProcessMaxLog {
			maxBytes = liveagentProcessMaxLog
		}
		out, cursor, truncated, bytes, err := manager.read(p, 0, maxBytes)
		if err != nil {
			return "", err
		}
		return liveagentProcessText("read_log", p.snapshot(), out, cursor, bytes, truncated, false), nil
	case "wait":
		return liveagentProcessWaitWithTool(ctx, manager, a, "ManagedProcess")
	case "stop":
		return liveagentProcessStopWithTool(ctx, manager, a, "ManagedProcess")
	default:
		return "", errors.New("ManagedProcess.action must be one of: start, status, read_log, wait, stop")
	}
}

func liveagentProcessWaitWithTool(ctx context.Context, manager *liveagentProcessManager, a map[string]any, tool string) (string, error) {
	id := strings.TrimSpace(laString(a, "process_id"))
	if id == "" {
		id = strings.TrimSpace(laString(a, "session_id"))
	}
	if err := AuthorizePoll(ctx, tool, id); err != nil {
		return "", err
	}
	return liveagentProcessWait(ctx, manager, a)
}

func liveagentProcessWait(ctx context.Context, manager *liveagentProcessManager, a map[string]any) (string, error) {
	id := strings.TrimSpace(laString(a, "process_id"))
	if id == "" {
		id = strings.TrimSpace(laString(a, "session_id"))
	}

	if id == "" {
		return "", errors.New("process_id is required")
	}
	p, err := manager.getOwned(ctx, id)
	if err != nil {
		return "", err
	}
	yield, maxBytes := liveagentProcessWaitBounds(a)
	cursor := int64(laInt(a, "cursor", 0))
	out, next, truncated, bytes, timedOut, err := manager.wait(ctx, p, cursor, yield, maxBytes)
	if err != nil {
		return "", err
	}
	return liveagentProcessText("wait", p.snapshot(), out, next, bytes, truncated, timedOut), nil
}

func liveagentProcessStopWithTool(ctx context.Context, manager *liveagentProcessManager, a map[string]any, tool string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	id := strings.TrimSpace(laString(a, "process_id"))
	if id == "" {
		id = strings.TrimSpace(laString(a, "session_id"))
	}
	if id == "" {
		return "", errors.New("process_id is required")
	}
	if deny := checkGate(ctx, tool, id); deny != "" {
		return "", errors.New(deny)
	}
	p, stopped, err := manager.stopOwned(ctx, id)
	if err != nil {
		return "", err
	}
	cursor := int64(laInt(a, "cursor", 0))
	out, next, truncated, bytes, readErr := manager.read(p, cursor, liveagentProcessMaxLog)
	if readErr != nil {
		return "", readErr
	}
	snap := p.snapshot()
	return liveagentProcessText(fmt.Sprintf("stop stopped=%t", stopped), snap, out, next, bytes, truncated, false), nil
}
