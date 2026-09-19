package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestFileStoreReopen(t *testing.T) {
	home := t.TempDir()
	st, err := OpenHome(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	id, err := st.Create("D:/project", "model1", "demo")
	if err != nil {
		t.Fatal(err)
	}
	msgs := []ai.Message{{Role: "system", Content: "system"}, {Role: "user", Content: strings.Repeat("长消息", 30000), Authored: true}, {Role: "assistant", Content: "answer"}}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.Save(id, 0, msgs, "model1", "demo"))
	must(st.SetTitle(id, "会话标题"))
	must(st.SetGoal(id, "goal"))
	must(st.SetTodos(id, `[{"text":"task"}]`))
	must(st.SetTags(id, []string{"work,one", "中文"}))
	must(st.SetPinned(id, true))
	must(st.SetEffort(id, "high"))
	usage := ai.Usage{PromptTokens: 23, CompletionTokens: 7}
	models := map[string]ai.Usage{
		"model1 @ demo": {PromptTokens: 10, CompletionTokens: 3},
		"model2 @ demo": {PromptTokens: 13, CompletionTokens: 4},
	}
	must(st.SetUsage(id, ai.UsageSummary{Total: ai.Usage{PromptTokens: 23, PromptCacheHitTokens: 5, CompletionTokens: 7}, Models: models, Subagents: map[string]ai.Usage{"model2": usage}}))
	task := Task{ID: "task-1", Description: "task", Status: "done", StartedAt: time.Now().UTC(), EndedAt: time.Now().UTC()}
	must(st.SaveTask(id, task))
	must(st.SetSnapshot(id, 1, "stash-ref"))
	anchor := time.Now().UTC()
	schedule, err := st.AddSchedule(id, "@every 1m", "prompt", anchor)
	must(err)
	must(st.MarkFired(id, schedule, anchor.Add(time.Minute)))
	must(st.RecordCompaction(id, 2, "summary", "model2", usage))
	_, err = st.SaveSubagentTranscript(id, "task-1", msgs[2:], "model2", "demo")
	must(err)
	meta, loaded, err := st.Load(id)
	must(err)
	path := st.TranscriptPath(id)
	if path != filepath.Join(home, "sessions", id, "session.jsonl") {
		t.Fatalf("path = %s", path)
	}
	data, err := os.ReadFile(path)
	must(err)
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	for i, line := range lines {
		if !json.Valid(line) {
			t.Fatalf("line %d is invalid", i)
		}
	}
	var header struct {
		Type    string         `json:"type"`
		Payload metadataRecord `json:"payload"`
	}
	must(json.Unmarshal(lines[0], &header))
	if header.Type != "session_meta" || header.Payload.ID != id {
		t.Fatalf("missing readable ID: %s", lines[0])
	}
	must(st.Close())
	st, err = OpenHome(home)
	must(err)
	gotMeta, gotLoaded, err := st.Load(id)
	must(err)
	if !reflect.DeepEqual(meta, gotMeta) || !reflect.DeepEqual(loaded, gotLoaded) {
		t.Fatal("reopen lost metadata/context")
	}
	if !reflect.DeepEqual(gotMeta.ModelUsage, models) {
		t.Fatalf("reopen lost model usage: %+v", gotMeta.ModelUsage)
	}
	if !reflect.DeepEqual(st.RawMessages(id), msgs) {
		t.Fatal("reopen lost raw messages")
	}
	if st.Todos(id) != `[{"text":"task"}]` {
		t.Fatal("todos lost")
	}
	tasks, err := st.LoadTasks(id)
	must(err)
	if len(tasks) != 1 || !reflect.DeepEqual(tasks[0], task) {
		t.Fatalf("tasks lost: %+v", tasks)
	}
	if st.Snapshots(id)[1] != "stash-ref" {
		t.Fatal("snapshot lost")
	}
	schedules := st.Schedules(id)
	if len(schedules) != 1 || !schedules[0].LastFire.Equal(anchor.Add(time.Minute)) {
		t.Fatal("schedule lost")
	}
	if cs := st.Compactions(id); len(cs) != 1 || cs[0].Model != "model2" {
		t.Fatal("compaction lost")
	}
	sub, err := st.SubagentTranscript(id, "task-1")
	must(err)
	if !reflect.DeepEqual(sub, msgs[2:]) {
		t.Fatal("subagent messages lost")
	}
	must(st.DeleteFrom(id, 2))
	must(st.Close())
	st, err = OpenHome(home)
	must(err)
	if len(st.RawMessages(id)) != 2 {
		t.Fatal("rewind not persisted")
	}
}

func TestProjectStoreGroupsSessionsByWorkspace(t *testing.T) {
	home := t.TempDir()
	st, err := OpenProjectHome(home)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	one, err := st.Create(filepath.Join(home, "one"), "model1", "demo")
	if err != nil {
		t.Fatal(err)
	}
	two, err := st.Create(filepath.Join(home, "two"), "model1", "demo")
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Fatal("session IDs should be unique")
	}
	for id, want := range map[string]string{one: "one", two: "two"} {
		path := st.TranscriptPath(id)
		project := filepath.Base(filepath.Dir(filepath.Dir(path)))
		if !strings.HasPrefix(project, want+"-") {
			t.Fatalf("%s stored outside project directory: %s", id, path)
		}
		if !strings.Contains(path, filepath.Join("sessions", project)) {
			t.Fatalf("%s is missing stable project grouping: %s", id, path)
		}
	}
	if _, _, err := st.Load(one); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Load(two); err != nil {
		t.Fatal(err)
	}
}

func TestFileStoreRejectsInvalidIDs(t *testing.T) {
	st, id := seeded(t)
	for _, bad := range []string{"", "../outside", `..\outside`, "a/b", "a:b", ".", ".."} {
		if st.TranscriptPath(bad) != "" {
			t.Fatalf("unsafe path: %q", bad)
		}
		if err := st.SetTitle(bad, "x"); err == nil {
			t.Fatalf("unsafe write: %q", bad)
		}
		if _, err := st.SaveSubagentTranscript(id, bad+"/", nil, "m", "p"); err == nil {
			t.Fatalf("unsafe subagent: %q", bad)
		}
	}
	if err := st.Save(id, -1, nil, "m", "p"); err == nil {
		t.Fatal("negative offset accepted")
	}
}

func TestFileStoreIgnoresLegacyDatabase(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, "sessions.db")
	original := []byte("not a readable database")
	if err := os.WriteFile(legacy, original, 0600); err != nil {
		t.Fatal(err)
	}
	st, err := OpenHome(home)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	metas, err := st.Recent(10)
	if err != nil || len(metas) != 0 {
		t.Fatalf("legacy file affected store: %v", err)
	}
	if _, err := st.Create("cwd", "m", "p"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(legacy)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("legacy data was touched")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(legacy + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("database sidecar created")
		}
	}
}

func TestFileStoreMultipleInstances(t *testing.T) {
	home := t.TempDir()
	st, err := OpenHome(home)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	other, err := OpenHome(home)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	id, err := st.Create("cwd", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for n := 0; n < 40; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			writer := st
			if n%2 == 1 {
				writer = other
			}
			if err := writer.SaveTask(id, Task{ID: fmt.Sprint(n), Description: "concurrent"}); err != nil {
				t.Error(err)
			}
		}(n)
	}
	wg.Wait()
	tasks, err := st.LoadTasks(id)
	if err != nil || len(tasks) != 40 {
		t.Fatalf("lost concurrent updates: %d %v", len(tasks), err)
	}
}

func TestFileStoreMultipleProcesses(t *testing.T) {
	if home := os.Getenv("K_BRAIN_SESSION_TEST_HOME"); home != "" {
		st, err := OpenHome(home)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		for n := 0; n < 10; n++ {
			id := fmt.Sprintf("%d-%d", os.Getpid(), n)
			if err := st.SaveTask(os.Getenv("K_BRAIN_SESSION_TEST_ID"), Task{ID: id}); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	home := t.TempDir()
	st, err := OpenHome(home)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.Create("cwd", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for n := 0; n < 3; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestFileStoreMultipleProcesses$", "-test.timeout=30s")
			cmd.Env = append(os.Environ(), "K_BRAIN_SESSION_TEST_HOME="+home, "K_BRAIN_SESSION_TEST_ID="+id)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("child: %v %s", err, out)
			}
		}()
	}
	wg.Wait()
	tasks, err := st.LoadTasks(id)
	if err != nil || len(tasks) != 30 {
		t.Fatalf("lost process updates: %d %v", len(tasks), err)
	}
}

func TestFileStoreRejectsInvalidRecords(t *testing.T) {
	for _, kind := range []string{"version", "id", "duplicate", "truncated", "unknown", "sequence"} {
		t.Run(kind, func(t *testing.T) {
			st, id := seeded(t)
			path := st.TranscriptPath(id)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "version":
				data = bytes.Replace(data, []byte(`"version":1`), []byte(`"version":99`), 1)
			case "id":
				data = bytes.Replace(data, []byte(`"id":"`+id+`"`), []byte(`"id":"wrong"`), 1)
			case "duplicate":
				data = append(data, bytes.SplitAfter(data, []byte("\n"))[0]...)
			case "truncated":
				data = data[:len(data)-4]
			case "unknown":
				data = append(data, []byte(`{"type":"future_record","payload":{}}`)...)
			case "sequence":
				data = append(data, []byte(`{"type":"message","payload":{"seq":-1,"message":{"role":"user","content":"invalid"}}}`)...)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := st.Load(id); err == nil {
				t.Fatal("invalid record accepted")
			}
			if err := st.SetGoal(id, "overwrite"); err == nil {
				t.Fatal("invalid file overwritten")
			}
		})
	}
}

func TestFileStorePrefixAndUnicodeTitle(t *testing.T) {
	st, id := seeded(t)
	meta, _, err := st.Load(id[:4])
	if err != nil || meta.ID != id {
		t.Fatalf("unique prefix failed: %v", err)
	}
	base := "完成 100%_任务"
	title, err := st.ForkTitle(base)
	if err != nil || title != base+" (fork #1)" {
		t.Fatalf("title: %q %v", title, err)
	}
	fork, err := st.Fork(id, 2, title)
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err = st.Load(fork)
	if err != nil || meta.ForkedFrom != id {
		t.Fatal("fork metadata lost")
	}
	next, err := st.ForkTitle(title)
	if err != nil || next != base+" (fork #2)" {
		t.Fatalf("next title: %q %v", next, err)
	}
	if got := truncate(strings.Repeat("脑", 70), 64); len([]rune(got)) != 64 || !strings.HasSuffix(got, "…") {
		t.Fatal("unicode title truncated incorrectly")
	}
}
