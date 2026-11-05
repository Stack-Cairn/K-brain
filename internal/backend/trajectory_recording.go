package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

type trajectorySection struct {
	SectionID string `json:"sectionId"`
	Slot      string `json:"slot"`
	Content   string `json:"content"`
}
type trajectoryRequestKey struct{}
type trajectoryRequestState struct {
	ID, TaskID, ParentTaskID, Provider, Model string
	mu                                        sync.Mutex
	headers                                   map[string]bool
	origin                                    string
}
type trajectoryObserver struct {
	rt    *runtimeSession
	runID string
}

func sectionDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "s_" + hex.EncodeToString(sum[:8])
}
func (rt *runtimeSession) storeTrajectorySection(slot, content string) (string, error) {
	id := sectionDigest(content)
	dir := filepath.Join(rt.eventDir, "sections", rt.id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	data, err := json.Marshal(trajectorySection{SectionID: id, Slot: slot, Content: content})
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, id+".json")
	if _, err := os.Stat(path); err == nil {
		return id, nil
	}
	tmp, err := os.CreateTemp(dir, ".section-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err = tmp.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return id, nil
}

func (o *trajectoryObserver) Start(ctx context.Context, in agent.RequestObservation) (context.Context, error) {
	state := &trajectoryRequestState{ID: newRunID(), TaskID: in.TaskID, ParentTaskID: in.ParentTaskID, Provider: in.Provider, Model: in.Request.Model, headers: map[string]bool{}}
	if parsed, err := url.Parse(in.Endpoint); err == nil && parsed.Host != "" {
		state.origin = parsed.Scheme + "://" + parsed.Host
	}
	refs := make([]any, 7)
	var system []string
	for _, message := range in.Request.Messages {
		if message.Role == "system" || message.Role == "developer" {
			system = append(system, message.TextContent())
		}
	}
	if len(system) > 0 {
		id, err := o.rt.storeTrajectorySection("base", strings.Join(system, "\n\n"))
		if err != nil {
			return ctx, err
		}
		refs[0] = id
	}
	if in.Request.Tools != nil {
		catalog := make([]any, 0, len(in.Request.Tools))
		for _, tool := range in.Request.Tools {
			catalog = append(catalog, tool.Function)
		}
		data, err := json.Marshal(catalog)
		if err != nil {
			return ctx, err
		}
		id, err := o.rt.storeTrajectorySection("toolCatalog", string(data))
		if err != nil {
			return ctx, err
		}
		refs[5] = id
	}
	userID := ""
	for i := len(in.Request.Messages) - 1; i >= 0; i-- {
		if in.Request.Messages[i].Role == "user" {
			userID = in.Request.Messages[i].ID
			break
		}
	}
	_, err := o.publish("trajectory.request.started", map[string]any{"request_id": state.ID, "task_id": state.TaskID, "parent_task_id": state.ParentTaskID, "provider": state.Provider, "model": state.Model, "sections": refs, "user_message_id": userID, "parent_tool_call_id": in.ToolCallID}, "")
	if err != nil {
		return ctx, err
	}
	ctx = context.WithValue(ctx, trajectoryRequestKey{}, state)
	ctx = ai.WithRuntimeDiagnosticObserver(ctx, func(event ai.RuntimeDiagnostic) {
		state.mu.Lock()
		if event.Provider != "" {
			state.Provider = event.Provider
		}
		if parsed, err := url.Parse(event.Endpoint); err == nil && parsed.Host != "" {
			state.origin = parsed.Scheme + "://" + parsed.Host
		}
		state.mu.Unlock()
		if event.Kind == "failover" {
			_, _ = o.publish("trajectory.failover", map[string]any{"request_id": state.ID, "task_id": state.TaskID, "from": event.From, "to": event.To, "attempt": event.Attempt, "target_index": event.TargetIndex, "error": event.Error}, "")
		}
	})
	trace := &httptrace.ClientTrace{
		WroteHeaderField: func(name string, _ []string) { state.mu.Lock(); state.headers[name] = true; state.mu.Unlock() },
		WroteHeaders: func() {
			state.mu.Lock()
			names := make([]string, 0, len(state.headers))
			for name := range state.headers {
				names = append(names, name)
			}
			state.headers = map[string]bool{}
			provider, origin := state.Provider, state.origin
			state.mu.Unlock()
			sort.Strings(names)
			_, _ = o.publish("trajectory.transport", map[string]any{"request_id": state.ID, "task_id": state.TaskID, "provider": provider, "origin": origin, "header_names": names}, "")
		},
	}
	return httptrace.WithClientTrace(ctx, trace), nil
}
func (o *trajectoryObserver) End(ctx context.Context, message ai.Message, usage ai.Usage, err error) {
	state, _ := ctx.Value(trajectoryRequestKey{}).(*trajectoryRequestState)
	if state == nil {
		return
	}
	state.mu.Lock()
	provider := state.Provider
	state.mu.Unlock()
	if state.TaskID != "" {
		o.rt.addSubagentUsage(state.TaskID, protocol.FromAIUsage(&usage))
	}
	payload := map[string]any{"request_id": state.ID, "task_id": state.TaskID, "provider": provider, "model": state.Model, "stop_reason": message.StopReason, "usage": protocol.FromAIUsage(&usage), "status": "complete"}
	if err != nil {
		payload["status"] = "error"
		payload["error"] = err.Error()
	}
	_, _ = o.publish("trajectory.request.completed", payload, "")
}
func (o *trajectoryObserver) Retry(ctx context.Context, event ai.RetryEvent) {
	state, _ := ctx.Value(trajectoryRequestKey{}).(*trajectoryRequestState)
	if state == nil {
		return
	}
	message := ""
	if event.Err != nil {
		message = event.Err.Error()
	}
	state.mu.Lock()
	provider := state.Provider
	state.mu.Unlock()
	_, _ = o.publish("trajectory.retry", map[string]any{"request_id": state.ID, "task_id": state.TaskID, "provider": provider, "attempt": event.Attempt, "max": event.Max, "delay_ms": event.Delay.Milliseconds(), "error": message}, "")
}

func (rt *runtimeSession) trajectoryChildEvents(id string) agent.Events {
	rt.mu.Lock()
	runID := rt.trajectoryTaskRunLocked(id)
	rt.mu.Unlock()
	return rt.trajectoryChildEventsForRun(id, runID)
}

func (rt *runtimeSession) trajectoryTaskRunLocked(id string) string {
	events, err := rt.allEventsLocked()
	if err != nil {
		return rt.runID
	}
	for _, event := range events {
		if event.Type != protocol.EventSubagentStarted {
			continue
		}
		var payload protocol.SubagentEvent
		if json.Unmarshal(event.Payload, &payload) == nil && payload.Subagent.ID == id {
			return event.RunID
		}
	}
	return rt.runID
}
func trajectoryRequestPayload(e protocol.Event) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(e.Payload, &p)
	return p
}
func trajectoryString(p map[string]any, key string) string { value, _ := p[key].(string); return value }
func validSectionID(id string) bool {
	if len(id) != 18 || !strings.HasPrefix(id, "s_") {
		return false
	}
	_, err := hex.DecodeString(id[2:])
	return err == nil
}
func (s *Server) readTrajectorySection(conversationID, id string) (trajectorySection, error) {
	var section trajectorySection
	if !validSectionID(id) {
		return section, fmt.Errorf("invalid section id")
	}
	data, err := os.ReadFile(filepath.Join(s.eventDir, "sections", conversationID, id+".json"))
	if err != nil {
		return section, err
	}
	if err = json.Unmarshal(data, &section); err != nil {
		return section, err
	}
	if section.SectionID != id || sectionDigest(section.Content) != id {
		return section, fmt.Errorf("trajectory section digest mismatch")
	}
	return section, nil
}

func (o *trajectoryObserver) ChildStart(ctx context.Context, info agent.ChildInfo, stop func()) {
	o.rt.subagent(info.ID, o.runID)
	o.rt.setSubagentRunning(info.ID, true, stop)
	_, _ = o.publish(protocol.EventSubagentStarted, protocol.SubagentEvent{Subagent: protocol.Subagent{ID: info.ID, Description: info.Description, Status: string(agent.TaskRunning), Model: protocol.ModelRef{Provider: info.Provider, Model: info.Model}, ParentToolCallID: info.ParentToolCallID, StartedAt: ptrTime(time.Now().UTC())}}, "")
}
func (o *trajectoryObserver) ChildEnd(ctx context.Context, info agent.ChildInfo, result agent.ChildResult) {
	o.rt.flushSubagent(info.ID)
	status, kind, message := string(agent.TaskDone), protocol.EventSubagentCompleted, ""
	switch {
	case result.Stopped:
		status, kind, message = string(agent.TaskCancelled), protocol.EventSubagentFailed, result.Err.Error()
	case result.Err != nil:
		status, kind, message = string(agent.TaskError), protocol.EventSubagentFailed, result.Err.Error()
	}
	ended := time.Now().UTC()
	view := protocol.Subagent{ID: info.ID, Description: info.Description, Status: status, Error: message, Model: protocol.ModelRef{Provider: info.Provider, Model: info.Model}, ParentToolCallID: info.ParentToolCallID, EndedAt: &ended}
	o.rt.subagentStats(info.ID, &view)
	if usage := protocol.FromAIUsage(&result.Usage); usage != nil && *usage != (protocol.Usage{}) {
		view.Usage = usage
	}
	if started := o.rt.subagentStartedAt(info.ID); !started.IsZero() {
		view.StartedAt = &started
		view.DurationMS = ended.Sub(started).Milliseconds()
	}
	_, _ = o.publish(kind, protocol.SubagentEvent{Subagent: view}, "")
	o.rt.setSubagentRunning(info.ID, false, nil)
}
func (o *trajectoryObserver) ChildEvents(id string) agent.Events {
	return o.rt.trajectoryChildEventsForRun(id, o.runID)
}

func (o *trajectoryObserver) publish(kind string, payload any, parent string) (protocol.Event, error) {
	return o.rt.publishTrajectory(o.runID, kind, payload, parent)
}
func (rt *runtimeSession) publishTrajectory(runID, kind string, payload any, parent string) (protocol.Event, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.deleted {
		return protocol.Event{}, session.ErrNotFound
	}
	if runID == "" {
		runID = rt.runID
	}
	event, err := protocol.NewEvent(rt.nextSeq+1, rt.id, runID, kind, payload)
	if err != nil {
		return event, err
	}
	event.ParentRunID = parent
	if err = rt.persistEvent(rt.eventDir, event); err != nil {
		return event, err
	}
	rt.nextSeq = event.Seq
	rt.appendEventLocked(event)
	old := rt.changed
	rt.changed = make(chan struct{})
	close(old)
	return event, nil
}
