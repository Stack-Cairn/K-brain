package tools

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

type Result struct {
	Text      string
	Parts     []ai.ContentPart
	Failed    bool
	Cancelled bool
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
	result := executeResult(callCtx, ts, name, args)
	parts := a.close()
	if ctx.Err() != nil {
		parts = nil
	}
	result.Parts = parts
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
