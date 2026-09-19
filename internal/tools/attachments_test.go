package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestAttachmentsAreScopedToExecution(t *testing.T) {
	var late context.Context
	child := Tool{Def: ai.NewTool("child", "", `{}`), Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		late = ctx
		AttachScreenshot(ctx, []byte("child"))
		return "child", nil
	}}
	parent := Tool{Def: ai.NewTool("parent", "", `{}`), Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		AttachScreenshot(ctx, []byte("parent"))
		for _, vision := range []bool{true, false} {
			result := ExecuteResult(ctx, []Tool{child}, "child", nil, vision)
			if (len(result.Parts) == 1) != vision {
				t.Fatalf("child vision gate: %+v", result)
			}
		}
		return "partial output", errors.New("failed after capture")
	}}
	result := ExecuteResult(t.Context(), []Tool{parent}, "parent", nil, true)
	if len(result.Parts) != 1 || result.Parts[0].ImageURL.URL != ai.ImagePart("jpg", []byte("parent")).ImageURL.URL {
		t.Fatalf("child screenshot leaked: %+v", result)
	}
	if !strings.HasPrefix(result.Text, "Error: failed after capture") || !strings.Contains(result.Text, "partial output") {
		t.Fatalf("partial output lost: %q", result.Text)
	}
	if AttachScreenshot(late, []byte("late")) || AttachScreenshot(t.Context(), []byte("unscoped")) {
		t.Fatal("accepted image outside an active tool execution")
	}
}

func TestAttachmentCancellationAndParallelCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	tool := Tool{Def: ai.NewTool("capture", "", `{}`), Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		var wg sync.WaitGroup
		for range 16 {
			wg.Go(func() { AttachScreenshot(ctx, []byte("screenshot")) })
		}
		wg.Wait()
		return "captured", nil
	}}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			result := ExecuteResult(t.Context(), []Tool{tool}, "capture", nil, true)
			if len(result.Parts) != 16 {
				t.Errorf("parallel images lost or mixed: %d", len(result.Parts))
			}
		})
	}
	wg.Wait()
	original := tool.Run
	tool.Run = func(ctx context.Context, args json.RawMessage) (string, error) {
		out, err := original(ctx, args)
		cancel()
		if AttachScreenshot(ctx, []byte("cancelled")) {
			t.Error("accepted image after cancellation")
		}
		return out, err
	}
	if result := ExecuteResult(ctx, []Tool{tool}, "capture", nil, true); len(result.Parts) != 0 {
		t.Fatal("cancelled call retained attachments")
	}
}
