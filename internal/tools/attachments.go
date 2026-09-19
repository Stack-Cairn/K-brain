package tools

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

type Result struct {
	Text  string
	Parts []ai.ContentPart
}

type attachmentKey struct{}

type attachments struct {
	mu      sync.Mutex
	enabled bool
	closed  bool
	parts   []ai.ContentPart
}

func ExecuteResult(ctx context.Context, ts []Tool, name string, args json.RawMessage, vision bool) Result {
	a := &attachments{enabled: vision}
	defer a.close()
	callCtx := context.WithValue(ctx, attachmentKey{}, a)
	out := Execute(callCtx, ts, name, args)
	parts := a.close()
	if ctx.Err() != nil {
		parts = nil
	}
	return Result{Text: out, Parts: parts}
}

func (a *attachments) close() []ai.ContentPart {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	parts := a.parts
	a.parts = nil
	return parts
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
