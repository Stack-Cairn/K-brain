package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func toolNames(ts []tools.Tool) []string {
	names := make([]string, len(ts))
	for i, tool := range ts {
		names[i] = tool.Def.Function.Name
	}
	return names
}

func TestSubagentPreservesToolSubset(t *testing.T) {
	for _, names := range [][]string{nil, {"read"}, {"echo"}, {"write", "read"}} {
		t.Run(strings.Join(names, "+"), func(t *testing.T) {
			parent := New(ai.New("http://unused", "k"), "m", 100, "sys")
			original := parent.Tools
			parent.Tools = nil
			for _, name := range names {
				for _, tool := range append(original, echoTool()) {
					if tool.Def.Function.Name == name {
						parent.Tools = append(parent.Tools, tool)
					}
				}
			}
			sub := parent.newSub(SubModel{})
			if got, want := toolNames(sub.AllTools()), toolNames(parent.AllTools()); !reflect.DeepEqual(got, want) {
				t.Fatalf("subagent tools = %v, parent tools = %v", got, want)
			}
		})
	}
}

func TestSubagentPreservesBuiltinReplacement(t *testing.T) {
	parent := New(ai.New("http://unused", "k"), "m", 100, "sys")
	replacement := echoTool()
	replacement.Def.Function.Name = "read"
	replacement.Def.Function.Description = "Read through the project's custom adapter"
	for i, tool := range parent.Tools {
		if tool.Def.Function.Name == "read" {
			parent.Tools[i] = replacement
		}
	}
	sub := parent.newSub(SubModel{})
	got := tools.Execute(context.Background(), sub.AllTools(), "read", json.RawMessage(`{"s":"custom adapter"}`))
	if got != "echoed: custom adapter" {
		t.Fatalf("replacement tool was not inherited: %q", got)
	}
	if findTool(t, sub, "read").Def.Function.Description != replacement.Def.Function.Description {
		t.Fatal("replacement schema was not inherited")
	}
	sub.Tools[0] = echoTool()
	if parent.Tools[0].Def.Function.Name != "bash" {
		t.Fatal("child tool slice aliases parent")
	}
}

func TestSubagentExcludesParentSessionTools(t *testing.T) {
	parent := New(ai.New("http://unused", "k"), "m", 100, "sys", WithExperimental([]string{FeatureWorkflows}))
	sub := parent.newSub(SubModel{})
	for _, tool := range sub.AllTools() {
		switch tool.Def.Function.Name {
		case "question", "subagent", "subagent_steer", "workflow", "todowrite", "wait", "remember", "forget":
			t.Errorf("inherited parent session tool: %s", tool.Def.Function.Name)
		}
	}
}

func TestSubagentPlanModeRemainsLive(t *testing.T) {
	parent := New(ai.New("http://unused", "k"), "m", 100, "sys")
	parent.Tools = append(parent.Tools, echoTool())
	parent.SetPlanMode(true)
	sub := parent.newSub(SubModel{})
	if got := toolNames(sub.AllTools()); !reflect.DeepEqual(got, []string{"read"}) {
		t.Fatalf("plan tools = %v", got)
	}
	parent.SetPlanMode(false)
	active := sub.AllTools()
	parent.SetPlanMode(true)
	if got := tools.Execute(context.Background(), active, "echo", json.RawMessage(`{"s":"no"}`)); !strings.Contains(got, "Plan mode blocks") {
		t.Fatalf("captured tools ignored plan mode: %q", got)
	}
	parent.SetPlanMode(false)
	if got := tools.Execute(context.Background(), sub.AllTools(), "echo", json.RawMessage(`{"s":"yes"}`)); got != "echoed: yes" {
		t.Fatalf("tool was not restored after leaving plan mode: %q", got)
	}
}

func TestSubagentRuntimeToolInheritance(t *testing.T) {
	for _, source := range []string{"custom", "mcp", "plugin"} {
		t.Run(source, func(t *testing.T) {
			parent := New(ai.New("http://unused", "k"), "m", 100, "sys")
			parent.Tools = nil
			local := echoTool()
			local.Def.Function.Name = "parent_only"
			local.NoInherit = true
			input := []tools.Tool{echoTool(), local}
			switch source {
			case "custom":
				parent.Tools = input
			case "mcp":
				parent.SetMCPTools(input)
			case "plugin":
				parent.SetPluginTools(input)
			}
			sub := parent.newSub(SubModel{})
			if got := toolNames(sub.AllTools()); !reflect.DeepEqual(got, []string{"echo"}) {
				t.Fatalf("inherited tools = %v", got)
			}
			input[0] = local
			if got := tools.Execute(context.Background(), sub.AllTools(), "echo", json.RawMessage(`{"s":"snapshot"}`)); got != "echoed: snapshot" {
				t.Fatalf("child tool snapshot changed: %q", got)
			}
		})
	}
}

func TestSubagentPreservesToolResolutionOrder(t *testing.T) {
	parent := New(ai.New("http://unused", "k"), "m", 100, "sys")
	makeTool := func(result string) tools.Tool {
		tool := echoTool()
		tool.Run = func(context.Context, json.RawMessage) (string, error) { return result, nil }
		return tool
	}
	parent.Tools = []tools.Tool{makeTool("custom")}
	parent.SetMCPTools([]tools.Tool{makeTool("mcp")})
	parent.SetPluginTools([]tools.Tool{makeTool("plugin")})
	for _, want := range []string{"custom", "mcp", "plugin"} {
		sub := parent.newSub(SubModel{})
		for _, agent := range []*Agent{parent, sub} {
			if got := tools.Execute(context.Background(), agent.AllTools(), "echo", json.RawMessage(`{}`)); got != want {
				t.Fatalf("resolved tool = %q, want %q", got, want)
			}
		}
		if want == "custom" {
			parent.Tools = nil
		} else {
			parent.SetMCPTools(nil)
		}
	}
}

func TestSubagentRestrictedToolTurn(t *testing.T) {
	for _, name := range []string{"read", "write"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req ai.Request
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if len(req.Tools) != 1 || req.Tools[0].Function.Name != "read" {
					t.Errorf("unexpected advertised tool set: %+v", req.Tools)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				last := req.Messages[len(req.Messages)-1]
				if last.Role == "tool" {
					if name == "read" && last.Content != "echoed: adapted" {
						t.Errorf("custom read result = %q", last.Content)
					}
					if name == "write" && !strings.Contains(last.Content, `unknown tool "write"`) {
						t.Errorf("removed write result = %q", last.Content)
					}
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					return
				}
				args, _ := json.Marshal(`{"s":"adapted","path":"must-not-exist.txt","content":"unexpected"}`)
				fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"t1","type":"function","function":{"name":%q,"arguments":%s}}]}}]}`+"\n\ndata: [DONE]\n\n", name, args)
			}))
			defer srv.Close()
			parent := New(ai.New(srv.URL, "k"), "m", 100, "sys")
			parent.WorkingDir = dir
			read := echoTool()
			read.Def.Function.Name = "read"
			parent.Tools = []tools.Tool{read}
			sub := parent.newSub(SubModel{})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if out, err := sub.Turn(ctx, "go", Events{}); err != nil || out != "done" {
				t.Fatalf("turn = %q, %v", out, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "must-not-exist.txt")); !os.IsNotExist(err) {
				t.Fatalf("removed write tool created a file: %v", err)
			}
		})
	}
}
