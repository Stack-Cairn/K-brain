package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestRunResumeEmptyAndNonSystemHistory(t *testing.T) {
	for _, history := range []string{"empty", "user-first", "system-first"} {
		t.Run(history, func(t *testing.T) {
			t.Chdir(t.TempDir())
			var requests []ai.Request
			runFixture(t, "reply", &requests)
			dir, _ := configDir()
			store, err := sessionOpen(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			id, err := store.Create(cwd(), "test", "testprov")
			if err != nil {
				t.Fatal(err)
			}
			var msgs []ai.Message
			if history == "system-first" {
				msgs = append(msgs, ai.Message{Role: "system", Content: "old system"})
			}
			if history != "empty" {
				msgs = append(msgs, ai.Message{Role: "user", Content: "preserved first message"}, ai.Message{Role: "assistant", Content: "earlier reply"})
			}
			if err := store.Save(id, 0, msgs, "test", "testprov"); err != nil {
				t.Fatal(err)
			}
			if _, err := runCapture(t, "", "--quiet", "--system", "new system", "--resume", id, "continue"); err != nil {
				t.Fatal(err)
			}
			if len(requests) != 1 {
				t.Fatalf("requests=%d", len(requests))
			}
			got := requests[0].Messages
			if got[0].Role != "system" || got[0].Content != "new system" {
				t.Fatalf("system override lost: %+v", got)
			}
			if history != "empty" && (len(got) < 4 || got[1].Content != "preserved first message") {
				t.Fatalf("first conversation message dropped: %+v", got)
			}
			_, saved, err := store.Load(id)
			if err != nil || len(saved) != len(msgs)+2 {
				t.Fatalf("resumed transcript: %d, %v", len(saved), err)
			}
			if len(msgs) > 0 && !reflect.DeepEqual(saved[:len(msgs)], msgs) {
				t.Fatalf("restoring a session rewrote its original records: %+v", saved)
			}
		})
	}
}

func TestRunSessionsUseProjectDirectories(t *testing.T) {
	runFixture(t, "ok", nil)
	dir, _ := configDir()
	store, err := sessionOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for range 2 {
		project := t.TempDir()
		t.Chdir(project)
		if _, err := runCapture(t, "", "--quiet", "hello"); err != nil {
			t.Fatal(err)
		}
	}
	metas, err := store.Recent(10)
	if err != nil || len(metas) != 2 {
		t.Fatalf("sessions: %+v, %v", metas, err)
	}
	parents := map[string]bool{}
	for _, meta := range metas {
		path := store.TranscriptPath(meta.ID)
		rel, err := filepath.Rel(store.SessionsDir(), path)
		if err != nil || len(strings.Split(rel, string(filepath.Separator))) != 3 {
			t.Fatalf("not project-grouped: %s, %v", path, err)
		}
		parents[filepath.Dir(filepath.Dir(path))] = true
	}
	if len(parents) != 2 {
		t.Fatal("different project sessions share one directory")
	}
	listed := captureStdout(t, func() {
		if err := sessionsCLI(); err != nil {
			t.Error(err)
		}
	})
	for _, meta := range metas {
		if !strings.Contains(listed, meta.ID) {
			t.Fatalf("kn sessions omitted grouped run %s: %s", meta.ID, listed)
		}
	}
}

func TestRunStorageFailureDoesNotCallModel(t *testing.T) {
	var requests []ai.Request
	runFixture(t, "unused", &requests)
	dir, _ := configDir()
	if err := os.WriteFile(filepath.Join(dir, "sessions"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runCapture(t, "", "hello"); err == nil || !strings.Contains(err.Error(), "session storage") {
		t.Fatalf("storage error hidden: %v", err)
	}
	if len(requests) != 0 {
		t.Fatal("model called without requested session persistence")
	}
	if _, err := runCapture(t, "", "--no-session", "hello"); err != nil {
		t.Fatalf("no-session should not require storage: %v", err)
	}
}

func TestRunTimeoutWhileReadingStdin(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	start := time.Now()
	err = runCLI([]string{"--timeout", "100ms", "--no-session", "hello"})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "run timed out after 100ms") || time.Since(start) > 2*time.Second {
		t.Fatalf("stdin timeout: %v (%s)", err, time.Since(start))
	}
}

func TestReadRunInputPropagatesReadError(t *testing.T) {
	f, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := readRunInput(context.Background(), f); err == nil {
		t.Fatal("directory read error was ignored")
	}
}

func TestRunStartupHonorsTimeout(t *testing.T) {
	for _, stage := range []string{"catalog", "credential", "hook"} {
		t.Run(stage, func(t *testing.T) {
			runFixture(t, "unused", nil)
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				select {
				case <-r.Context().Done():
				case <-time.After(3 * time.Second):
				}
			}))
			defer srv.Close()
			prov := cfg.Providers["testprov"]
			prov.BaseURL = srv.URL + "/v1"
			switch stage {
			case "catalog":
				cfg.DefaultModel = "missing"
			case "credential":
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv("K_BRAIN_RUN_CREDENTIAL_HELPER", "1")
				prov.APIKey = `!"` + exe + `" -test.run=^TestRunCredentialHelper$`
			case "hook":
				command := "sleep 10"
				if runtime.GOOS == "windows" {
					command = "Start-Sleep -Seconds 10"
				}
				cfg.Hooks = map[string][]config.Hook{"SessionStart": {{Command: command}}}
			}
			cfg.Providers["testprov"] = prov
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			_, err = runCapture(t, "", "--timeout", "200ms", "--no-session", "hello")
			if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "run timed out after 200ms") || time.Since(start) > 2*time.Second {
				t.Fatalf("%s outlived run deadline: %v (%s)", stage, err, time.Since(start))
			}
			if stage != "catalog" && calls.Load() != 0 {
				t.Fatal("model called despite startup cancellation")
			}
		})
	}
}

func TestRunCredentialHelper(t *testing.T) {
	if os.Getenv("K_BRAIN_RUN_CREDENTIAL_HELPER") != "1" {
		return
	}
	time.Sleep(10 * time.Second)
	os.Exit(0)
}

func TestRunSaveFailureEmitsErrorNotDone(t *testing.T) {
	runFixture(t, "unused", nil)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	home, _ := configDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths, err := filepath.Glob(filepath.Join(home, "sessions", "*", "*", "session.jsonl"))
		if err != nil || len(paths) != 1 {
			t.Errorf("expected created session: %v, %v", paths, err)
		} else if err := os.WriteFile(paths[0], []byte("invalid transcript\n"), 0600); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	p := cfg.Providers["testprov"]
	p.BaseURL = srv.URL + "/v1"
	cfg.Providers["testprov"] = p
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	out, err := runCapture(t, "", "--quiet", "--format", "json", "hello")
	if err == nil || !strings.Contains(err.Error(), "session save") {
		t.Fatalf("persistence failure hidden: %v", err)
	}
	seen := false
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		var event map[string]string
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event["type"] == "done" {
			t.Fatal("reported successful completion after save failure")
		}
		if event["type"] == "error" {
			seen = strings.Contains(event["error"], "session save")
		}
	}
	if !seen {
		t.Fatalf("no persistence error event: %s", out)
	}
}
