package session

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func openUsageStore(t *testing.T) *Store {
	t.Helper()
	st, err := OpenProjectDir(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func at(s string) *time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &v
}

func reply(id, sent string, in, out int, model string) ai.Message {
	msg := ai.Message{ID: id, Role: "assistant", Content: "ok", Model: model, Usage: &ai.Usage{PromptTokens: in, CompletionTokens: out}}
	if sent != "" {
		msg.SentAt = at(sent)
	}
	return msg
}

func prompt(sent string) ai.Message {
	return ai.Message{Role: "user", Content: "hi", SentAt: at(sent)}
}

func saveSession(t *testing.T, st *Store, cwd string, msgs ...ai.Message) string {
	t.Helper()
	id, err := st.Create(cwd, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(id, 0, msgs, "m", "p"); err != nil {
		t.Fatal(err)
	}
	return id
}

func usageStats(t *testing.T, st *Store, days int, tz, now string) UsageStats {
	t.Helper()
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := st.UsageStats(context.Background(), UsageStatsOptions{Days: days, Location: loc, Now: *at(now)})
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

func TestUsageStatsBucketsDaysInTheRequestedZone(t *testing.T) {
	st := openUsageStore(t)
	// 17:30 UTC on the 14th is 01:30 on the 15th in Shanghai.
	saveSession(t, st, "/work/a", prompt("2026-11-14T17:29:00Z"), reply("msg-a", "2026-11-14T17:30:00Z", 100, 10, "alpha @ p"))
	saveSession(t, st, "/work/b", prompt("2026-11-14T03:00:00Z"), reply("msg-b", "2026-11-14T03:00:05Z", 20, 2, "beta @ q"))

	utc := usageStats(t, st, 30, "UTC", "2026-11-15T10:00:00Z")
	if len(utc.Daily) != 1 || utc.Daily[0].Date != "2026-11-14" || utc.Daily[0].Total() != 132 || utc.Daily[0].Turns != 2 {
		t.Fatalf("UTC daily = %+v", utc.Daily)
	}
	sh := usageStats(t, st, 30, "Asia/Shanghai", "2026-11-15T10:00:00Z")
	if len(sh.Daily) != 2 || sh.Daily[0].Date != "2026-11-14" || sh.Daily[0].Total() != 22 || sh.Daily[1].Date != "2026-11-15" || sh.Daily[1].Total() != 110 {
		t.Fatalf("Shanghai daily = %+v", sh.Daily)
	}
	if sh.Daily[1].Models["alpha"] != 110 || sh.Daily[0].Models["beta"] != 22 {
		t.Fatalf("daily models = %+v %+v", sh.Daily[0].Models, sh.Daily[1].Models)
	}
	if sh.Totals.Total() != 132 || sh.Sessions != 2 || sh.TopModel != "alpha" || sh.LongestSession == nil || sh.LongestSession.Total != 110 {
		t.Fatalf("Shanghai stats = %+v", sh)
	}
	if len(sh.Models) != 2 || sh.Models[0].Model != "alpha" || sh.Models[0].Provider != "p" || sh.Models[1].Provider != "q" {
		t.Fatalf("models = %+v", sh.Models)
	}
	// One day in Shanghai is only the 15th.
	if one := usageStats(t, st, 1, "Asia/Shanghai", "2026-11-15T10:00:00Z"); one.Totals.Total() != 110 || len(one.Daily) != 1 {
		t.Fatalf("one day = %+v", one)
	}
}

func TestUsageStatsTimeFallbackForRepliesWithoutSentAt(t *testing.T) {
	st := openUsageStore(t)
	saveSession(t, st, "/w", prompt("2026-11-10T08:00:00Z"), reply("", "", 5, 5, ""), prompt("2026-11-12T08:00:00Z"), reply("", "", 7, 3, ""))
	stats := usageStats(t, st, 30, "UTC", "2026-11-15T10:00:00Z")
	if len(stats.Daily) != 2 || stats.Daily[0].Date != "2026-11-10" || stats.Daily[1].Date != "2026-11-12" {
		t.Fatalf("daily = %+v", stats.Daily)
	}
	// A reply without a model is labelled with the session's route, as UsageSummary does.
	if len(stats.Models) != 1 || stats.Models[0].Model != "m" || stats.Models[0].Provider != "p" || stats.Models[0].Turns != 2 {
		t.Fatalf("models = %+v", stats.Models)
	}
}

func TestUsageStatsStreaks(t *testing.T) {
	day := func(d int) usageEntry {
		return usageEntry{at: time.Date(2026, 11, d, 12, 0, 0, 0, time.UTC).Unix(), in: 1, out: 1, turn: true}
	}
	session := func(days ...int) []*sessionUsage {
		su := &sessionUsage{id: "s", labels: []string{"m @ p"}}
		for _, d := range days {
			su.entries = append(su.entries, day(d))
		}
		return []*sessionUsage{su}
	}
	now := time.Date(2026, 11, 15, 18, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name                     string
		days                     []int
		active, current, longest int
	}{
		{"through today", []int{1, 2, 3, 4, 5, 9, 13, 14, 15}, 9, 3, 5},
		{"through yesterday", []int{12, 13, 14}, 3, 3, 3},
		{"broken before yesterday", []int{11, 12, 13}, 3, 0, 3},
		{"none", nil, 0, 0, 0},
	} {
		stats := aggregateUsage(session(tc.days...), UsageStatsOptions{Days: 30, Location: time.UTC, Now: now})
		if stats.ActiveDays != tc.active || stats.CurrentStreak != tc.current || stats.LongestStreak != tc.longest {
			t.Errorf("%s: active %d current %d longest %d", tc.name, stats.ActiveDays, stats.CurrentStreak, stats.LongestStreak)
		}
	}
	// The longest streak only counts days inside the range (the 5th to the 15th).
	stats := aggregateUsage(session(1, 2, 3, 4, 5, 6, 15), UsageStatsOptions{Days: 11, Location: time.UTC, Now: now})
	if stats.LongestStreak != 2 || stats.ActiveDays != 3 || stats.CurrentStreak != 1 {
		t.Fatalf("range-limited streaks = %+v", stats)
	}
}

func TestUsageStatsCountsSubagentsOnce(t *testing.T) {
	st := openUsageStore(t)
	parent := saveSession(t, st, "/w", prompt("2026-11-15T01:00:00Z"), reply("msg-parent", "2026-11-15T01:00:10Z", 100, 10, "main @ p"))
	sub := map[string]ai.Usage{"helper @ p": {PromptTokens: 50, CompletionTokens: 5}}
	if err := st.SetUsage(parent, ai.UsageSummary{Total: ai.Usage{PromptTokens: 100, CompletionTokens: 10}, Subagents: sub}); err != nil {
		t.Fatal(err)
	}
	child := []ai.Message{prompt("2026-11-15T01:00:02Z"), reply("msg-child", "2026-11-15T01:00:05Z", 50, 5, "helper @ p")}
	childID, err := st.SaveSubagentTranscript(parent, "task1", child, "helper", "p")
	if err != nil {
		t.Fatal(err)
	}
	// An older parent whose subagent transcript was never saved keeps its SubUsage.
	legacy := saveSession(t, st, "/w", prompt("2026-11-14T01:00:00Z"), reply("msg-legacy", "2026-11-14T01:00:10Z", 10, 1, "main @ p"))
	if err := st.SetUsage(legacy, ai.UsageSummary{Total: ai.Usage{PromptTokens: 10, CompletionTokens: 1}, Subagents: map[string]ai.Usage{"helper @ p": {PromptTokens: 30, CompletionTokens: 3}}}); err != nil {
		t.Fatal(err)
	}

	stats := usageStats(t, st, 30, "UTC", "2026-11-15T10:00:00Z")
	if stats.Totals.Input != 190 || stats.Totals.Output != 19 || stats.Totals.Turns != 3 {
		t.Fatalf("totals = %+v (child %s)", stats.Totals, childID)
	}
	if stats.Sessions != 2 || stats.LongestSession == nil || stats.LongestSession.ID != parent || stats.LongestSession.Total != 165 {
		t.Fatalf("sessions = %d longest = %+v", stats.Sessions, stats.LongestSession)
	}
	for _, m := range stats.Models {
		if m.Model == "helper" && (m.Total() != 88 || m.Turns != 1) {
			t.Fatalf("helper = %+v", m)
		}
	}
}

func TestUsageStatsDoesNotCountForkedRepliesTwice(t *testing.T) {
	st := openUsageStore(t)
	src := saveSession(t, st, "/w", prompt("2026-11-15T01:00:00Z"), reply("msg-1", "2026-11-15T01:00:05Z", 100, 10, "m @ p"),
		prompt("2026-11-15T02:00:00Z"), reply("", "2026-11-15T02:00:05Z", 40, 4, "m @ p"))
	fork, err := st.Fork(src, 3, "fork")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(fork, 4, []ai.Message{4: prompt("2026-11-15T03:00:00Z"), 5: reply("msg-3", "2026-11-15T03:00:05Z", 7, 3, "m @ p")}, "m", "p"); err != nil {
		t.Fatal(err)
	}
	stats := usageStats(t, st, 30, "UTC", "2026-11-15T10:00:00Z")
	if stats.Totals.Total() != 164 || stats.Totals.Turns != 3 || stats.Sessions != 2 {
		t.Fatalf("totals = %+v sessions %d", stats.Totals, stats.Sessions)
	}
}

func TestUsageStatsLegacyMetadataTotals(t *testing.T) {
	st := openUsageStore(t)
	id := saveSession(t, st, "/old", prompt("2026-11-01T08:00:00Z"), ai.Message{Role: "assistant", Content: "ok"}, prompt("2026-11-03T08:00:00Z"))
	cached := ai.Usage{PromptTokens: 1000, CompletionTokens: 100, PromptCacheHitTokens: 400}
	if err := st.SetUsage(id, ai.UsageSummary{Total: cached, Models: map[string]ai.Usage{"old @ prov": cached}}); err != nil {
		t.Fatal(err)
	}
	// Without per-model totals the session's route labels the usage.
	plain := saveSession(t, st, "/old", prompt("2026-11-02T08:00:00Z"))
	if err := st.SetUsage(plain, ai.UsageSummary{Total: ai.Usage{PromptTokens: 10, CompletionTokens: 1}}); err != nil {
		t.Fatal(err)
	}
	stats := usageStats(t, st, 30, "UTC", "2026-11-15T10:00:00Z")
	if len(stats.Daily) != 2 || stats.Daily[0].Date != "2026-11-02" || stats.Daily[1].Date != "2026-11-03" {
		t.Fatalf("daily = %+v", stats.Daily)
	}
	if stats.Totals.Input != 1010 || stats.Totals.CacheRead != 400 || stats.Totals.Turns != 0 || stats.Sessions != 2 {
		t.Fatalf("totals = %+v", stats.Totals)
	}
	if stats.Models[0].Model != "old" || stats.Models[0].Provider != "prov" || stats.Models[1].Model != "m" {
		t.Fatalf("models = %+v", stats.Models)
	}
	if stats.LongestSession == nil || stats.LongestSession.ID != id || stats.LongestSession.Title != "hi" {
		t.Fatalf("longest = %+v", stats.LongestSession)
	}
}

func TestUsageStatsCacheFollowsFileSizeAndModTime(t *testing.T) {
	st := openUsageStore(t)
	id := saveSession(t, st, "/w", prompt("2026-11-15T01:00:00Z"), reply("msg-1", "2026-11-15T01:00:05Z", 100, 10, "m @ p"))
	if got := usageStats(t, st, 7, "UTC", "2026-11-15T10:00:00Z").Totals.Total(); got != 110 {
		t.Fatalf("total = %d", got)
	}
	path := st.TranscriptPath(id)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Same size and modification time: the cached entry is used, the file is not read.
	edited := bytes.Replace(data, []byte(`"prompt_tokens":100`), []byte(`"prompt_tokens":900`), 1)
	if len(edited) != len(data) || bytes.Equal(edited, data) {
		t.Fatal("edit did not apply")
	}
	if err := os.WriteFile(path, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got := usageStats(t, st, 7, "UTC", "2026-11-15T10:00:00Z").Totals.Total(); got != 110 {
		t.Fatalf("cached total = %d", got)
	}
	// A new modification time invalidates it.
	if err := os.Chtimes(path, info.ModTime().Add(time.Second), info.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := usageStats(t, st, 7, "UTC", "2026-11-15T10:00:00Z").Totals.Total(); got != 910 {
		t.Fatalf("reparsed total = %d", got)
	}
	// So does an ordinary save, and a deleted session drops out of the cache.
	if err := st.Save(id, 2, []ai.Message{2: prompt("2026-11-15T02:00:00Z"), 3: reply("msg-2", "2026-11-15T02:00:05Z", 1, 1, "m @ p")}, "m", "p"); err != nil {
		t.Fatal(err)
	}
	if got := usageStats(t, st, 7, "UTC", "2026-11-15T10:00:00Z").Totals.Total(); got != 912 {
		t.Fatalf("saved total = %d", got)
	}
	if err := st.Delete(id); err != nil {
		t.Fatal(err)
	}
	if got := usageStats(t, st, 7, "UTC", "2026-11-15T10:00:00Z"); got.Totals.Total() != 0 || got.Sessions != 0 || got.LongestSession != nil || len(st.usageStats.files) != 0 {
		t.Fatalf("after delete = %+v, cache %d", got, len(st.usageStats.files))
	}
}
