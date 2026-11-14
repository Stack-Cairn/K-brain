package backend

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestHistoryLegacyNoticeSurvivesStoreFork(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const notice = "[subagent sub-1 cancelled] Review history\n\ncontext canceled"
	id := createStoredHistorySession(t, store, t.TempDir(), []ai.Message{{Role: "user", Content: notice}})
	if err := store.SaveTask(id, session.Task{ID: "sub-1", Description: "Review history", Status: "cancelled", Report: "context canceled"}); err != nil {
		t.Fatal(err)
	}
	source, err := store.HistorySnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	if source.Messages[0].Origin != ai.OriginBackground {
		t.Fatal("source notice was not recovered")
	}
	fork, err := store.Fork(id, 1<<30, "TUI fork")
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := store.LoadTasks(fork)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatal("fixture requires a fork without task records")
	}
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "unused"})
	defer server.Close()
	var page historyResponse
	code := doJSON(t, http.MethodGet, server.URL+"/v1/sessions/"+fork+"/history?include_active=true", nil, &page)
	if code != http.StatusOK {
		t.Fatalf("history status=%d", code)
	}
	if len(page.Session.Messages) != 1 {
		t.Fatalf("messages=%d", len(page.Session.Messages))
	}
	got := page.Session.Messages[0]
	if got.Origin != ai.OriginBackground {
		t.Errorf("fork origin=%q, want background", got.Origin)
	}
	if got.ID != source.Messages[0].ID {
		t.Errorf("fork ID=%q, want source ID=%q", got.ID, source.Messages[0].ID)
	}
	if page.ActiveMessages == nil || len(*page.ActiveMessages) != 1 {
		t.Fatal("missing active history")
	}
	if (*page.ActiveMessages)[0].Origin != ai.OriginBackground {
		t.Error("fork active history lost notice provenance")
	}
}

func TestHistoryLegacySubagentNoticeSurvivesForkAndTruncatedReport(t *testing.T) {
	for _, long := range []bool{false, true} {
		t.Run(fmt.Sprintf("long=%v", long), func(t *testing.T) {
			store, err := session.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			report := "context canceled"
			if long {
				report = strings.Repeat("a", 49_999) + strings.Repeat("中", 10)
			}
			displayed := report
			if len(displayed) > 50_000 {
				end := 50_000
				for !utf8.RuneStart(displayed[end]) {
					end--
				}
				displayed = displayed[:end] + fmt.Sprintf("\n\n... [report truncated — %d bytes total; the investigation covered more than fits here]", len(report))
			}
			notice := "[subagent sub-1 cancelled] Review history\n\n" + displayed
			id := createStoredHistorySession(t, store, t.TempDir(), []ai.Message{
				{Role: "user", Content: notice},
				{ID: "quote", Role: "user", Content: notice, Authored: true},
			})
			if err := store.SaveTask(id, session.Task{ID: "sub-1", Description: "Review history", Status: "cancelled", Report: report}); err != nil {
				t.Fatal(err)
			}
			snapshot, err := store.HistorySnapshot(id)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Messages[0].Origin != ai.OriginBackground {
				t.Error("source notice lost provenance")
			}
			fork, err := store.MutateHistory(id, "quote", snapshot.Revision, "fork", nil)
			if err != nil {
				t.Fatal(err)
			}
			for reload := 0; reload < 2; reload++ {
				_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "unused"})
				var page historyResponse
				code := doJSON(t, http.MethodGet, server.URL+"/v1/sessions/"+fork+"/history?include_active=true", nil, &page)
				server.Close()
				if code != http.StatusOK {
					t.Fatalf("status=%d", code)
				}
				if len(page.Session.Messages) != 2 {
					t.Fatalf("messages=%d", len(page.Session.Messages))
				}
				first, quote := page.Session.Messages[0], page.Session.Messages[1]
				if first.Origin != ai.OriginBackground || first.ID != snapshot.Messages[0].ID || first.Role != protocol.RoleUser || first.Content[0].Text != notice {
					t.Error("fork notice lost provenance, identity or content")
				}
				if quote.Origin != "" || !quote.Authored || quote.ID != "quote" || quote.Content[0].Text != notice {
					t.Error("authored quote changed")
				}
				if page.ActiveMessages == nil || len(*page.ActiveMessages) != 2 || (*page.ActiveMessages)[0].Origin != ai.OriginBackground {
					t.Error("fork active history lost provenance")
				}
			}
		})
	}
}

func TestHistoryLegacySubagentNoticeProvenanceSurvivesReload(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const notice = "[subagent sub-1 cancelled] Review history\n\ncontext canceled"
	messages := []ai.Message{
		{Role: "user", Content: notice}, // Old runtime record: no identity or authorship.
		{ID: "user-quote-1", Role: "user", Content: notice, Authored: true},
		{ID: "user-quote-2", Role: "user", Content: notice, Authored: true},
		{Role: "user", Content: notice, Authored: true},
		{ID: "identified-quote", Role: "user", Content: notice},
		{Role: "user", Content: "[subagent sub-2 cancelled] Unknown task\n\ncontext canceled"},
		{Role: "user", Content: notice, Origin: "user"},
		{ID: "new-notice", Role: "user", Content: notice, Origin: ai.OriginBackground},
	}
	id := createStoredHistorySession(t, store, t.TempDir(), messages)
	if err := store.SaveTask(id, session.Task{ID: "sub-1", Description: "Review history", Status: "cancelled", Report: "context canceled"}); err != nil {
		t.Fatal(err)
	}
	var firstIDs []string
	for reload := 0; reload < 2; reload++ {
		// A fresh backend runtime must derive the same projection from persisted data.
		_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "unused"})
		var page historyResponse
		if code := doJSON(t, http.MethodGet, server.URL+"/v1/sessions/"+id+"/history?include_active=true", nil, &page); code != http.StatusOK {
			t.Fatalf("history status=%d", code)
		}
		if len(page.Session.Messages) != len(messages) {
			t.Fatalf("message count = %d", len(page.Session.Messages))
		}
		for i, got := range page.Session.Messages {
			wantOrigin := messages[i].Origin
			if i == 0 {
				wantOrigin = ai.OriginBackground
			}
			if got.Origin != wantOrigin || got.Authored != messages[i].Authored || got.Role != protocol.RoleUser || len(got.Content) != 1 || got.Content[0].Text != messages[i].Content {
				t.Errorf("reload %d message %d: origin=%q authored=%v role=%q content=%v; want origin=%q authored=%v", reload, i, got.Origin, got.Authored, got.Role, got.Content, wantOrigin, messages[i].Authored)
			}
			if got.ID == "" {
				t.Errorf("message %d has no stable history identity", i)
			}
			if reload == 0 {
				firstIDs = append(firstIDs, got.ID)
			} else if firstIDs[i] != got.ID {
				t.Errorf("message %d identity changed across reload", i)
			}
		}
		if page.ActiveMessages == nil || len(*page.ActiveMessages) != len(messages) || (*page.ActiveMessages)[0].Origin != ai.OriginBackground {
			t.Error("active history projection lost notice provenance")
		}
		server.Close()
	}
	// Projection must not rewrite the stored model roles, content, or identities.
	_, stored, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	for i, got := range stored {
		if got.Role != messages[i].Role || got.Content != messages[i].Content || got.ID != messages[i].ID || got.Origin != messages[i].Origin {
			t.Errorf("stored message %d changed during history reads", i)
		}
	}
}
