package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
)

type runOutput struct {
	mu       sync.Mutex
	writer   io.Writer
	json     bool
	cancel   context.CancelFunc
	err      error
	finished bool
	wrote    bool
}

func (o *runOutput) writeLocked(data []byte) {
	if o.err != nil {
		return
	}
	n, err := o.writer.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		o.err = fmt.Errorf("stdout: %w", err)
		if o.cancel != nil {
			o.cancel()
		}
	}
	o.wrote = o.wrote || n > 0
}

func (o *runOutput) emitLocked(event map[string]string) {
	data, err := json.Marshal(event)
	if err != nil {
		o.err = fmt.Errorf("json output: %w", err)
		if o.cancel != nil {
			o.cancel()
		}
		return
	}
	o.writeLocked(append(data, '\n'))
}

func (o *runOutput) emit(event map[string]string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.finished && o.err == nil {
		o.emitLocked(event)
	}
}

func (o *runOutput) text(delta string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.finished && o.err == nil {
		o.writeLocked([]byte(delta))
	}
}

func (o *runOutput) finish(final string, reason ai.StopReason, runErr error) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished {
		return o.err
	}
	o.finished = true
	if o.err != nil {
		return o.err
	}
	if o.json {
		if runErr != nil {
			o.emitLocked(map[string]string{"type": "error", "error": runErr.Error()})
		} else {
			o.emitLocked(map[string]string{"type": "done", "text": final, "stopReason": string(reason)})
		}
	} else if o.wrote || runErr == nil {
		o.writeLocked([]byte{'\n'})
	}
	return o.err
}

func (o *runOutput) events(note func(string, ...any)) agent.Events {
	if !o.json {
		return agent.Events{
			OnText: o.text,
			OnToolStart: func(_, name, _ string) {
				note("⚒ %s", name)
			},
		}
	}
	return agent.Events{
		OnText: func(delta string) {
			o.emit(map[string]string{"type": "text", "delta": delta})
		},
		OnThink: func(delta string) {
			o.emit(map[string]string{"type": "reasoning", "delta": delta})
		},
		OnToolStart: func(id, name, args string) {
			o.emit(map[string]string{"type": "tool_start", "id": id, "name": name, "args": args})
		},
		OnToolEnd: func(id, name, result string) {
			o.emit(map[string]string{"type": "tool_end", "id": id, "name": name, "result": result})
		},
		OnToolOutput: func(id, output string) {
			o.emit(map[string]string{"type": "tool_output", "id": id, "output": output})
		},
	}
}
