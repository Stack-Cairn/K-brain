package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// replyQueue answers each model request with the next scripted reply and records the
// last user message the model saw, including request-only snapshots.
type replyQueue struct {
	scriptedClient
	mu       sync.Mutex
	replies  []string
	seen     []string
	requests [][]ai.Message
	block    chan struct{}
}

func (c *replyQueue) Clone() ai.Client { return c }

func (c *replyQueue) Stream(ctx context.Context, request ai.Request, onText, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.mu.Lock()
	c.requests = append(c.requests, append([]ai.Message(nil), request.Messages...))
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if request.Messages[i].Role == "user" {
			c.seen = append(c.seen, request.Messages[i].Content)
			break
		}
	}
	reply := "still working"
	if len(c.replies) > 0 {
		reply, c.replies = c.replies[0], c.replies[1:]
	}
	block := c.block
	c.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ai.Message{}, ai.Usage{}, ctx.Err()
		}
	}
	if onText != nil {
		onText(reply)
	}
	return ai.Message{Role: "assistant", Content: reply, StopReason: ai.StopReasonStop}, ai.Usage{}, nil
}

func (c *replyQueue) userTurns() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen...)
}

func isolateUserDir(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func newGoalServer(t *testing.T, client ai.Client) (*session.Store, string) {
	t.Helper()
	isolateUserDir(t)
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, newGoalServerWithStore(t, store, client)
}

func newGoalServerWithStore(t *testing.T, store *session.Store, client ai.Client) string {
	t.Helper()
	_, url := newGoalBackend(t, store, client, t.TempDir(), t.TempDir())
	return url
}

// newGoalBackend also returns the server; backends sharing eventDir and store model a
// restart of the same installation.
func newGoalBackend(t *testing.T, store *session.Store, client ai.Client, eventDir, defaultCWD string) (*Server, string) {
	t.Helper()
	factory := func(_ context.Context, cwd string, selected protocol.ModelRef) (*agent.Agent, error) {
		ag := agent.New(client, selected.Model, 1024, "system")
		ag.ModelName, ag.Provider, ag.WorkingDir = selected.Model, selected.Provider, cwd
		return ag, nil
	}
	backendServer, err := New(Options{
		Store: store, Factory: factory, EventDir: eventDir, DefaultCWD: defaultCWD,
		Models: []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(backendServer)
	t.Cleanup(func() {
		httpServer.Close()
		_ = backendServer.Close()
	})
	return backendServer, httpServer.URL
}

func newGoalTestServer(t *testing.T, client ai.Client) (*Server, *session.Store, string) {
	t.Helper()
	isolateUserDir(t)
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server, base := newGoalBackend(t, store, client, t.TempDir(), t.TempDir())
	return server, store, base
}

func waitTurns(t *testing.T, client *replyQueue, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(client.userTurns()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("model saw %d turns, want %d", len(client.userTurns()), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// readSSEUntil collects events from a live stream until ctx ends it.
func readSSEUntil(ctx context.Context, resp *http.Response) []protocol.Event {
	defer resp.Body.Close()
	var events []protocol.Event
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for ctx.Err() == nil && scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event protocol.Event
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) == nil {
			events = append(events, event)
		}
	}
	return events
}

func postGoal(t *testing.T, base, id string, in protocol.GoalRequest) (int, protocol.GoalState) {
	t.Helper()
	var out protocol.GoalState
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+id+"/goal", in, nil)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, out
}

func firstUserMessage(t *testing.T, store *session.Store, id string) string {
	t.Helper()
	_, msgs, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range msgs {
		if message.Role == "user" {
			return message.Content
		}
	}
	return ""
}

func getGoal(t *testing.T, base, id string) protocol.GoalState {
	t.Helper()
	resp, err := http.Get(base + "/v1/sessions/" + id + "/goal")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out protocol.GoalState
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func waitGoal(t *testing.T, base, id string, done func(protocol.GoalState) bool) protocol.GoalState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		state := getGoal(t, base, id)
		if done(state) {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("goal did not settle: %+v", state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// goalEvents replays the session log; the stream closes after each run's terminal
// event, so it reconnects from the last sequence until nothing new arrives.
func goalEvents(t *testing.T, base, id string) []protocol.GoalState {
	t.Helper()
	var all []protocol.Event
	after := int64(0)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/sessions/"+id+"/events?after_seq="+strconv.FormatInt(after, 10), nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		batch := readSSEUntil(ctx, resp)
		cancel()
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		after = batch[len(batch)-1].Seq
	}
	var out []protocol.GoalState
	for _, event := range all {
		if event.Type != protocol.EventGoalUpdated {
			continue
		}
		var state protocol.GoalState
		_ = json.Unmarshal(event.Payload, &state)
		out = append(out, state)
	}
	return out
}
func TestGoalRunsUntilMetAndKeepsInstructionsOutOfHistory(t *testing.T) {
	client := &replyQueue{replies: []string{"first pass", "more work", "GOAL_MET — shipped and verified"}}
	store, base := newGoalServer(t, client)
	sess := createTestSession(t, base)

	status, state := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "ship the feature", Run: &protocol.PromptRequest{ClientRequestID: "goal-1"}})
	if status != http.StatusOK || state.Status != "active" || state.Run == nil || state.Run.RunID == "" {
		t.Fatalf("set: %d %+v", status, state)
	}
	final := waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "met" })
	if final.Goal != "" {
		t.Fatalf("met goal must be cleared: %+v", final)
	}
	turns := client.userTurns()
	if len(turns) != 3 || !strings.HasPrefix(turns[0], "/goal ship the feature") || !strings.Contains(turns[0], "Work toward it now") {
		t.Fatalf("model turns: %q", turns)
	}
	if first := firstUserMessage(t, store, sess.ID); first != "/goal ship the feature" {
		t.Fatalf("history must keep the typed command: %q", first)
	}
	for _, turn := range turns[1:] {
		if !strings.Contains(turn, "[goal check]") || !strings.Contains(turn, "GOAL_MET") {
			t.Fatalf("continuation must carry the goal check: %q", turn)
		}
	}
	meta, msgs, err := store.Load(sess.ID)
	if err != nil || meta.Goal != "" {
		t.Fatalf("durable goal: %q %v", meta.Goal, err)
	}
	for _, message := range msgs {
		if message.Role == "user" && strings.Contains(message.Content, "[goal check]") {
			t.Fatalf("history must keep only the visible round text: %q", message.Content)
		}
	}
	var statuses []string
	for _, event := range goalEvents(t, base, sess.ID) {
		statuses = append(statuses, event.Status)
		if event.Status == "continuing" && (!strings.HasPrefix(event.NextClientRequestID, "goal-"+sess.ID+"-") || event.NextPrompt == "") {
			t.Fatalf("continuing event must name the next round: %+v", event)
		}
	}
	if strings.Join(statuses, ",") != "active,continuing,continuing,met" {
		t.Fatalf("goal events: %v", statuses)
	}
}

func TestGoalPausesAtRoundLimitAndResumes(t *testing.T) {
	client := &replyQueue{}
	_, base := newGoalServer(t, client)
	sess := createTestSession(t, base)
	if err := config.SetProjectGoalMaxRounds(sess.CWD, 2); err != nil {
		t.Fatal(err)
	}
	postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "never done", Run: &protocol.PromptRequest{ClientRequestID: "g-1"}})
	paused := waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "paused" })
	if paused.Reason != "max_rounds" || paused.Round != 2 || paused.MaxRounds != 2 || paused.Goal != "never done" {
		t.Fatalf("paused: %+v", paused)
	}
	if n := len(client.userTurns()); n != 3 {
		t.Fatalf("goal + 2 checks expected, got %d", n)
	}

	client.mu.Lock()
	client.replies = []string{"GOAL_MET done"}
	client.mu.Unlock()
	status, resumed := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "resume", Run: &protocol.PromptRequest{ClientRequestID: "g-2"}})
	if status != http.StatusOK || resumed.Status != "active" || resumed.Round != 0 {
		t.Fatalf("resume: %d %+v", status, resumed)
	}
	waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "met" })
	turns := client.userTurns()
	if last := turns[len(turns)-1]; !strings.HasPrefix(last, "/goal resume") || !strings.Contains(last, "never done") || !strings.Contains(last, "[goal check]") {
		t.Fatalf("resume round: %q", last)
	}
}

func TestGoalStopPausesAndClearDropsGoal(t *testing.T) {
	client := &replyQueue{block: make(chan struct{})}
	store, base := newGoalServer(t, client)
	sess := createTestSession(t, base)
	_, state := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "long task", Run: &protocol.PromptRequest{ClientRequestID: "s-1"}})
	if status, _ := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "other", Run: &protocol.PromptRequest{ClientRequestID: "s-x"}}); status != http.StatusConflict {
		t.Fatalf("set while running: %d", status)
	}
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/runs/"+state.Run.RunID+"/cancel", map[string]any{}, nil)
	resp.Body.Close()
	paused := waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "paused" })
	if paused.Reason != "cancelled" {
		t.Fatalf("stop must pause, not continue: %+v", paused)
	}
	time.Sleep(100 * time.Millisecond)
	// The stop may land before the first model call; either way no round follows it.
	if n := len(client.userTurns()); n > 1 {
		t.Fatalf("no continuation after stop, got %d turns", n)
	}
	if status, cleared := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "clear"}); status != http.StatusOK || cleared.Status != "cleared" {
		t.Fatalf("clear: %d %+v", status, cleared)
	}
	if got := getGoal(t, base, sess.ID); got.Status != "none" || got.Goal != "" {
		t.Fatalf("after clear: %+v", got)
	}
	if meta, _, _ := store.Load(sess.ID); meta.Goal != "" {
		t.Fatalf("clear must drop the durable goal: %q", meta.Goal)
	}
	if status, _ := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "resume", Run: &protocol.PromptRequest{ClientRequestID: "s-2"}}); status != http.StatusConflict {
		t.Fatalf("resume without goal: %d", status)
	}
}

func TestGoalRestoredFromMetadataIsPaused(t *testing.T) {
	client := &replyQueue{}
	store, base := newGoalServer(t, client)
	sess := createTestSession(t, base)
	if err := store.SetGoal(sess.ID, "from the TUI"); err != nil {
		t.Fatal(err)
	}
	// A backend that loads the session after the metadata write sees a paused goal.
	got := getGoal(t, newGoalServerWithStore(t, store, client), sess.ID)
	if got.Status != "paused" || got.Reason != "restored" || got.Goal != "from the TUI" {
		t.Fatalf("restored: %+v", got)
	}
}

func TestGoalRejectsBadRequests(t *testing.T) {
	_, base := newGoalServer(t, &replyQueue{})
	sess := createTestSession(t, base)
	for _, in := range []protocol.GoalRequest{
		{Action: "nope"},
		{Action: "set", Goal: "x"},
		{Action: "set", Run: &protocol.PromptRequest{ClientRequestID: "r"}},
		{Action: "set", Goal: strings.Repeat("g", maxGoalBytes+1), Run: &protocol.PromptRequest{ClientRequestID: "r"}},
	} {
		if status, _ := postGoal(t, base, sess.ID, in); status != http.StatusBadRequest {
			t.Fatalf("%s: status %d", in.Action, status)
		}
	}
}

func TestGoalPauseStopsRoundAndUpdateKeepsPausedGoal(t *testing.T) {
	client := &replyQueue{block: make(chan struct{})}
	store, base := newGoalServer(t, client)
	sess := createTestSession(t, base)
	_, started := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "long task", Run: &protocol.PromptRequest{ClientRequestID: "p-1"}})
	if started.StartedAt == "" {
		t.Fatalf("started_at missing: %+v", started)
	}
	status, paused := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "pause"})
	if status != http.StatusOK || paused.Status != "paused" || paused.Reason != "user" {
		t.Fatalf("pause: %d %+v", status, paused)
	}
	// The cancelled round must not flip the goal back or start another round.
	time.Sleep(150 * time.Millisecond)
	if got := getGoal(t, base, sess.ID); got.Status != "paused" || got.Reason != "user" {
		t.Fatalf("after pause: %+v", got)
	}
	if n := len(client.userTurns()); n > 1 {
		t.Fatalf("no continuation after pause, got %d turns", n)
	}

	status, updated := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "update", Goal: "  shorter task  "})
	if status != http.StatusOK || updated.Goal != "shorter task" || updated.Status != "paused" {
		t.Fatalf("update: %d %+v", status, updated)
	}
	if meta, _, _ := store.Load(sess.ID); meta.Goal != "shorter task" {
		t.Fatalf("durable goal after update: %q", meta.Goal)
	}
	if status, _ := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "update"}); status != http.StatusBadRequest {
		t.Fatalf("empty update: %d", status)
	}

	close(client.block)
	client.mu.Lock()
	client.block = nil
	client.replies = []string{"GOAL_MET done"}
	client.mu.Unlock()
	postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "resume", Run: &protocol.PromptRequest{ClientRequestID: "p-2"}})
	waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "met" })
	turns := client.userTurns()
	if last := turns[len(turns)-1]; !strings.Contains(last, "shorter task") {
		t.Fatalf("resume must use the updated goal: %q", last)
	}
}

func TestGoalPauseAndUpdateNeedAGoal(t *testing.T) {
	_, base := newGoalServer(t, &replyQueue{})
	sess := createTestSession(t, base)
	for _, action := range []string{"pause", "update"} {
		if status, _ := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: action, Goal: "x"}); status != http.StatusConflict {
			t.Fatalf("%s without goal: %d", action, status)
		}
	}
}

func TestGoalClearStopsRunningRound(t *testing.T) {
	client := &replyQueue{block: make(chan struct{})}
	_, base := newGoalServer(t, client)
	sess := createTestSession(t, base)
	postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "long task", Run: &protocol.PromptRequest{ClientRequestID: "c-1"}})
	if status, _ := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "clear"}); status != http.StatusOK {
		t.Fatalf("clear status %d", status)
	}
	// The blocked round only returns once its context is cancelled.
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/goal", protocol.GoalRequest{Action: "set", Goal: "next", Run: &protocol.PromptRequest{ClientRequestID: "c-2"}}, nil)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("clear must stop the running round; last status %d", resp.StatusCode)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestGoalSetRetryReplaysWithoutTouchingGoal(t *testing.T) {
	client := &replyQueue{block: make(chan struct{}), replies: []string{"GOAL_MET done"}}
	store, base := newGoalServer(t, client)
	sess := createTestSession(t, base)
	req := protocol.GoalRequest{Action: "set", Goal: "ship", Run: &protocol.PromptRequest{ClientRequestID: "dup-1"}}
	_, first := postGoal(t, base, sess.ID, req)
	// A retry while the round runs replays it instead of conflicting.
	status, running := postGoal(t, base, sess.ID, req)
	if status != http.StatusOK || running.Run == nil || running.Run.RunID != first.Run.RunID || running.Status != "active" {
		t.Fatalf("retry while running: %d %+v", status, running)
	}
	close(client.block)
	waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "met" })
	before := len(goalEvents(t, base, sess.ID))

	status, retry := postGoal(t, base, sess.ID, req)
	if status != http.StatusOK || retry.Run == nil || retry.Run.RunID != first.Run.RunID || retry.Status != "met" || retry.Goal != "" {
		t.Fatalf("retry after met: %d %+v", status, retry)
	}
	time.Sleep(100 * time.Millisecond)
	if got := getGoal(t, base, sess.ID); got.Status != "met" || got.Goal != "" {
		t.Fatalf("retry must not resurrect the goal: %+v", got)
	}
	if meta, _, _ := store.Load(sess.ID); meta.Goal != "" {
		t.Fatalf("retry must not rewrite the durable goal: %q", meta.Goal)
	}
	if n := len(client.userTurns()); n != 1 {
		t.Fatalf("retry must not start a run, model saw %d turns", n)
	}
	if after := len(goalEvents(t, base, sess.ID)); after != before {
		t.Fatalf("retry must not publish goal events: %d -> %d", before, after)
	}
	if status, _ := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "other", Run: &protocol.PromptRequest{ClientRequestID: "dup-1"}}); status != http.StatusConflict {
		t.Fatalf("reused id with another goal: %d", status)
	}
}

func TestGoalUpdateDuringRoundDoesNotCompleteNewGoal(t *testing.T) {
	client := &replyQueue{block: make(chan struct{}), replies: []string{"GOAL_MET A is done", "GOAL_MET B is done"}}
	store, base := newGoalServer(t, client)
	sess := createTestSession(t, base)
	postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "goal A", Run: &protocol.PromptRequest{ClientRequestID: "u-1"}})
	waitTurns(t, client, 1)
	if status, st := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "update", Goal: "goal B"}); status != http.StatusOK || st.Goal != "goal B" {
		t.Fatalf("update: %d %+v", status, st)
	}
	client.mu.Lock()
	close(client.block)
	client.block = nil
	client.mu.Unlock()
	final := waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "met" })
	turns := client.userTurns()
	if len(turns) != 2 || !strings.Contains(turns[1], "goal B") || !strings.Contains(turns[1], "[goal check]") {
		t.Fatalf("the stale reply must lead to a check of the new goal: %q", turns)
	}
	if final.Round != 1 {
		t.Fatalf("met on the round for goal B: %+v", final)
	}
	if meta, _, _ := store.Load(sess.ID); meta.Goal != "" {
		t.Fatalf("durable goal after met: %q", meta.Goal)
	}
}

func TestGoalRoundIDsDoNotCollideAfterReload(t *testing.T) {
	isolateUserDir(t)
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	eventDir, cwd := t.TempDir(), t.TempDir()
	client := &replyQueue{}
	_, base := newGoalBackend(t, store, client, eventDir, cwd)
	sess := createTestSession(t, base)
	if err := config.SetProjectGoalMaxRounds(sess.CWD, 2); err != nil {
		t.Fatal(err)
	}
	postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "never done", Run: &protocol.PromptRequest{ClientRequestID: "r-1"}})
	waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "paused" })

	// A new backend over the same store and event log keeps the old run records.
	_, restarted := newGoalBackend(t, store, client, eventDir, cwd)
	if status, _ := postGoal(t, restarted, sess.ID, protocol.GoalRequest{Action: "resume", Run: &protocol.PromptRequest{ClientRequestID: "r-2"}}); status != http.StatusOK {
		t.Fatalf("resume status %d", status)
	}
	paused := waitGoal(t, restarted, sess.ID, func(s protocol.GoalState) bool { return s.Status == "paused" })
	if paused.Reason != "max_rounds" || paused.Round != 2 {
		t.Fatalf("resumed goal must run its rounds again: %+v", paused)
	}
	if n := len(client.userTurns()); n != 6 {
		t.Fatalf("goal + 2 checks per start expected, got %d turns", n)
	}
}

func TestGoalPauseLeavesPlainRunsAlone(t *testing.T) {
	client := &replyQueue{block: make(chan struct{})}
	store, base := newGoalServer(t, client)
	sess := createTestSession(t, base)
	if err := store.SetGoal(sess.ID, "old paused goal"); err != nil {
		t.Fatal(err)
	}
	var accepted protocol.RunAccepted
	postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "plain-1", Prompt: "hello"}, &accepted)
	waitTurns(t, client, 1)
	if status, _ := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "pause"}); status != http.StatusConflict {
		t.Fatalf("pause without a running goal: %d", status)
	}
	if got := getGoal(t, base, sess.ID); got.Status != "paused" || got.Reason != "restored" {
		t.Fatalf("goal must stay as it was: %+v", got)
	}
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/runs/"+accepted.RunID+"/cancel", map[string]any{}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the plain run must still be active after pause, cancel status %d", resp.StatusCode)
	}
}

// The next round waits briefly for a turn someone else is running, then pauses.
func TestGoalRoundWaitsForBusySessionThenPauses(t *testing.T) {
	retries, delay := goalBusyRetries, goalBusyDelay
	goalBusyRetries, goalBusyDelay = 3, 20*time.Millisecond
	t.Cleanup(func() { goalBusyRetries, goalBusyDelay = retries, delay })
	client := &replyQueue{}
	server, _, base := newGoalTestServer(t, client)
	sess := createTestSession(t, base)
	rt, err := server.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer server.releaseRuntime(rt)
	rt.mu.Lock()
	server.ensureGoalLoadedLocked(rt)
	rt.goal.text, rt.goal.status, rt.goal.round, rt.goal.nonce = "long task", "active", 1, "busy-nonce"
	rt.goal.next = &goalContinuation{afterRunID: "round-1", clientRequestID: "goal-busy-nonce-2", prompt: "/goal continue"}
	// Another run holds the session when the round would start.
	rt.cancel, rt.runDone = func() {}, false
	rt.mu.Unlock()
	t.Cleanup(func() {
		rt.mu.Lock()
		rt.cancel, rt.runDone = nil, true
		rt.mu.Unlock()
	})
	server.continueGoal(rt, "round-1", protocol.PromptRequest{})
	rt.mu.Lock()
	status, reason := rt.goal.status, rt.goal.reason
	rt.mu.Unlock()
	if status != "paused" || reason != "busy" {
		t.Fatalf("busy session must pause with a fixed reason: status=%s reason=%s", status, reason)
	}
	if n := len(client.userTurns()); n != 0 {
		t.Fatalf("no round may start while busy, got %d turns", n)
	}
}

// A background subagent does not hold the session: the next round starts while it
// works, and its report reaches whichever turn is running when it arrives.
func TestGoalRoundContinuesWhileBackgroundSubagentRuns(t *testing.T) {
	client := &replyQueue{block: make(chan struct{}), replies: []string{"still working", "GOAL_MET done"}}
	server, _, base := newGoalTestServer(t, client)
	sess := createTestSession(t, base)
	postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "long task", Run: &protocol.PromptRequest{ClientRequestID: "b-1"}})
	waitTurns(t, client, 1)
	rt, err := server.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	ag := rt.agent
	rt.mu.Unlock()
	task := ag.RegisterBackground("busy", "work", agent.SubModel{})
	server.releaseRuntime(rt)
	t.Cleanup(func() { ag.Tasks().Cancel(task.ID) })
	client.mu.Lock()
	close(client.block)
	client.block = nil
	client.mu.Unlock()
	met := waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status != "active" })
	if met.Status != "met" {
		t.Fatalf("goal with a running background subagent = %+v", met)
	}
	if n := len(client.userTurns()); n != 2 {
		t.Fatalf("goal rounds = %d, want 2", n)
	}
	if snap, _ := ag.Tasks().Get(task.ID); snap.Status != agent.TaskRunning {
		t.Fatalf("background subagent = %s", snap.Status)
	}
}

func TestGoalIgnoresRunsThatAreNotItsRounds(t *testing.T) {
	client := &replyQueue{block: make(chan struct{}), replies: []string{"GOAL_MET done"}}
	server, _, base := newGoalTestServer(t, client)
	sess := createTestSession(t, base)
	postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "ship", Run: &protocol.PromptRequest{ClientRequestID: "own-1"}})
	waitTurns(t, client, 1)
	rt, err := server.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer server.releaseRuntime(rt)
	// A cron or user run that took the session between rounds finishes.
	for _, outcome := range []string{"completed", "failed", "cancelled"} {
		server.evaluateGoal(rt, "run-foreign", outcome)
		rt.mu.Lock()
		status, round, next := rt.goal.status, rt.goal.round, rt.goal.next
		rt.mu.Unlock()
		if status != "active" || round != 0 || next != nil {
			t.Fatalf("%s foreign run changed the goal: status=%s round=%d next=%v", outcome, status, round, next)
		}
	}
	client.mu.Lock()
	close(client.block)
	client.block = nil
	client.mu.Unlock()
	waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "met" })
}

func postPlainRun(t *testing.T, base, id, requestID, prompt string) protocol.RunAccepted {
	t.Helper()
	var accepted protocol.RunAccepted
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+id+"/runs", protocol.PromptRequest{ConversationID: id, ClientRequestID: requestID, Prompt: prompt}, &accepted)
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		t.Fatalf("run %s: status %d", requestID, resp.StatusCode)
	}
	return accepted
}

func userMessages(t *testing.T, store *session.Store, id string) []string {
	t.Helper()
	_, msgs, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, message := range msgs {
		if message.Role == "user" {
			out = append(out, message.Content)
		}
	}
	return out
}

func TestGoalWaitsForInputAndTheUserReplyResumesIt(t *testing.T) {
	client := &replyQueue{replies: []string{"GOAL_NEEDS_INPUT — 文件标题用什么？", "GOAL_MET — 已写入并核对"}}
	store, base := newGoalServer(t, client)
	sess := createTestSession(t, base)
	postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "create the file", Run: &protocol.PromptRequest{ClientRequestID: "in-1"}})
	waiting := waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "paused" })
	if waiting.Reason != goalReasonAwaitingInput || waiting.Goal != "create the file" || waiting.NextClientRequestID != "" {
		t.Fatalf("waiting: %+v", waiting)
	}
	if n := len(client.userTurns()); n != 1 {
		t.Fatalf("no round may start while the goal waits, got %d turns", n)
	}

	first := postPlainRun(t, base, sess.ID, "reply-1", "标题用《测试》")
	waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "met" })
	turns := client.userTurns()
	if len(turns) != 2 || !strings.HasPrefix(turns[1], "标题用《测试》") || !strings.Contains(turns[1], "just replied") || !strings.Contains(turns[1], "create the file") {
		t.Fatalf("the reply round must carry the answer and the reply rules: %q", turns)
	}
	users := userMessages(t, store, sess.ID)
	if len(users) != 2 || users[1] != "标题用《测试》" {
		t.Fatalf("history must keep the reply as typed: %q", users)
	}
	var statuses []string
	for _, event := range goalEvents(t, base, sess.ID) {
		statuses = append(statuses, event.Status+":"+event.Reason)
	}
	if strings.Join(statuses, ",") != "active:,paused:awaiting_input,active:,met:" {
		t.Fatalf("goal events: %v", statuses)
	}
	// A retried reply replays its run instead of admitting another round.
	if again := postPlainRun(t, base, sess.ID, "reply-1", "标题用《测试》"); again.RunID != first.RunID {
		t.Fatalf("retry must replay: %s vs %s", again.RunID, first.RunID)
	}
	if n := len(client.userTurns()); n != 2 {
		t.Fatalf("a replayed reply must not run again, got %d turns", n)
	}
}

func TestGoalReplyThatStillNeedsInputWaitsAgainAndOtherwiseContinues(t *testing.T) {
	client := &replyQueue{replies: []string{
		"GOAL_NEEDS_INPUT — which title?",
		"GOAL_NEEDS_INPUT — and how many sentences?",
		"writing the file now",
		"GOAL_MET — done",
	}}
	_, base := newGoalServer(t, client)
	sess := createTestSession(t, base)
	postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "create the file", Run: &protocol.PromptRequest{ClientRequestID: "again-1"}})
	waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Reason == goalReasonAwaitingInput })

	postPlainRun(t, base, sess.ID, "reply-a", "title: Test")
	waitTurns(t, client, 2)
	again := waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "paused" })
	if again.Reason != goalReasonAwaitingInput {
		t.Fatalf("a reply that asks again must wait again: %+v", again)
	}
	if n := len(client.userTurns()); n != 2 {
		t.Fatalf("no round may start while waiting again, got %d turns", n)
	}

	postPlainRun(t, base, sess.ID, "reply-b", "two sentences")
	met := waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "met" })
	turns := client.userTurns()
	if len(turns) != 4 || !strings.HasPrefix(turns[3], "[goal 1/") {
		t.Fatalf("an unfinished reply round must continue with a goal round: %q", turns)
	}
	if met.Goal != "" {
		t.Fatalf("met goal must be cleared: %+v", met)
	}
}

func TestGoalPausedByTheUserIsNotResumedByAPlainRun(t *testing.T) {
	client := &replyQueue{block: make(chan struct{}), replies: []string{"GOAL_NEEDS_INPUT — which title?"}}
	_, base := newGoalServer(t, client)
	sess := createTestSession(t, base)
	postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "create the file", Run: &protocol.PromptRequest{ClientRequestID: "user-1"}})
	waitTurns(t, client, 1)
	if status, _ := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "pause"}); status != http.StatusOK {
		t.Fatalf("pause: %d", status)
	}
	client.mu.Lock()
	close(client.block)
	client.block = nil
	client.replies = []string{"GOAL_MET — unrelated answer"}
	client.mu.Unlock()
	waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "paused" && s.Reason == "user" })

	// The cancelled round may still be finishing; retry until the session is idle.
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "plain-after-pause", Prompt: "just a question"}, nil)
		resp.Body.Close()
		if resp.StatusCode == http.StatusAccepted {
			break
		}
		if resp.StatusCode != http.StatusConflict || time.Now().After(deadline) {
			t.Fatalf("plain run: status %d", resp.StatusCode)
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitTurns(t, client, 2)
	time.Sleep(100 * time.Millisecond)
	if got := getGoal(t, base, sess.ID); got.Status != "paused" || got.Reason != "user" || got.Goal != "create the file" {
		t.Fatalf("a user pause needs an explicit resume: %+v", got)
	}
	if turns := client.userTurns(); strings.Contains(turns[1], "[goal check]") || !strings.Contains(turns[1], "[goal paused]") || !strings.Contains(turns[1], "create the file") {
		t.Fatalf("a plain run after a user pause must carry only the paused notice: %q", turns[1])
	}
}

func TestGoalRestoredAfterRestartIsNotResumedByAPlainRun(t *testing.T) {
	client := &replyQueue{replies: []string{"GOAL_MET — unrelated answer"}}
	store, base := newGoalServer(t, client)
	sess := createTestSession(t, base)
	if err := store.SetGoal(sess.ID, "old goal"); err != nil {
		t.Fatal(err)
	}
	postPlainRun(t, base, sess.ID, "plain-restored", "hello")
	waitTurns(t, client, 1)
	time.Sleep(100 * time.Millisecond)
	if got := getGoal(t, base, sess.ID); got.Status != "paused" || got.Reason != "restored" || got.Goal != "old goal" {
		t.Fatalf("a restored goal needs an explicit resume: %+v", got)
	}
	if turns := client.userTurns(); !strings.Contains(turns[0], "[goal paused]") || !strings.Contains(turns[0], "old goal") {
		t.Fatalf("a plain run must be told the restored goal is paused: %q", turns[0])
	}
}

func TestGoalClearedIsNotPickedUpByAPlainRun(t *testing.T) {
	client := &replyQueue{replies: []string{"GOAL_NEEDS_INPUT — which title?", "the time is noon"}}
	store, base := newGoalServer(t, client)
	sess := createTestSession(t, base)
	postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "create the file", Run: &protocol.PromptRequest{ClientRequestID: "clr-1"}})
	waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Reason == goalReasonAwaitingInput })
	if status, _ := postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "clear"}); status != http.StatusOK {
		t.Fatalf("clear: %d", status)
	}
	postPlainRun(t, base, sess.ID, "after-clear", "what time is it?")
	waitTurns(t, client, 2)
	time.Sleep(100 * time.Millisecond)
	if got := getGoal(t, base, sess.ID); got.Status != "none" || got.Goal != "" {
		t.Fatalf("a cleared goal must stay cleared: %+v", got)
	}
	turn := client.userTurns()[1]
	if !strings.HasPrefix(turn, "what time is it?") || !strings.Contains(turn, "[goal cleared]") || strings.Contains(turn, "[goal check]") {
		t.Fatalf("a plain run after clear must carry only the cleared notice: %q", turn)
	}
	if users := userMessages(t, store, sess.ID); users[len(users)-1] != "what time is it?" {
		t.Fatalf("the notice must stay out of history: %q", users)
	}
	// A new goal drops the reminder of the cleared one.
	client.mu.Lock()
	client.replies = []string{"GOAL_MET — done"}
	client.mu.Unlock()
	postGoal(t, base, sess.ID, protocol.GoalRequest{Action: "set", Goal: "something else", Run: &protocol.PromptRequest{ClientRequestID: "clr-2"}})
	waitGoal(t, base, sess.ID, func(s protocol.GoalState) bool { return s.Status == "met" })
	postPlainRun(t, base, sess.ID, "after-met", "thanks")
	waitTurns(t, client, 4)
	if last := client.userTurns()[3]; strings.Contains(last, "[goal") {
		t.Fatalf("no goal notice after a met goal: %q", last)
	}
}
