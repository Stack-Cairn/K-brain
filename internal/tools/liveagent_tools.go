package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

type liveagentSnapshot struct {
	Hash    [32]byte
	ModTime time.Time
	Full    bool
}
type liveagentState struct {
	lock      chan struct{}
	snapshots map[string]liveagentSnapshot
	readViews map[string][32]byte
}

// LiveAgentCatalog creates session-local tools without changing CLI defaults.
func LiveAgentCatalog(provider ...string) []Tool {
	return LiveAgentCatalogWithManagers(provider, NewTerminalManager(), NewManagedProcessManager())
}

func LiveAgentCatalogWithManagers(provider []string, terminalManager *TerminalManager, processManager *ManagedProcessManager) []Tool {
	if terminalManager == nil {
		terminalManager = NewTerminalManager()
	}
	if processManager == nil {
		processManager = NewManagedProcessManager()
	}
	bashDefault, bashCap := 120000, 600000
	if len(provider) > 0 {
		switch strings.ToLower(provider[0]) {
		case "codex", "gemini", "xai", "deepseek":
			bashDefault, bashCap = 30000, 30000
		}
	}
	s := &liveagentState{lock: make(chan struct{}, 1), snapshots: map[string]liveagentSnapshot{}, readViews: map[string][32]byte{}}
	var out []Tool
	out = append(out, terminalTool("TerminalSession", terminalManager), terminalTool("ReadTerminal", terminalManager))
	for _, name := range []string{"Read", "Image", "Write", "Edit", "Delete", "List", "Glob", "Grep", "Bash", "ManagedProcess", "ProcessWait", "ProcessStop"} {
		def := liveagentDefinitions[name]
		if name == "Bash" {
			def = ai.NewTool(name, "Execute a non-interactive shell command for builds, tests, package managers or external CLIs. Use dedicated file tools for workspace operations. cwd is relative to the workspace; timeout_ms is milliseconds (default 120000, maximum 600000). Commands remain backend-owned and obey permission and sandbox policy.", `{"type":"object","additionalProperties":false,"properties":{"command":{"type":"string","description":"Shell command to execute (prefer non-interactive, idempotent commands)."},"cwd":{"type":"string","description":"Optional working directory. Omit to use the workspace root."},"timeout_ms":{"type":"number","minimum":1000,"maximum":600000,"description":"Timeout in milliseconds (default: 120000, maximum: 600000)."},"yield_time_ms":{"type":"number","minimum":1,"maximum":300000,"description":"Return after this many milliseconds while the same Bash command continues; use ProcessWait with the returned session_id."}},"required":["command"]}`)
		}
		if name == "Bash" || name == "ManagedProcess" {
			def = liveagentShellDefinition(def)
		}
		if name == "Bash" {
			def.Function.Description = strings.ReplaceAll(def.Function.Description, "default 120000, maximum 600000", fmt.Sprintf("default %d, provider maximum %d", bashDefault, bashCap))
			def.Function.Parameters = json.RawMessage(strings.ReplaceAll(string(def.Function.Parameters), "default: 120000, maximum 600000", fmt.Sprintf("default: %d, provider cap: %d; larger values are clamped", bashDefault, bashCap)))
		}
		toolName, toolDef := name, def
		out = append(out, Tool{Def: toolDef, Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			a, err := liveagentArguments(toolDef, raw)
			if err != nil {
				return "", err
			}
			if toolName == "ManagedProcess" {
				return liveagentManagedProcess(ctx, processManager, a)
			}
			if toolName == "ProcessWait" {
				return liveagentProcessWaitWithTool(ctx, processManager, a, toolName)
			}
			if toolName == "ProcessStop" {
				return liveagentProcessStopWithTool(ctx, processManager, a, toolName)
			}
			select {
			case s.lock <- struct{}{}:
				defer func() { <-s.lock }()
			case <-ctx.Done():
				return "", ctx.Err()
			}
			switch toolName {
			case "Read":
				return s.read(ctx, a)
			case "Image":
				return liveagentImage(ctx, a)
			case "Write", "Edit":
				return s.mutate(ctx, toolName, a)
			case "Delete":
				return s.delete(ctx, a)
			case "List", "Glob", "Grep":
				return liveagentSearch(ctx, toolName, a)
			case "Bash":
				return liveagentBash(ctx, processManager, a, bashDefault, bashCap)
			}

			return "", fmt.Errorf("unknown tool %s", toolName)
		}})
	}
	return out
}

func liveagentArguments(def ai.Tool, raw json.RawMessage) (map[string]any, error) {
	var a map[string]any
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if a == nil {
		return nil, errors.New("tool arguments must be an object")
	}
	if def.Function.Name == "Write" {
		delete(a, "mode")
	}
	var schema map[string]any
	if err := json.Unmarshal(def.Function.Parameters, &schema); err != nil {
		return nil, err
	}
	if err := liveagentValidate(a, schema, def.Function.Name); err != nil {
		return nil, err
	}
	return a, nil
}
func liveagentValidate(value any, schema map[string]any, label string) error {
	if branches, ok := schema["anyOf"].([]any); ok {
		for _, branch := range branches {
			if liveagentValidate(value, branch.(map[string]any), label) == nil {
				return nil
			}
		}
		return fmt.Errorf("%s is not one of the allowed values", label)
	}
	if want, ok := schema["const"]; ok && value != want {
		return fmt.Errorf("%s must be %v", label, want)
	}
	switch schema["type"] {
	case "object":
		a, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", label)
		}
		props, _ := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, key := range required {
				if _, ok := a[key.(string)]; !ok {
					return fmt.Errorf("%s.%s is required", label, key)
				}
			}
		}
		for key, v := range a {
			p, ok := props[key]
			if !ok {
				return fmt.Errorf("%s received unsupported argument %q", label, key)
			}
			if err := liveagentValidate(v, p.(map[string]any), label+"."+key); err != nil {
				return err
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be a string", label)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", label)
		}
	case "number":
		n, ok := value.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
			return fmt.Errorf("%s must be a finite number", label)
		}
		if lo, ok := schema["minimum"].(float64); ok && n < lo {
			return fmt.Errorf("%s must be at least %v", label, lo)
		}
		if hi, ok := schema["maximum"].(float64); ok && n > hi {
			return fmt.Errorf("%s must be at most %v", label, hi)
		}
	case "array":
		a, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", label)
		}
		if lo, ok := schema["minItems"].(float64); ok && len(a) < int(lo) {
			return fmt.Errorf("%s requires at least %v items", label, lo)
		}
		if hi, ok := schema["maxItems"].(float64); ok && len(a) > int(hi) {
			return fmt.Errorf("%s permits at most %v items", label, hi)
		}
		for _, v := range a {
			if err := liveagentValidate(v, schema["items"].(map[string]any), label); err != nil {
				return err
			}
		}
	}
	return nil
}
func laString(a map[string]any, k string) string { v, _ := a[k].(string); return v }
func laInt(a map[string]any, k string, fallback int) int {
	if v, ok := a[k].(float64); ok {
		return int(min(v, float64(math.MaxInt32)))
	}
	return fallback
}
func laBool(a map[string]any, k string, fallback bool) bool {
	if v, ok := a[k].(bool); ok {
		return v
	}
	return fallback
}

func liveagentAuthorizePath(ctx context.Context, name, raw, access string, directory bool) (string, error) {
	path, err := liveagentPath(ctx, raw, access, name, directory)
	if err != nil {
		return "", err
	}
	if deny := checkGate(ctx, name, path); deny != "" {
		return "", errors.New(deny)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return liveagentPath(ctx, path, access, name, directory)
}
func (s *liveagentState) baseline(path string, data []byte, info fs.FileInfo) error {
	snap, ok := s.snapshots[path]
	if ok && snap.Full {
		if snap.Hash != sha256.Sum256(data) || !snap.ModTime.Equal(info.ModTime()) {
			return errors.New("file changed since the last Read; Read it again before retrying")
		}
		return nil
	}
	if len(strings.Split(string(data), "\n")) > 5000 {
		return errors.New("requires a full-file Read first: file exceeds the automatic full-read limit (5000 lines)")
	}
	if IsBinary(data) {
		return errors.New("requires a full text Read; binary files cannot be edited")
	}
	return nil
}
func (s *liveagentState) mutate(ctx context.Context, name string, a map[string]any) (string, error) {
	path, err := liveagentAuthorizePath(ctx, name, laString(a, "path"), "write", false)
	if err != nil {
		return "", err
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil && (!errors.Is(readErr, os.ErrNotExist) || name == "Edit") {
		return "", readErr
	}
	if readErr == nil {
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if err := s.baseline(path, data, info); err != nil {
			return "", err
		}
	}
	var run Tool
	translated := map[string]any{"path": path}
	strategy := "exact"
	if name == "Write" {
		run = writeTool()
		translated["content"] = laString(a, "content")
	} else {
		old, newText := laString(a, "old_string"), laString(a, "new_string")
		if old == "" {
			return "", errors.New("Edit.old_string must be a non-empty string")
		}
		actual, replacement, matchStrategy, err := liveagentEditMatch(string(data), old, newText)
		if err != nil {
			return "", err
		}
		strategy = matchStrategy
		n := strings.Count(string(data), actual)
		if n > 1 && !laBool(a, "replace_all", false) {
			return "", fmt.Errorf("old_string appears %d times; make it unique or set replace_all", n)
		}
		if expected := laInt(a, "expected_replacements", 0); expected > 0 && expected != n {
			return "", fmt.Errorf("expected %d replacements, found %d", expected, n)
		}
		run = editTool()
		translated["old_string"] = actual
		translated["new_string"] = replacement
		translated["replace_all"] = laBool(a, "replace_all", false)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := liveagentPath(ctx, path, "write", name, false); err != nil {
		return "", err
	}
	// Authorization was completed above; delegation retains mutation and LSP hooks.
	delegated := WithGate(ctx, func(GateRequest) (GateDecision, string) { return GateAllowOnce, "" })
	raw, _ := json.Marshal(translated)
	out, err := run.Run(delegated, raw)
	if err == nil {
		current, e := os.ReadFile(path)
		info, e2 := os.Stat(path)
		if e == nil && e2 == nil {
			s.snapshots[path] = liveagentSnapshot{sha256.Sum256(current), info.ModTime(), true}
		}
	}
	if name == "Edit" && strategy != "exact" {
		out += "\nmatchStrategy=" + strategy
	}
	return out, err
}
func (s *liveagentState) delete(ctx context.Context, a map[string]any) (string, error) {
	path, err := liveagentAuthorizePath(ctx, "Delete", laString(a, "path"), "write", true)
	if err != nil {
		return "", err
	}
	roots := append([]WorkspaceRoot(nil), workspaceRoots(ctx)...)
	named, _ := ctx.Value(liveagentNamedRootsKey{}).(map[string]WorkspaceRoot)
	for _, root := range named {
		roots = append(roots, root)
	}
	for _, root := range append(roots, WorkspaceRoot{Path: WorkingDir(ctx)}) {
		if filepath.Clean(root.Path) == path {
			return "", errors.New("Delete cannot remove a workspace root")
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	var paths []string
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("Delete refuses symbolic links")
		}
		if _, err := liveagentPath(ctx, p, "write", "Delete", true); err != nil {
			return err
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return "", err
	}
	for _, p := range paths {
		BeforeFileMutation(ctx, p)
	}
	for i := len(paths) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if _, err := liveagentPath(ctx, paths[i], "write", "Delete", true); err != nil {
			return "", err
		}
		if err := os.Remove(paths[i]); err != nil {
			return "", err
		}
		delete(s.snapshots, paths[i])
	}
	kind := "file"
	if info.IsDir() {
		kind = "dir"
	}
	return fmt.Sprintf("Delete: %s\nkind=%s", path, kind), nil
}
func liveagentBash(ctx context.Context, manager *liveagentProcessManager, a map[string]any, defaultTimeout, maxTimeout int) (string, error) {
	command := laString(a, "command")
	if strings.TrimSpace(command) == "" {
		return "", errors.New("Bash.command is required")
	}
	ctx = bashrun.WithShell(ctx, bashrun.ResolveShell(ctx, laString(a, "shell")))
	if err := liveagentValidateShellBackground(ctx, command, false); err != nil {
		return "", err
	}
	raw := laString(a, "cwd")
	if raw == "" {
		raw = WorkingDir(ctx)
	}
	dir, err := liveagentPath(ctx, raw, "write", "Bash", true)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("Bash.cwd must be a directory")
	}
	if deny := checkShellGate(ctx, "Bash", command, liveagentShell(ctx, "")); deny != "" {
		return "", errors.New(deny)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	timeout := time.Duration(min(laInt(a, "timeout_ms", defaultTimeout), maxTimeout)) * time.Millisecond
	if yield := laInt(a, "yield_time_ms", 0); yield > 0 {
		p, startErr := manager.startBashSession(ctx, command, dir, timeout)
		if startErr != nil {
			return "", startErr
		}
		reportProcessStarted(ctx, p.id)
		waitArgs := map[string]any{"process_id": p.id, "cursor": float64(0), "yield_time_ms": float64(yield), "max_bytes": float64(liveagentProcessMaxLog)}
		out, waitErr := liveagentProcessWait(ctx, manager, waitArgs)
		if waitErr != nil {
			return "", waitErr
		}
		return fmt.Sprintf("Bash session_id=%s\n%s", p.id, out), nil
	}
	update, _ := ctx.Value(updateKey{}).(func(string))
	res := bashrun.Run(ctx, bashrun.Options{Command: command, Dir: dir, Timeout: timeout, OnUpdate: update})
	out := TruncateTail(res.Output)
	if res.OutputFile != "" {
		out += fmt.Sprintf("\n[full output (%d bytes): %s]", res.OutputBytes, res.OutputFile)
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if res.TimedOut {
		return out, errors.New("Bash command timed out")
	}
	if res.Exit != "" {
		return out, errors.New(res.Exit)
	}
	return out, nil
}
