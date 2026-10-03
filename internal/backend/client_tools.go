package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// A client tool call waits for the client to run the action (for example a browser action that
// can load a page and take a screenshot). Generous, but bounded so a vanished client cannot hang
// the run forever.
const defaultClientToolTimeout = 5 * time.Minute

const maxClientTools = 32

var clientToolNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

type clientToolWaiter struct {
	request    protocol.ClientToolRequest
	ctx        context.Context
	done       chan struct{}
	resolution *protocol.ClientToolResolution
	images     []protocol.ClientToolImage
}

// normalizeClientTools validates client-declared tools. A name may not shadow a backend tool:
// the backend implementation would silently win and the client tool would never run.
func normalizeClientTools(in []protocol.ClientTool, available []tools.Tool) ([]protocol.ClientTool, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxClientTools {
		return nil, fmt.Errorf("options.client_tools allows at most %d tools", maxClientTools)
	}
	seen := map[string]bool{}
	out := make([]protocol.ClientTool, 0, len(in))
	for i, tool := range in {
		if !clientToolNamePattern.MatchString(tool.Name) {
			return nil, fmt.Errorf("options.client_tools[%d].name is invalid", i)
		}
		if seen[tool.Name] || tool.Name == "ExitPlanMode" {
			return nil, fmt.Errorf("options.client_tools contains duplicate or reserved tool %q", tool.Name)
		}
		for _, existing := range available {
			if existing.Def.Function.Name == tool.Name {
				return nil, fmt.Errorf("options.client_tools[%d] %q collides with a backend tool", i, tool.Name)
			}
		}
		seen[tool.Name] = true
		tool.Description = strings.TrimSpace(tool.Description)
		if tool.Description == "" {
			return nil, fmt.Errorf("options.client_tools[%d].description is required", i)
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.Parameters, &schema); err != nil || schema["type"] != "object" {
			return nil, fmt.Errorf("options.client_tools[%d].parameters must be a JSON object schema", i)
		}
		out = append(out, tool)
	}
	return out, nil
}

// clientToolDefs exposes client tool names for option validation (policies may reference them).
func clientToolDefs(in []protocol.ClientTool) []tools.Tool {
	out := make([]tools.Tool, 0, len(in))
	for _, tool := range in {
		out = append(out, tools.Tool{Def: ai.NewTool(tool.Name, tool.Description, string(tool.Parameters))})
	}
	return out
}

// clientTools builds the run-scoped tools whose execution is delegated to the client.
func (s *Server) clientTools(rt *runtimeSession, in []protocol.ClientTool) []tools.Tool {
	out := make([]tools.Tool, 0, len(in))
	for _, def := range in {
		name := def.Name
		tool := tools.Tool{NoInherit: true, Def: ai.NewTool(name, def.Description, string(def.Parameters))}
		tool.Run = func(ctx context.Context, args json.RawMessage) (string, error) {
			runID, _ := ctx.Value(questionRunKey{}).(string)
			if runID == "" {
				return "", fmt.Errorf("%s requires an active backend run", name)
			}
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			resolution, images, err := s.waitClientTool(ctx, rt, runID, tools.ToolCallID(ctx), name, args)
			if err != nil {
				return "", err
			}
			for _, image := range images {
				data, decodeErr := base64.StdEncoding.DecodeString(image.Data)
				if decodeErr == nil {
					tools.AttachImage(ctx, strings.TrimPrefix(image.MimeType, "image/"), data)
				}
			}
			if resolution.IsError {
				return "", errors.New(resolution.Text)
			}
			return resolution.Text, nil
		}
		out = append(out, tool)
	}
	return out
}

func (s *Server) waitClientTool(ctx context.Context, rt *runtimeSession, runID, toolCallID, name string, args json.RawMessage) (*protocol.ClientToolResolution, []protocol.ClientToolImage, error) {
	if isUnattendedRun(ctx) {
		return nil, nil, fmt.Errorf("%s needs the client and is unavailable in unattended runs", name)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	waiter := &clientToolWaiter{ctx: ctx, done: make(chan struct{}), request: protocol.ClientToolRequest{
		CallID: newRunID(), ToolCallID: toolCallID, RunID: runID, Tool: name, Arguments: args,
		DeadlineAt: time.Now().Add(defaultClientToolTimeout).UnixMilli(),
	}}
	rt.mu.Lock()
	if rt.runID != runID || rt.runDone || rt.deleted {
		rt.mu.Unlock()
		return nil, nil, errors.New("client tool run is no longer active")
	}
	if rt.clientTools == nil {
		rt.clientTools = make(map[string]*clientToolWaiter)
	}
	if err := rt.publishEventLocked(runID, protocol.EventClientToolRequested, waiter.request); err != nil {
		rt.mu.Unlock()
		return nil, nil, err
	}
	rt.clientTools[waiter.request.CallID] = waiter
	rt.mu.Unlock()
	timer := time.NewTimer(time.Until(time.UnixMilli(waiter.request.DeadlineAt)))
	defer timer.Stop()
	select {
	case <-waiter.done:
	case <-timer.C:
	case <-ctx.Done():
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	defer delete(rt.clientTools, waiter.request.CallID)
	if waiter.resolution == nil {
		state := "timeout"
		if ctx.Err() != nil {
			state = "cancelled"
		}
		if err := rt.settleClientToolLocked(waiter, protocol.ClientToolResultRequest{}, state); err != nil {
			return nil, nil, err
		}
	}
	if waiter.resolution.Cancelled {
		return nil, nil, context.Canceled
	}
	return waiter.resolution, waiter.images, nil
}

func (rt *runtimeSession) settleClientToolLocked(waiter *clientToolWaiter, in protocol.ClientToolResultRequest, state string) error {
	if waiter.resolution != nil {
		return nil
	}
	request := waiter.request
	result := protocol.ClientToolResolution{CallID: request.CallID, ToolCallID: request.ToolCallID, RunID: request.RunID, Tool: request.Tool}
	switch state {
	case "timeout":
		result.TimedOut, result.IsError = true, true
		result.Text = fmt.Sprintf("%s did not return a result in time; the client may be disconnected.", request.Tool)
	case "cancelled":
		result.Cancelled, result.IsError = true, true
		result.Text = "The user stopped the turn before the tool finished."
	default:
		result.Text, result.IsError, result.ImageCount = in.Text, in.IsError, len(in.Images)
		waiter.images = in.Images
	}
	// Images go to the model through the tool result; the journal keeps only their count.
	if err := rt.publishEventLocked(result.RunID, protocol.EventClientToolResolved, result); err != nil {
		return err
	}
	waiter.resolution = &result
	close(waiter.done)
	return nil
}

func (s *Server) resolveClientTool(w http.ResponseWriter, r *http.Request, id, callID string) {
	var in protocol.ClientToolResultRequest
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	if in.ConversationID != id || in.RunID == "" {
		writeJSONError(w, http.StatusConflict, "client tool identity mismatch")
		return
	}
	for _, image := range in.Images {
		if !strings.HasPrefix(image.MimeType, "image/") {
			writeJSONError(w, http.StatusBadRequest, "client tool images require an image/* mime_type")
			return
		}
		if _, err := base64.StdEncoding.DecodeString(image.Data); err != nil {
			writeJSONError(w, http.StatusBadRequest, "client tool image data must be base64")
			return
		}
	}
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	waiter := rt.clientTools[callID]
	if waiter == nil || waiter.request.RunID != in.RunID {
		writeJSONError(w, http.StatusConflict, "client tool call is not pending")
		return
	}
	if waiter.ctx.Err() != nil || time.Now().UnixMilli() >= waiter.request.DeadlineAt {
		writeJSONError(w, http.StatusConflict, "client tool call is no longer pending")
		return
	}
	if err := rt.settleClientToolLocked(waiter, in, "resolved"); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
