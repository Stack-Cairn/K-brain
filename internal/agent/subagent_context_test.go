package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestSubagentInheritsExecutionContext(t *testing.T) {
	parent := New(ai.New("http://unused", "k"), "m", 100, "sys")
	parent.WorkingDir = t.TempDir()
	parent.ResolveModel = func(string, string) (SubModel, error) { return SubModel{}, nil }
	parent.TaskDefault = SubModel{Model: "fallback", Effort: "high"}
	parent.SetMCPTools([]tools.Tool{{Def: ai.NewTool("mcp_test", "test", `{"type":"object"}`)}})
	parent.SetPluginTools([]tools.Tool{{Def: ai.NewTool("plugin_test", "test", `{"type":"object"}`)}})

	sub := parent.newSub(SubModel{})
	if sub.WorkingDir != parent.WorkingDir {
		t.Fatalf("working directory = %q, want %q", sub.WorkingDir, parent.WorkingDir)
	}
	if !strings.Contains(sub.Messages[0].Content, parent.WorkingDir) {
		t.Fatalf("subagent prompt does not identify its working directory: %q", sub.Messages[0].Content)
	}
	if len(sub.mcpTools) != 1 || len(sub.pluginTools) != 1 {
		t.Fatalf("runtime tools were not inherited: mcp=%d plugin=%d", len(sub.mcpTools), len(sub.pluginTools))
	}
	if sub.ResolveModel == nil || sub.TaskDefault.Model != "fallback" {
		t.Fatal("routing configuration was not inherited")
	}
}

func TestSubagentInheritedToolsExecute(t *testing.T) {
	for _, kind := range []string{"custom", "mcp", "plugin"} {
		t.Run(kind, func(t *testing.T) {
			srv := loopServer(t)
			defer srv.Close()
			parent := New(ai.New(srv.URL, "k"), "m", 100, "sys")
			parent.WorkingDir = t.TempDir()
			tool := echoTool()
			run := tool.Run
			tool.Run = func(ctx context.Context, args json.RawMessage) (string, error) {
				if tools.WorkingDir(ctx) != parent.WorkingDir {
					t.Errorf("tool working directory = %q, want %q", tools.WorkingDir(ctx), parent.WorkingDir)
				}
				return run(ctx, args)
			}
			switch kind {
			case "custom":
				parent.Tools = append(parent.Tools, tool)
			case "mcp":
				parent.SetMCPTools([]tools.Tool{tool})
			case "plugin":
				parent.SetPluginTools([]tools.Tool{tool})
			}
			sub := parent.newSub(SubModel{})
			for _, tool := range sub.AllTools() {
				if tool.Def.Function.Name == "subagent" || tool.Def.Function.Name == "question" {
					t.Fatal("subagent inherited parent orchestration tools")
				}
			}
			out, err := sub.Turn(context.Background(), "go", Events{})
			if err != nil || out != "done" {
				t.Fatalf("inherited tool execution: %q, %v", out, err)
			}
		})
	}
}

func TestSubagentWorktreeContext(t *testing.T) {
	parent := New(ai.New("http://unused", "k"), "m", 100, "sys")
	parent.WorkingDir = t.TempDir()
	parent.SandboxPolicy = sandbox.New("strict", "auto", parent.WorkingDir, false, []string{"cache"}, nil)
	sub := parent.newSub(SubModel{})
	dir := t.TempDir()
	sub.setWorktree(dir)
	if sub.WorkingDir != dir || !strings.Contains(sub.Messages[0].Content, dir) {
		t.Fatal("worktree not reflected in execution context and system prompt")
	}
	if sub.SandboxPolicy.Root != dir || parent.SandboxPolicy.Root != parent.WorkingDir {
		t.Fatal("worktree sandbox root not isolated from parent")
	}
	sub.SandboxPolicy.Writable[0] = "changed"
	if parent.SandboxPolicy.Writable[0] != "cache" {
		t.Fatal("worktree policy shares writable slice with parent")
	}
	if sub.files != parent.files {
		t.Fatal("subagents must coordinate shared file mutations")
	}
}

func TestSubagentDisabledToolsAndPlanMode(t *testing.T) {
	parent := New(ai.New("http://unused", "k"), "m", 100, "sys")
	parent.BrowserDisabled, parent.ComputerDisabled = true, true
	sub := parent.newSub(SubModel{})
	for _, tool := range sub.AllTools() {
		if strings.Contains(tool.Def.Function.Name, "browser") || strings.Contains(tool.Def.Function.Name, "computer") {
			t.Fatalf("disabled tool inherited: %s", tool.Def.Function.Name)
		}
	}
	parent.SetPlanMode(true)
	for _, tool := range sub.AllTools() {
		if !planToolAllowed(tool.Def.Function.Name) {
			t.Fatalf("plan mode exposed %s", tool.Def.Function.Name)
		}
	}
}

func TestBackgroundParentCancellationSettles(t *testing.T) {
	parent := New(ai.New("http://unused", "k"), "m", 100, "sys")
	ctx, cancel := context.WithCancel(context.Background())
	task := parent.RegisterBackgroundContext(ctx, "cancel me", "work", SubModel{})
	cancel()
	parent.LaunchBackground(task, "")
	select {
	case <-task.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled background task did not settle")
	}
	snap, _ := parent.Tasks().Get(task.ID)
	if snap.Status != TaskCancelled {
		t.Fatalf("status = %s", snap.Status)
	}
}

func TestSubagentEmptyPromptAndWorktreeFailure(t *testing.T) {
	parent := New(ai.New("http://unused", "k"), "m", 100, "sys")
	parent.WorkingDir = t.TempDir()
	for _, args := range []string{`{}`, `{"prompt":"  "}`, `{"prompt":"go","worktree":true,"background":true}`} {
		if _, err := taskTool(parent).Run(context.Background(), json.RawMessage(args)); err == nil {
			t.Fatalf("expected error for %s", args)
		}
	}
	tasks := parent.Tasks().List()
	if len(tasks) != 1 || tasks[0].Status != TaskError {
		t.Fatalf("worktree provisioning failure left a running task: %+v", tasks)
	}
	select {
	case <-tasks[0].Done:
	default:
		t.Fatal("failed task completion not signalled")
	}
}

func TestSubagentReportPreservesUTF8(t *testing.T) {
	report := capReport(strings.Repeat("氪脑", subagentReportCap))
	if !utf8.ValidString(report) || !strings.Contains(report, "report truncated") {
		t.Fatal("truncation produced invalid UTF-8")
	}
}

func TestSubagentWorktreesAreDistinct(t *testing.T) {
	repo := newWorktreeTestRepo(t)
	var previous string
	for _, mode := range []string{"foreground", "foreground", "background", "background", "direct"} {
		background := mode != "foreground"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req ai.Request
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			for _, msg := range req.Messages {
				if msg.Role == "tool" {
					if msg.Content != "echoed: hi" {
						t.Errorf("unexpected tool output: %s", msg.Content)
					}
					fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
					return
				}
			}
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"t1","type":"function","function":{"name":"echo","arguments":"{\"s\":\"hi\"}"}}]}}]}`+"\n\ndata: [DONE]\n\n")
		}))
		defer srv.Close()
		parent := New(ai.New(srv.URL, "k"), "m", 100, "sys")
		parent.WorkingDir = repo
		tool := echoTool()
		run := tool.Run
		tool.Run = func(ctx context.Context, args json.RawMessage) (string, error) {
			out := tools.Execute(ctx, tools.All(), "write", json.RawMessage(`{"path":"marker.txt","content":"isolated"}`))
			if strings.HasPrefix(out, "Error:") {
				t.Error(out)
			}
			return run(ctx, args)
		}
		parent.Tools = append(parent.Tools, tool)
		args, _ := json.Marshal(map[string]any{"prompt": "go", "worktree": true, "background": background})
		var out string
		var err error
		if mode == "direct" {
			parent.WorktreeSubagents = true
			parent.StartBackground("direct task", "go", SubModel{})
		} else {
			out, err = taskTool(parent).Run(context.Background(), args)
		}
		if err != nil {
			t.Fatal(err)
		}
		_, dir, ok := strings.Cut(out, "\n\nWorktree: ")
		if background {
			tasks := parent.Tasks().List()
			if len(tasks) != 1 {
				t.Fatalf("background tasks = %d", len(tasks))
			}
			select {
			case <-tasks[0].Done:
			case <-time.After(5 * time.Second):
				t.Fatal("background worktree task did not finish")
			}
			snap, _ := parent.Tasks().Get(tasks[0].ID)
			if snap.Status != TaskDone {
				t.Fatalf("background worktree task = %s: %s", snap.Status, snap.Report)
			}
			dir, ok = tasks[0].sub.WorkingDir, true
			if !strings.Contains(snap.Report, "Worktree: "+dir) {
				t.Fatal("background report lost the worktree location")
			}
		}
		if !ok || dir == previous {
			t.Fatalf("missing or reused worktree: %q", out)
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			t.Fatal(err)
		}
		if data, err := os.ReadFile(filepath.Join(dir, "marker.txt")); err != nil || string(data) != "isolated" {
			t.Fatalf("worktree file: %q, %v", data, err)
		}
		if _, err := os.Stat(filepath.Join(repo, "marker.txt")); !os.IsNotExist(err) {
			t.Fatalf("subagent wrote outside its worktree: %v", err)
		}
		previous = dir
	}
}

func TestBackgroundRegistrationInheritsCancellation(t *testing.T) {
	parent := New(ai.New("http://unused", "k"), "m", 100, "sys")
	ctx, cancel := context.WithCancel(context.Background())
	task := parent.RegisterBackgroundContext(ctx, "cancel me", "work", SubModel{})
	cancel()
	select {
	case <-task.ctx.Done():
	default:
		t.Fatal("background task did not inherit parent cancellation")
	}
	if task.Status != TaskRunning {
		t.Fatalf("registration should not settle before launch: %s", task.Status)
	}
}
