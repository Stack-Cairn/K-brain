package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

const defaultQuestionTimeout = 3 * time.Minute

const questionSchema = `{"type":"object","properties":{"questions":{"type":"array","minItems":1,"maxItems":4,"items":{"type":"object","properties":{"id":{"type":"string"},"header":{"type":"string"},"prompt":{"type":"string"},"options":{"type":"array","minItems":2,"maxItems":6,"items":{"type":"object","properties":{"label":{"type":"string"},"description":{"type":"string"},"recommended":{"type":"boolean"}},"required":["label"]}}},"required":["prompt","options"]}}},"required":["questions"]}`

type questionRunKey struct{}
type questionWaiter struct {
	request    protocol.QuestionRequest
	ctx        context.Context
	done       chan struct{}
	resolution *protocol.QuestionResolution
}

func (s *Server) wireQuestions(rt *runtimeSession, ag *agent.Agent) {
	tool := tools.Tool{NoInherit: true, Def: ai.NewTool("AskUserQuestion", "Ask 1-4 focused questions when a decision belongs to the user. Each question needs 2-6 distinct options and at most one recommended option, shown first. The UI adds Other for a custom answer; do not add your own. Give each question a short header. After 3 minutes the recommended (or first) option is selected, and the result explicitly reports the timeout. Do not ask questions answerable from the workspace or conversation.", questionSchema)}
	tool.Run = func(ctx context.Context, args json.RawMessage) (string, error) {
		var input struct {
			Questions []protocol.Question       `json:"questions"`
			Question  string                    `json:"question"`
			Options   []protocol.QuestionOption `json:"options"`
		}
		if err := json.Unmarshal(args, &input); err != nil {
			return "", err
		}
		if input.Questions == nil && input.Question != "" {
			input.Questions = []protocol.Question{{Prompt: input.Question, Options: input.Options}}
		}
		questions, err := normalizeQuestions(input.Questions)
		if err != nil {
			return "", err
		}
		runID, _ := ctx.Value(questionRunKey{}).(string)
		if runID == "" {
			return "", errors.New("question requires an active backend run")
		}
		// No permission mode prompts for a question (permissions.IsInteractionTool);
		// the gate is here so that deny rules and per-tool policies apply in every mode,
		// not only when an AI review gate runs first.
		if err := tools.Authorize(ctx, "AskUserQuestion", string(args)); err != nil {
			return "", err
		}
		result, err := s.waitQuestion(ctx, rt, runID, tools.ToolCallID(ctx), questions)
		if result == nil {
			return "", err
		}
		tools.ReportQuestionAnswers(ctx, result.Answers)
		encoded, encodeErr := json.Marshal(result)
		return string(encoded), errors.Join(err, encodeErr)
	}
	for _, existing := range ag.Tools {
		if existing.Def.Function.Name == tool.Def.Function.Name {
			return
		}
	}
	ag.Tools = append(ag.Tools, tool)
}

func normalizeQuestions(input []protocol.Question) ([]protocol.Question, error) {
	if len(input) < 1 || len(input) > 4 {
		return nil, errors.New("AskUserQuestion requires 1-4 questions")
	}
	out := make([]protocol.Question, len(input))
	seen := map[string]bool{}
	for i, q := range input {
		q.ID, q.Header, q.Prompt = strings.TrimSpace(q.ID), strings.TrimSpace(q.Header), strings.TrimSpace(q.Prompt)
		if q.ID == "" {
			q.ID = fmt.Sprintf("q%d", i+1)
		}
		if q.Prompt == "" || seen[q.ID] {
			return nil, errors.New("questions require a non-empty prompt and unique IDs")
		}
		seen[q.ID] = true
		if len(q.Options) < 2 || len(q.Options) > 6 {
			return nil, errors.New("each question requires 2-6 options")
		}
		q.Options = append([]protocol.QuestionOption(nil), q.Options...)
		labels := map[string]bool{}
		recommended := -1
		for j := range q.Options {
			option := &q.Options[j]
			option.Label, option.Description = strings.TrimSpace(option.Label), strings.TrimSpace(option.Description)
			if option.Label == "" || labels[option.Label] {
				return nil, errors.New("option labels must be non-empty and unique")
			}
			labels[option.Label] = true
			if option.Recommended {
				if recommended >= 0 {
					return nil, errors.New("at most one option may be recommended")
				}
				recommended = j
			}
		}
		if recommended > 0 {
			chosen := q.Options[recommended]
			copy(q.Options[1:recommended+1], q.Options[:recommended])
			q.Options[0] = chosen
		}
		out[i] = q
	}
	return out, nil
}

// validateQuestionAnswers checks a client's answers against the questions
// asked and returns them in question order, ready to record. An answer is one
// of:
//   - selected_label: one of the question's option labels;
//   - selected_label with custom: the user's own words (at most 2000 characters);
//   - selected_labels: the chosen options, each exactly one of the question's
//     labels and none twice; exactly one on a single-select question. They are
//     kept in option order and selected_label, which must be left empty, is
//     filled with them joined by ", " for readers that predate selected_labels;
//   - selected_labels with custom (multi-select questions only): the chosen
//     options plus the user's own words in selected_label.
func validateQuestionAnswers(questions []protocol.Question, input []protocol.QuestionAnswer) ([]protocol.QuestionAnswer, error) {
	if len(input) != len(questions) {
		return nil, errors.New("one answer is required for every question")
	}
	byID := map[string]protocol.QuestionAnswer{}
	for _, a := range input {
		a.QuestionID, a.SelectedLabel = strings.TrimSpace(a.QuestionID), strings.TrimSpace(a.SelectedLabel)
		if a.QuestionID == "" || (a.SelectedLabel == "" && len(a.SelectedLabels) == 0) {
			return nil, errors.New("question IDs and answers cannot be empty")
		}
		if _, exists := byID[a.QuestionID]; exists {
			return nil, errors.New("duplicate answer question ID")
		}
		if a.Custom && len([]rune(a.SelectedLabel)) > 2000 {
			return nil, errors.New("custom answer exceeds 2000 characters")
		}
		byID[a.QuestionID] = a
	}
	answers := make([]protocol.QuestionAnswer, len(questions))
	for i, q := range questions {
		a, exists := byID[q.ID]
		if !exists {
			return nil, errors.New("answer question ID does not match request")
		}
		if len(a.SelectedLabels) > 0 {
			labels, err := selectedOptions(q, a.SelectedLabels)
			if err != nil {
				return nil, err
			}
			switch {
			case !q.Multiple && (len(labels) != 1 || a.Custom):
				return nil, fmt.Errorf("question %q is single-select: selected_labels must hold exactly one label, without custom", q.ID)
			case a.Custom && a.SelectedLabel == "":
				return nil, fmt.Errorf("question %q: custom with selected_labels needs the user's text in selected_label", q.ID)
			case !a.Custom && a.SelectedLabel != "":
				return nil, fmt.Errorf("question %q: selected_label must be empty when selected_labels is sent without custom", q.ID)
			case !a.Custom:
				a.SelectedLabel = strings.Join(labels, ", ")
			}
			a.SelectedLabels = labels
		} else {
			a.SelectedLabels = nil
			listed := false
			for _, option := range q.Options {
				if option.Label == a.SelectedLabel {
					listed = true
				}
			}
			if !a.Custom && !listed {
				return nil, errors.New("answer must be a listed option or explicitly custom")
			}
		}
		a.Prompt = q.Prompt
		answers[i] = a
	}
	return answers, nil
}

// selectedOptions checks that every label names one of q's options exactly,
// none twice, and returns them in option order.
func selectedOptions(q protocol.Question, labels []string) ([]string, error) {
	chosen := make(map[string]bool, len(labels))
	for _, label := range labels {
		listed := false
		for _, option := range q.Options {
			if option.Label == label {
				listed = true
				break
			}
		}
		if !listed {
			return nil, fmt.Errorf("question %q: selected_labels has %q, which is not one of its option labels", q.ID, label)
		}
		if chosen[label] {
			return nil, fmt.Errorf("question %q: selected_labels has %q twice", q.ID, label)
		}
		chosen[label] = true
	}
	out := make([]string, 0, len(labels))
	for _, option := range q.Options {
		if chosen[option.Label] {
			out = append(out, option.Label)
		}
	}
	return out, nil
}

// askAnswer is a recorded answer as the question tools present it to the model.
func askAnswer(a protocol.QuestionAnswer) tools.AskAnswer {
	switch {
	case len(a.SelectedLabels) > 0 && a.Custom:
		return tools.AskAnswer{Labels: a.SelectedLabels, Custom: a.SelectedLabel}
	case len(a.SelectedLabels) > 0:
		return tools.AskAnswer{Labels: a.SelectedLabels}
	case a.Custom:
		return tools.AskAnswer{Custom: a.SelectedLabel}
	}
	return tools.AskAnswer{Labels: []string{a.SelectedLabel}}
}

// rt.mu protects registration, settlement, and journal publication together.
func (rt *runtimeSession) publishEventLocked(runID, eventType string, payload any) error {
	event, err := protocol.NewEvent(rt.nextSeq+1, rt.id, runID, eventType, payload)
	if err != nil {
		return err
	}
	if err := rt.persistEvent(rt.eventDir, event); err != nil {
		return err
	}
	rt.nextSeq = event.Seq
	rt.appendEventLocked(event)
	close(rt.changed)
	rt.changed = make(chan struct{})
	return nil
}

func (rt *runtimeSession) settleQuestionLocked(waiter *questionWaiter, answers []protocol.QuestionAnswer, state string) error {
	if waiter.resolution != nil {
		return nil
	}
	if answers == nil {
		answers = []protocol.QuestionAnswer{}
	}
	result := protocol.QuestionResolution{QuestionID: waiter.request.QuestionID, ToolCallID: waiter.request.ToolCallID, RunID: waiter.request.RunID, Kind: "ask_user_question", Questions: waiter.request.Questions, Answers: answers, TimedOut: state == "timeout", Cancelled: state == "cancelled"}
	switch {
	case result.Cancelled:
		result.Text = "The user stopped the turn without answering. Do not assume any selection."
	case result.TimedOut:
		// No option is chosen on the user's behalf: option order says nothing about intent,
		// and a question about a destructive step must not turn into consent by waiting.
		result.Text = "The user did not answer within the time limit, so no option was selected. " +
			"Do not assume an answer or act on any option. If the next step depends on this choice, " +
			"stop and tell the user what you need from them; otherwise continue without it."
	default:
		result.Text = "The user answered every question. Their selections are final — proceed accordingly:"
		for i, a := range answers {
			result.Text += fmt.Sprintf("\n%d. %s\n   → %s", i+1, a.Prompt, askAnswer(a).Text())
		}
	}
	if err := rt.publishEventLocked(result.RunID, protocol.EventQuestionResolved, result); err != nil {
		return err
	}
	waiter.resolution = &result
	close(waiter.done)
	return nil
}

func (s *Server) waitQuestion(ctx context.Context, rt *runtimeSession, runID, toolCallID string, questions []protocol.Question) (*protocol.QuestionResolution, error) {
	if isUnattendedRun(ctx) {
		return nil, errors.New("unattended scheduled runs cannot request interactive answers")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout := s.questionWait
	if timeout <= 0 {
		timeout = defaultQuestionTimeout
	}
	waiter := &questionWaiter{ctx: ctx, done: make(chan struct{}), request: protocol.QuestionRequest{QuestionID: newRunID(), ToolCallID: toolCallID, RunID: runID, DeadlineAt: time.Now().Add(timeout).UnixMilli(), Questions: questions}}
	rt.mu.Lock()
	if rt.runID != runID || rt.runDone || rt.deleted {
		rt.mu.Unlock()
		return nil, errors.New("question run is no longer active")
	}
	if rt.questions == nil {
		rt.questions = make(map[string]*questionWaiter)
	}
	if err := rt.publishEventLocked(runID, protocol.EventQuestionRequested, waiter.request); err != nil {
		rt.mu.Unlock()
		return nil, err
	}
	rt.questions[waiter.request.QuestionID] = waiter
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
	defer delete(rt.questions, waiter.request.QuestionID)
	if waiter.resolution == nil {
		state := "timeout"
		if ctx.Err() != nil {
			state = "cancelled"
		}
		if err := rt.settleQuestionLocked(waiter, nil, state); err != nil {
			return nil, err
		}
	}
	if waiter.resolution.Cancelled {
		return waiter.resolution, context.Canceled
	}
	return waiter.resolution, nil
}

func (s *Server) answerQuestion(w http.ResponseWriter, r *http.Request, id, questionID string) {
	var in protocol.QuestionAnswerRequest
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	if in.ConversationID != id || in.QuestionID != questionID || in.RunID == "" {
		writeJSONError(w, http.StatusConflict, "question identity mismatch")
		return
	}
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	defer s.releaseRuntime(rt)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	events, err := rt.allEventsLocked()
	if err != nil {
		mutationError(w, err)
		return
	}
	var request *protocol.QuestionRequest
	var resolved *protocol.QuestionResolution
	for i := range events {
		event := &events[i]
		if event.RunID != in.RunID {
			continue
		}
		if event.Type == protocol.EventQuestionRequested {
			var q protocol.QuestionRequest
			if json.Unmarshal(event.Payload, &q) == nil && q.QuestionID == questionID {
				request = &q
			}
		}
		if event.Type == protocol.EventQuestionResolved {
			var result protocol.QuestionResolution
			if json.Unmarshal(event.Payload, &result) == nil && result.QuestionID == questionID {
				resolved = &result
			}
		}
	}
	if request == nil {
		writeJSONError(w, http.StatusNotFound, "question request not found for this run")
		return
	}
	answers, err := validateQuestionAnswers(request.Questions, in.Answers)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if resolved != nil {
		if !resolved.Cancelled && !resolved.TimedOut && reflect.DeepEqual(resolved.Answers, answers) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": true})
			return
		}
		writeJSONError(w, http.StatusConflict, "question is already resolved")
		return
	}
	waiter := rt.questions[questionID]
	if waiter == nil || rt.runID != in.RunID || rt.runDone || rt.deleted {
		writeJSONError(w, http.StatusConflict, "question is no longer pending")
		return
	}
	if waiter.ctx.Err() != nil || time.Now().UnixMilli() >= request.DeadlineAt {
		state := "timeout"
		if waiter.ctx.Err() != nil {
			state = "cancelled"
		}
		if err := rt.settleQuestionLocked(waiter, nil, state); err != nil {
			writeJSONError(w, 500, err.Error())
			return
		}
		writeJSONError(w, http.StatusConflict, "question is no longer pending")
		return
	}
	if err := rt.settleQuestionLocked(waiter, answers, "answered"); err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
