package backend

// Second independent review of the memory master switch (8ac064b + bb6d83c). These
// tests reuse newReviewMemoryFixture (real backend, real memoryruntime, real session
// store) and isolate LIVEAGENT_HOME and the data root under t.TempDir().

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

func review2TailUser(t *testing.T, messages []protocol.Message) protocol.Message {
	t.Helper()
	// A run stopped before any reply leaves an empty stop marker after its
	// prompt; resuming that prompt replaces the marker.
	if n := len(messages); n > 0 && strings.HasPrefix(messages[n-1].ID, "assistant-stopped-") {
		messages = messages[:n-1]
	}
	if len(messages) == 0 || messages[len(messages)-1].Role != protocol.RoleUser {
		t.Fatalf("fixture: tail message is not a user message: %+v", messages)
	}
	return messages[len(messages)-1]
}

// edit replaces the last user message and resends it the way LiveAgent's 编辑重发 does:
// POST /edit, then a run with resume_message_id.
func (f *reviewMemoryFixture) review2EditAndResend(t *testing.T, sessionID, text, requestID string, after int64) int64 {
	t.Helper()
	current := getSession(t, f.url, sessionID)
	var target protocol.Message
	for _, message := range current.Messages {
		if message.Role == protocol.RoleUser {
			target = message
		}
	}
	var edited protocol.Session
	if code := doJSON(t, http.MethodPost, f.url+"/v1/sessions/"+sessionID+"/edit", protocol.EditSessionRequest{
		ExpectedRevision: current.Revision,
		MessageRef:       protocol.HistoryMessageRef{MessageID: target.ID, Role: protocol.RoleUser},
		Replacement:      protocol.Message{Role: protocol.RoleUser, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: text}}},
	}, &edited); code != http.StatusOK {
		t.Fatalf("edit status %d", code)
	}
	resumed := review2TailUser(t, edited.Messages)
	if code := doJSON(t, http.MethodPost, f.url+"/v1/sessions/"+sessionID+"/runs", protocol.PromptRequest{ConversationID: sessionID, ClientRequestID: requestID, ResumeMessageID: resumed.ID, Prompt: text}, nil); code != http.StatusAccepted {
		t.Fatalf("resend status %d", code)
	}
	events := waitForRun(t, f.url, sessionID, after)
	return events[len(events)-1].Seq
}

func (f *reviewMemoryFixture) review2WaitExtraction(t *testing.T) [][]ai.Message {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(f.extractionRequests()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	requests := f.extractionRequests()
	if len(requests) == 0 {
		t.Fatal("extractor never ran after memory was turned back on")
	}
	return requests
}

func review2AssertNoLeak(t *testing.T, requests [][]ai.Message, secret string) {
	t.Helper()
	for _, request := range requests {
		for _, message := range request {
			if strings.Contains(message.TextContent(), secret) {
				t.Fatalf("a user message written while memory was off reached the memory extractor after re-enabling:\n%s", message.TextContent())
			}
		}
	}
}

// 编辑重发：关闭期间编辑并重发一条消息。/edit 用 protocol.Message 重建替换消息，丢掉了
// memory-off 快照；resume 路径又直接复用 a.Messages 里的这条消息，不写入新快照。重新打开后
// 这条关闭期间写的话作为 earlier-user-messages 发给了提取模型。
func TestReview2MemorySwitchEditResendWhileOffReachesExtractorAfterReenable(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	f := newReviewMemoryFixture(t, t.TempDir(), reviewFixtureConfig())
	sess := createTestSession(t, f.url)

	f.setEnabled(t, false)
	seq := f.turn(t, sess.ID, "off-1", "what does the build script do", 0)
	seq = f.review2EditAndResend(t, sess.ID, "OFFSECRET-EDIT-4410 I always deploy on Fridays", "off-resend", seq)
	time.Sleep(50 * time.Millisecond)
	if got := f.extractionRequests(); len(got) != 0 {
		t.Fatalf("extractor ran while memory is off: %d requests", len(got))
	}

	f.setEnabled(t, true)
	f.turn(t, sess.ID, "on-1", "I prefer short commit messages", seq)
	review2AssertNoLeak(t, f.review2WaitExtraction(t), "OFFSECRET-EDIT-4410")
}

// 编辑重发：开着时第一轮注入了记忆，关闭后第二轮带撤回通知；编辑第二轮会截掉它（连同撤回
// 通知），resume 路径不再生成撤回。关闭期间发给对话模型的请求里仍有第一轮的记忆条目，却没有
// "Memory is turned off"。
func TestReview2MemorySwitchEditResendWhileOffDropsRetraction(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	f := newReviewMemoryFixture(t, t.TempDir(), reviewFixtureConfig())
	sess := createTestSession(t, f.url)

	seq := f.turn(t, sess.ID, "on-1", "hello there friend", 0)
	if _, context := f.client.lastRequest(t); !strings.Contains(context, "prefers-go") {
		t.Fatalf("baseline turn did not inject memory: %q", context)
	}
	f.setEnabled(t, false)
	seq = f.turn(t, sess.ID, "off-1", "second turn while off", seq)
	if _, context := f.client.lastRequest(t); !strings.Contains(context, "Memory is turned off") {
		t.Fatalf("precondition: first off turn must retract: %q", context)
	}
	f.review2EditAndResend(t, sess.ID, "second turn while off, edited", "off-resend", seq)

	f.client.scriptedClient.mu.Lock()
	last := f.client.scriptedClient.requests[len(f.client.scriptedClient.requests)-1]
	f.client.scriptedClient.mu.Unlock()
	var whole strings.Builder
	for _, message := range last {
		whole.WriteString(message.TextContent())
		whole.WriteString("\n")
	}
	if text := whole.String(); strings.Contains(text, "prefers-go") && !strings.Contains(text, "Memory is turned off") {
		t.Fatalf("memory is off but the resent request carries the earlier memory snapshot without the off notice:\n%s", text)
	}
}

// 停止后继续：关闭期间发出的消息被停止（尾部仍是这条用户消息，带 memory-off 快照）。重新打开
// 后用 resume_message_id 继续它，Completed 收到的 User 是新建的 userMessage，没有走
// withoutMemoryOffTurns，关闭期间的原话被当作 latest-user-message 提取。
func TestReview2MemorySwitchCancelledOffTurnResumedAfterReenableIsExtracted(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	f := newReviewMemoryFixture(t, t.TempDir(), reviewFixtureConfig())
	sess := createTestSession(t, f.url)
	const secret = "OFFSECRET-CANCEL-5521 I always deploy on Fridays"

	f.setEnabled(t, false)
	f.client.scriptedClient.mu.Lock()
	f.client.scriptedClient.wait = true
	f.client.scriptedClient.mu.Unlock()
	accepted := runRequest(t, f.url, sess.ID, "off-1", secret)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.client.mu.Lock()
		called := len(f.client.tools) > 0
		f.client.mu.Unlock()
		if called {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if code := doJSON(t, http.MethodPost, f.url+"/v1/sessions/"+sess.ID+"/runs/"+accepted.RunID+"/cancel", map[string]any{}, nil); code >= 300 {
		t.Fatalf("cancel status %d", code)
	}
	events := waitForRun(t, f.url, sess.ID, 0)
	seq := events[len(events)-1].Seq
	f.client.scriptedClient.mu.Lock()
	f.client.scriptedClient.wait = false
	f.client.scriptedClient.mu.Unlock()
	tail := review2TailUser(t, getSession(t, f.url, sess.ID).Messages)

	f.setEnabled(t, true)
	if code := doJSON(t, http.MethodPost, f.url+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "on-resume", ResumeMessageID: tail.ID, Prompt: secret}, nil); code != http.StatusAccepted {
		t.Fatalf("resume status %d", code)
	}
	events = waitForRun(t, f.url, sess.ID, seq)
	// A later turn written while on is extracted; once it is, the resumed message has had
	// its chance to leak, as the latest or an earlier message.
	f.turn(t, sess.ID, "on-sentinel", "I also prefer short commit messages", events[len(events)-1].Seq)
	review2AssertNoLeak(t, f.review2WaitExtraction(t), "OFFSECRET-CANCEL-5521")
}
