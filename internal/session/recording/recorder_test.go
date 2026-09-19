package recording

import (
	"os"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestRecorderUsageSaveFailureAndReopen(t *testing.T) {
	home := t.TempDir()
	st, err := session.OpenHome(home)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.Create("project", "original", "provider1")
	if err != nil {
		t.Fatal(err)
	}
	prior := ai.Usage{PromptTokens: 100, CompletionTokens: 10, PromptCacheHitTokens: 60, PromptCacheWriteTokens: 20}
	if err := st.Save(id, 0, []ai.Message{{Role: "user", Content: "before"}, {Role: "assistant", Content: "answer", Usage: &prior}}, "original", "provider1"); err != nil {
		t.Fatal(err)
	}
	ag := agent.New(ai.New("http://unused", "key"), "new", 100, "sys")
	ag.ModelName, ag.Provider = "new", "provider2"
	r, err := Open(st, id, ag)
	if err != nil {
		t.Fatal(err)
	}
	if ag.ModelUsage()["original @ provider1"].PromptTokens != 100 {
		t.Fatal("restored usage attributed to new route")
	}
	ag.AddUsage(ai.Usage{PromptTokens: 200, CompletionTokens: 20, PromptCacheHitTokens: 120, PromptCacheWriteTokens: 30})
	ag.AddSubUsage("child @ provider3", ai.Usage{PromptTokens: 50, PromptCacheHitTokens: 40})
	ag.Messages = append(ag.Messages, ai.Message{Role: "user", Content: "after"}, ai.Message{Role: "assistant", Content: "done"})
	path := st.TranscriptPath(id)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(); err == nil {
		t.Fatal("save accepted corrupt transcript")
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := r.Save(); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := session.OpenHome(home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	resumed := agent.New(ai.New("http://unused", "key"), "third", 100, "sys")
	resumed.Provider = "provider3"
	if _, err := Open(reopened, id, resumed); err != nil {
		t.Fatal(err)
	}
	u := resumed.UsageSummary()
	if u.Total.PromptTokens != 300 || u.Total.CompletionTokens != 30 || u.Total.Cached() != 180 || u.Total.CacheWrite() != 50 || len(u.Models) != 2 || u.Subagents["child @ provider3"].PromptTokens != 50 || len(resumed.Messages) != 5 {
		t.Fatalf("save retry/reopen lost or duplicated state: %+v", u)
	}
	delete(u.Models, "original @ provider1")
	if len(resumed.ModelUsage()) != 2 {
		t.Fatal("snapshot aliases agent usage")
	}
}
