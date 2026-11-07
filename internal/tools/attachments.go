package tools

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
)

type Result struct {
	Text      string
	Parts     []ai.ContentPart
	Failed    bool
	Cancelled bool
	// ExitCode is the exit status of the command a shell tool (bash, Bash,
	// ManagedProcess, ProcessWait, ProcessStop) ran or observed, 0 included; nil
	// when the tool runs no command, or the command did not exit on its own
	// (it timed out, was cancelled or killed, or is still running).
	ExitCode *int
	// TimedOut marks a command the tool killed when its timeout passed.
	TimedOut bool
	// Duration is the tool's own wall-clock time, without time spent waiting for
	// the user's permission; zero when the tool did not run. Display only: the
	// model sees Text alone.
	Duration time.Duration
	// SandboxDenial is set when the OS sandbox most likely caused a shell
	// command to fail; the same explanation is appended to Text.
	SandboxDenial *sandbox.Denial
	// QuestionAnswers is what the user answered a question tool call, one
	// entry per question (see ReportQuestionAnswers). Display only.
	QuestionAnswers []ai.QuestionAnswer
	// Elicitations are the forms MCP servers asked the user to fill during
	// the call (see ReportElicitation). Display only.
	Elicitations []ai.Elicitation
}

type attachmentKey struct{}

type attachments struct {
	mu      sync.Mutex
	enabled bool
	closed  bool
	parts   []ai.ContentPart
}

func ExecuteResult(ctx context.Context, ts []Tool, name string, args json.RawMessage, vision bool) Result {
	hooks, _ := ctx.Value(reviewKey{}).(ReviewHooks)
	if hooks.Before != nil {
		var err error
		ctx, err = hooks.Before(ctx, name, args)
		if err != nil {
			return Result{Text: "Permission denied: " + err.Error(), Failed: true, Cancelled: ctx.Err() != nil}
		}
	}
	a := &attachments{enabled: vision}
	defer a.close()
	callCtx := context.WithValue(ctx, attachmentKey{}, a)
	result := executeResult(callCtx, ts, name, args)
	parts := a.close()
	if ctx.Err() != nil {
		parts = nil
	}
	result.Parts = parts
	if hooks.After != nil {
		result = hooks.After(ctx, name, args, result)
	}
	return result
}

func (a *attachments) close() []ai.ContentPart {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	parts := a.parts
	a.parts = nil
	return parts
}

// AttachImage attaches an image of the given extension (png, jpg, gif, webp) to the current
// tool result when the model supports vision.
func AttachImage(ctx context.Context, ext string, data []byte) bool {
	a, _ := ctx.Value(attachmentKey{}).(*attachments)
	if a == nil || len(data) == 0 || ctx.Err() != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.enabled || a.closed || ctx.Err() != nil {
		return false
	}
	ext, data = ai.NormalizeImage(ext, data)
	a.parts = append(a.parts, ai.ImagePart(ext, data))
	return true
}

func AttachScreenshot(ctx context.Context, jpeg []byte) bool {
	a, _ := ctx.Value(attachmentKey{}).(*attachments)
	if a == nil || len(jpeg) == 0 || ctx.Err() != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.enabled || a.closed || ctx.Err() != nil {
		return false
	}
	ext, data := ai.NormalizeImage("jpg", jpeg)
	a.parts = append(a.parts, ai.ImagePart(ext, data))
	return true
}
