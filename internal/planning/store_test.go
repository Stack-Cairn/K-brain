package planning

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, e := Open(filepath.Join(t.TempDir(), "planning.json"))
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func mutation(action string, d Item, x Item) Mutation {
	m := Mutation{RequestID: ID(), Action: action, Data: d}
	if x != nil {
		m.ID = text(x, "id")
		rev := uint64(number(x, "revision"))
		m.ExpectedRevision = &rev
	}
	return m
}
func applyTest(t *testing.T, s *Store, action string, d Item, x Item) Item {
	t.Helper()
	out, e := s.Mutate(mutation(action, d, x))
	if e != nil || out.Status != "ok" {
		t.Fatalf("%s: %+v %v", action, out, e)
	}
	if out.Item == nil {
		return nil
	}
	return out.Item.(map[string]any)
}
func readTest(t *testing.T, s *Store) Snapshot {
	t.Helper()
	v, e := s.Query(0, 0)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestPersistenceCASIdempotencyAndRollback(t *testing.T) {
	s := openTest(t)
	task := applyTest(t, s, "todo.create", Item{"title": "task"}, nil)
	request := mutation("todo.update", Item{"title": "updated"}, task)
	first, e := s.Mutate(request)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.Mutate(request)
	if e != nil || !again.Replayed || again.Seq != first.Seq {
		t.Fatalf("replay %+v %v", again, e)
	}
	reused := clone(request)
	reused.Data["title"] = "different"
	if _, e = s.Mutate(reused); e == nil {
		t.Fatal("accepted reused ID")
	}
	conflict, e := s.Mutate(mutation("todo.delete", Item{}, task))
	if e != nil || conflict.Status != "conflict" {
		t.Fatalf("conflict %+v %v", conflict, e)
	}
	before := readTest(t, s)
	if _, e = s.Mutate(mutation("todo.create", Item{"title": "invalid", "parentId": "missing"}, nil)); e == nil {
		t.Fatal("accepted invalid parent")
	}
	if readTest(t, s).Seq != before.Seq {
		t.Fatal("failed mutation committed")
	}
	reopened, e := Open(s.path)
	if e != nil {
		t.Fatal(e)
	}
	if text(readTest(t, reopened).Todos[0], "title") != "updated" {
		t.Fatal("lost task")
	}
	out, e := reopened.Mutate(request)
	if e != nil || !out.Replayed {
		t.Fatal("lost idempotency")
	}
	info, e := os.Stat(s.path)
	// Windows reports every writable file as 0666; there is no POSIX mode to check.
	if e != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatalf("permissions %v %v", info, e)
	}
}
func TestConcurrentWritesAndPersistenceFailure(t *testing.T) {
	s := openTest(t)
	task := applyTest(t, s, "todo.create", Item{"title": "task"}, nil)
	var wg sync.WaitGroup
	results := make(chan Result, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := s.Mutate(mutation("todo.update", Item{"title": ID()}, task))
			if e != nil {
				t.Error(e)
			}
			results <- r
		}()
	}
	wg.Wait()
	close(results)
	ok := 0
	for r := range results {
		if r.Status == "ok" {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("%d concurrent writes succeeded", ok)
	}
	before := readTest(t, s)
	s.path = filepath.Join(s.path, "cannot-write")
	if _, e := s.Mutate(mutation("todo.create", Item{"title": "never persisted"}, nil)); e == nil {
		t.Fatal("expected write failure")
	}
	if readTest(t, s).Seq != before.Seq {
		t.Fatal("memory changed on failed save")
	}
}
func TestHierarchyMoveTrashAndRestore(t *testing.T) {
	s := openTest(t)
	group := applyTest(t, s, "group.create", Item{"name": "list"}, nil)
	parent := applyTest(t, s, "todo.create", Item{"title": "parent"}, nil)
	child := applyTest(t, s, "todo.create", Item{"title": "child", "parentId": parent["id"]}, nil)
	if _, e := s.Mutate(mutation("todo.move", Item{"parentId": child["id"]}, parent)); e == nil {
		t.Fatal("cycle accepted")
	}
	parent = applyTest(t, s, "todo.move", Item{"groupId": group["id"]}, parent)
	child = find(readTest(t, s).Todos, text(child, "id"))
	if text(child, "groupId") != text(group, "id") {
		t.Fatal("subtree not moved")
	}
	parent = applyTest(t, s, "todo.delete", Item{}, parent)
	if text(find(readTest(t, s).Todos, text(child, "id")), "parentId") != "" {
		t.Fatal("child not promoted")
	}
	parent = applyTest(t, s, "todo.restore", Item{}, parent)
	if _, e := s.Mutate(mutation("todo.purge", Item{}, parent)); e == nil {
		t.Fatal("purged live task")
	}
	parent = applyTest(t, s, "todo.delete", Item{}, parent)
	applyTest(t, s, "todo.purge", Item{}, parent)
	if find(readTest(t, s).Todos, text(parent, "id")) != nil {
		t.Fatal("purge failed")
	}
}
func TestCalendarRecurrenceExceptionAndImport(t *testing.T) {
	s := openTest(t)
	cal := readTest(t, s).Calendars[0]
	ev := applyTest(t, s, "event.create", Item{"calendarId": cal["id"], "title": "daily", "time": Item{"kind": "allDay", "startDate": "2026-10-05", "endDateExclusive": "2026-10-06", "timeZone": "UTC"}, "recurrence": Item{"frequency": "daily", "interval": 1, "count": 3, "excludedDates": []any{}}}, nil)
	from, _ := time.Parse("2006-01-02", "2026-10-05")
	v, e := s.Query(from.UnixMilli(), from.AddDate(0, 0, 5).UnixMilli())
	if e != nil || len(v.Events) != 3 {
		t.Fatalf("expand %d %v", len(v.Events), e)
	}
	exception := applyTest(t, s, "event.exception", Item{"date": "2026-10-06", "title": "exception"}, ev)
	v, e = s.Query(from.UnixMilli(), from.AddDate(0, 0, 5).UnixMilli())
	if e != nil || len(v.Events) != 3 {
		t.Fatalf("exception %d %v", len(v.Events), e)
	}
	master := find(readTest(t, s).Events, text(ev, "id"))
	applyTest(t, s, "event.restoreException", Item{"date": "2026-10-06", "exceptionId": exception["id"], "exceptionRevision": exception["revision"]}, master)
	entries := Item{"entries": []any{Item{"uid": "same-source", "title": "import", "time": Item{"kind": "timed", "startAt": from.UnixMilli(), "endAt": from.Add(time.Hour).UnixMilli(), "timeZone": "UTC"}}}}
	r := applyTest(t, s, "calendar.import", entries, cal)
	if number(r, "imported") != 1 {
		t.Fatal(r)
	}
	r = applyTest(t, s, "calendar.import", entries, cal)
	if number(r, "skipped") != 1 {
		t.Fatal(r)
	}
}
func TestDSTAndTimeValidation(t *testing.T) {
	event := newEvent(Item{"title": "DST", "time": Item{"kind": "timed", "startAt": time.Date(2026, 3, 7, 7, 30, 0, 0, time.UTC).UnixMilli(), "endAt": time.Date(2026, 3, 7, 8, 30, 0, 0, time.UTC).UnixMilli(), "timeZone": "America/New_York"}, "recurrence": Item{"frequency": "daily", "interval": 1, "count": 3}})
	items, e := occurrences(event, time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC).UnixMilli(), time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC).UnixMilli())
	if e != nil || len(items) != 2 {
		t.Fatalf("DST gap %d %v", len(items), e)
	}
	a, b, e := bounds(Item{"kind": "allDay", "startDate": "2026-03-08", "endDateExclusive": "2026-03-09", "timeZone": "America/New_York"})
	if e != nil || b-a != 23*3600000 {
		t.Fatalf("DST day %d %v", b-a, e)
	}
}
func TestMigrationIdempotencyAndConflict(t *testing.T) {
	legacy := openTest(t)
	task := applyTest(t, legacy, "todo.create", Item{"title": "legacy"}, nil)
	snapshot := readTest(t, legacy)
	target := openTest(t)
	result, e := target.Import(snapshot, "desktop-A", nil)
	if e != nil || result["status"] != "ok" {
		t.Fatalf("import %v %v", result, e)
	}
	result, e = target.Import(snapshot, "desktop-A", nil)
	if e != nil || result["status"] != "already_imported" {
		t.Fatal(result, e)
	}
	updated := applyTest(t, target, "todo.update", Item{"title": "backend edit"}, task)
	if _, e = target.Import(snapshot, "desktop-B", nil); e == nil {
		t.Fatal("migration overwrote backend data")
	}
	if text(find(readTest(t, target).Todos, text(task, "id")), "title") != text(updated, "title") {
		t.Fatal("conflict changed data")
	}
}
func TestReminderLeaseAndStaleAcknowledgment(t *testing.T) {
	s := openTest(t)
	task := applyTest(t, s, "todo.create", Item{"title": "due", "dueAt": time.Now().Add(-time.Minute).UnixMilli(), "dueReminder": true}, nil)
	due, e := s.Claim()
	if e != nil || len(due) != 1 {
		t.Fatal(due, e)
	}
	again, e := s.Claim()
	if e != nil || len(again) != 0 {
		t.Fatal("duplicate lease")
	}
	ack := clone(due[0])
	ack["success"] = true
	if _, e = s.Finish(ack); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Finish(ack); e == nil {
		t.Fatal("stale ack accepted")
	}
	task = applyTest(t, s, "todo.update", Item{"status": "completed"}, task)
	_ = task
	due, e = s.Claim()
	if e != nil || len(due) != 0 {
		t.Fatal("completed task reminder delivered")
	}
}
func TestSubscriptionRefreshWithoutDesktop(t *testing.T) {
	// Imports keep events from 30 days back; a fixed date ages out of that window.
	start := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Hour)
	feed := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:test-feed
DTSTART:` + start.Format("20060102T150405Z") + `
DTEND:` + start.Add(time.Hour).Format("20060102T150405Z") + `
SUMMARY:Subscription event
END:VEVENT
END:VCALENDAR
`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(feed, "\n", "\r\n")))
	}))
	defer server.Close()
	s := openTest(t)
	result, e := s.Subscription("subscription.create", Item{"name": "feed", "url": server.URL + "/private-secret", "refreshMinutes": 15})
	if e != nil {
		t.Fatal(e)
	}
	cal := result.(Item)
	s.refreshDue(context.Background())
	snap := readTest(t, s)
	if len(snap.Events) != 1 || text(snap.Events[0], "title") != "Subscription event" {
		t.Fatal(snap.Events, snap.Subscriptions)
	}
	b, _ := json.Marshal(snap)
	if strings.Contains(string(b), "private-secret") {
		t.Fatal("secret URL leaked")
	}
	if _, e = s.Mutate(mutation("event.update", Item{"title": "write"}, snap.Events[0])); e == nil {
		t.Fatal("subscription event writable")
	}
	s.refreshDue(context.Background())
	if len(readTest(t, s).Events) != 1 {
		t.Fatal("duplicate feed")
	}
	if _, e = s.Subscription("subscription.delete", Item{"id": cal["id"]}); e != nil {
		t.Fatal(e)
	}
	if len(readTest(t, s).Events) != 0 {
		t.Fatal("orphan event")
	}
}

func TestSubscriptionInvalidRefreshPreservesSnapshot(t *testing.T) {
	title := "original"
	date := time.Now().UTC().Format("20060102")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(calendarICS("BEGIN:VEVENT\nUID:stable\nDTSTART:" + date + "T090000Z\nDURATION:PT1H\nSUMMARY:" + title + "\nEND:VEVENT")))
	}))
	defer server.Close()
	s := openTest(t)
	result, e := s.Subscription("subscription.create", Item{"name": "rollback", "url": server.URL})
	if e != nil {
		t.Fatal(e)
	}
	id := result.(Item)["id"]
	s.refreshDue(context.Background())
	before := readTest(t, s)
	title = strings.Repeat("x", 501)
	if _, e = s.Subscription("subscription.refresh", Item{"id": id}); e != nil {
		t.Fatal(e)
	}
	s.refreshDue(context.Background())
	after := readTest(t, s)
	a, _ := json.Marshal(before.Events)
	b, _ := json.Marshal(after.Events)
	if string(a) != string(b) {
		t.Fatal("failed refresh changed events")
	}
	if text(after.Subscriptions[0], "lastError") != "E:subscription_not_ics" {
		t.Fatal(after.Subscriptions)
	}
}

func TestSharedTimeZonePreferenceRevisionAndPersistence(t *testing.T) {
	s := openTest(t)
	initial := s.TimeZoneSettings()
	rev := uint64(number(initial, "revision"))
	if _, e := s.UpdateTimeZone("America/New_York", &rev); e != nil {
		t.Fatal(e)
	}
	if _, e := s.UpdateTimeZone("Europe/London", &rev); e == nil {
		t.Fatal("accepted stale preference")
	}
	current := s.TimeZoneSettings()
	if text(current, "preference") != "America/New_York" {
		t.Fatal(current)
	}
	if _, e := s.UpdateTimeZone("", nil); e != nil {
		t.Fatal(e)
	}
	current = s.TimeZoneSettings()
	if text(current, "preference") != "" || text(current, "timeZone") != systemZone() {
		t.Fatal(current)
	}
	reopened, e := Open(s.path)
	if e != nil {
		t.Fatal(e)
	}
	if number(reopened.TimeZoneSettings(), "revision") != number(current, "revision") {
		t.Fatal("lost preference revision")
	}
	if _, e := s.UpdateTimeZone("Invalid/Zone", nil); e == nil {
		t.Fatal("accepted bad timezone")
	}
}

func TestSubscriptionProviderErrorsAndURLNormalization(t *testing.T) {
	url, e := normalizedURL("  webcal://example.org/a.ics  ")
	if e != nil || url != "https://example.org/a.ics" {
		t.Fatal(url, e)
	}
	for _, tc := range []struct {
		status int
		code   string
	}{{401, "E:subscription_auth_required"}, {403, "E:subscription_auth_required"}, {404, "E:subscription_unavailable"}, {410, "E:subscription_unavailable"}, {429, "E:subscription_rate_limited"}, {500, "E:subscription_fetch_failed"}, {200, "E:subscription_not_ics"}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte("<html>sign in</html>"))
		}))
		_, e := download(context.Background(), server.URL+"/secret-url")
		server.Close()
		if e == nil || subscriptionError(e) != tc.code {
			t.Fatalf("%d: %v", tc.status, e)
		}
	}
}

func TestAutomaticTimeZoneFollowsBackendAfterRestart(t *testing.T) {
	s := openTest(t)
	t.Setenv("TZ", "Europe/Paris")
	if _, e := s.UpdateTimeZone("", nil); e != nil {
		t.Fatal(e)
	}
	t.Setenv("TZ", "Asia/Tokyo")
	next, e := Open(s.path)
	if e != nil {
		t.Fatal(e)
	}
	if text(next.TimeZoneSettings(), "timeZone") != "Asia/Tokyo" {
		t.Fatal(next.TimeZoneSettings())
	}
}
