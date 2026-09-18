package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

type Tool struct {
	Def ai.Tool
	Run func(ctx context.Context, args json.RawMessage) (string, error)
}

type InteractiveRunner interface {
	Run(ctx context.Context, command string, timeout time.Duration, keys <-chan []byte) string
}

var InteractiveBash InteractiveRunner

var LSP interface {
	WaitDiagnostics(ctx context.Context, path string) string
}

func All() []Tool {
	return []Tool{bashTool(), readTool(), writeTool(), editTool()}
}

type updateKey struct{}

func WithOnUpdate(ctx context.Context, onUpdate func(outputSoFar string)) context.Context {
	return context.WithValue(ctx, updateKey{}, onUpdate)
}

func Defs(ts []Tool) []ai.Tool {
	defs := make([]ai.Tool, len(ts))
	for i, t := range ts {
		defs[i] = t.Def
	}
	return defs
}

var Suggester func(name string) []string

func Execute(ctx context.Context, ts []Tool, name string, args json.RawMessage) string {
	for _, t := range ts {
		if t.Def.Function.Name == name {
			out, err := t.Run(ctx, args)
			if err != nil {
				return "Error: " + err.Error()
			}
			if out == "" {
				out = "(no output)"
			}
			return out
		}
	}
	msg := fmt.Sprintf("Error: unknown tool %q", name)
	if Suggester != nil {
		if hints := Suggester(name); len(hints) > 0 {
			msg += " — did you mean " + strings.Join(hints, " or ") + "?"
		}
	}
	return msg
}

const maxOutput = 50_000

func Truncate(s string) string {
	return truncate(s)
}

func truncate(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	return middleElide(s)
}

func middleElide(s string) string {
	keep := maxOutput / 2
	head, tail := s[:keep], s[len(s)-keep:]
	elided := len(s) - 2*keep
	marker := fmt.Sprintf("\n... [%d bytes elided from the middle", elided)
	if path := bashrun.Spill(s); path != "" {
		marker += fmt.Sprintf(" — full output (%d bytes): %s", len(s), path)
	}
	marker += "] ...\n"
	return head + marker + tail
}

func lspDiagnostics(ctx context.Context, path string) string {
	if LSP == nil {
		return ""
	}
	return LSP.WaitDiagnostics(ctx, path)
}

func TruncateTail(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	return fmt.Sprintf("[... first %d bytes truncated]\n", len(s)-maxOutput) + s[len(s)-maxOutput:]
}

func bashTool() Tool {
	return Tool{
		Def: ai.NewTool("bash",
			"Execute a shell command in the current working directory and return combined stdout/stderr. Default shell: "+bashrun.DefaultShell()+". Use shell to select powershell, pwsh (PowerShell 7), bash, wsl or cmd; use syntax appropriate for that shell. Native Windows does not support interactive PTY.",
			`{"type":"object","properties":{"command":{"type":"string","description":"The command to execute in the selected shell"},"shell":{"type":"string","description":"Shell executable name or full path, without arguments; powershell, pwsh/powershell7, bash, wsl, cmd. Defaults to K_BRAIN_SHELL, then the platform default."},"timeout":{"type":"number","description":"Timeout in seconds (default 120)"},"interactive":{"type":"boolean","description":"Run in a PTY so sudo/ssh-style password prompts work. K-brain stays in control of the terminal and forwards your keystrokes; the command is killed after 15s of no input. Use only for commands that genuinely need a password."}},"required":["command"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Command     string  `json:"command"`
				Shell       string  `json:"shell"`
				Timeout     float64 `json:"timeout"`
				Interactive bool    `json:"interactive"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if a.Timeout <= 0 {
				a.Timeout = 120
			}
			if deny := checkGate(ctx, "bash", a.Command); deny != "" {
				return "", errors.New(deny)
			}
			if a.Interactive && runtime.GOOS == "windows" {
				return "", errors.New("interactive PTY is not available on native Windows; run non-interactively or run k-brain inside WSL")
			}
			ctx = bashrun.WithShell(ctx, a.Shell)
			dur := time.Duration(a.Timeout * float64(time.Second))

			if a.Interactive && InteractiveBash != nil {
				keys := make(chan []byte, 16)
				out := InteractiveBash.Run(ctx, a.Command, dur, keys)
				if isBinary([]byte(out)) {
					return binaryPlaceholder("", len(out)), nil
				}
				return TruncateTail(out), nil
			}

			var onUpdate func(string)
			if cb, ok := ctx.Value(updateKey{}).(func(string)); ok {
				onUpdate = cb
			}
			res := bashrun.Run(ctx, bashrun.Options{
				Command:  a.Command,
				Timeout:  dur,
				OnUpdate: onUpdate,
			})

			s := TruncateTail(res.Output)
			if isBinary([]byte(res.Output)) {

				s = binaryPlaceholder("", len(res.Output))
				if res.TimedOut {
					return s + "\n(command timed out)", nil
				}
				if res.Exit != "" {
					return fmt.Sprintf("%s\n(%s)", s, res.Exit), nil
				}
				return s, nil
			}
			if len(res.Output) > maxOutput {

				if path := bashrun.Spill(res.Output); path != "" {
					s += fmt.Sprintf("\n[full output (%d bytes): %s]", len(res.Output), path)
				}
			}
			if res.TimedOut {
				return s + "\n(command timed out)", nil
			}
			if res.Exit != "" {
				return fmt.Sprintf("%s\n(%s)", s, res.Exit), nil
			}
			if s == "" {
				return "(no output)", nil
			}
			return s, nil
		},
	}
}

func readTool() Tool {
	return Tool{
		Def: ai.NewTool("read",
			"Read a file and return its contents with line numbers.",
			`{"type":"object","properties":{"path":{"type":"string","description":"Path to the file"},"offset":{"type":"number","description":"1-based line to start from"},"limit":{"type":"number","description":"Max lines to return (default 2000)"}},"required":["path"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Path   string `json:"path"`
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			data, err := os.ReadFile(a.Path)
			if err != nil {
				return "", err
			}
			if isBinary(data) {
				return binaryPlaceholder(a.Path, len(data)), nil
			}
			lines := strings.Split(string(data), "\n")
			start := max(a.Offset-1, 0)
			if start >= len(lines) {
				return "", fmt.Errorf("offset %d past end of file (%d lines)", a.Offset, len(lines))
			}
			limit := a.Limit
			if limit <= 0 {
				limit = 2000
			}
			end := min(start+limit, len(lines))
			var b strings.Builder
			for i := start; i < end; i++ {
				fmt.Fprintf(&b, "%d\t%s\n", i+1, lines[i])
			}
			return truncate(b.String()), nil
		},
	}
}

func writeTool() Tool {
	return Tool{
		Def: ai.NewTool("write",
			"Write content to a file, creating it (and parent directories) or overwriting it.",
			`{"type":"object","properties":{"path":{"type":"string","description":"Path to the file"},"content":{"type":"string","description":"Full file content"}},"required":["path","content"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if deny := checkGate(ctx, "write", a.Path); deny != "" {
				return "", errors.New(deny)
			}

			old, oldErr := os.ReadFile(a.Path)

			if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
				return "", err
			}

			if err := os.WriteFile(a.Path, []byte(a.Content), 0o644); err != nil {
				return "", err
			}
			out := fmt.Sprintf("Wrote %d bytes to %s", len(a.Content), a.Path)

			if oldErr == nil {
				if d := editDiff(string(old), a.Content, 1); d != "" {
					out += "\n```diff\n" + d + "\n```"
				}
			}
			return out + lspDiagnostics(ctx, a.Path), nil
		},
	}
}

func editTool() Tool {
	return Tool{
		Def: ai.NewTool("edit",
			"Replace an exact string in a file. old_string must appear exactly once unless replace_all is true.",
			`{"type":"object","properties":{"path":{"type":"string","description":"Path to the file"},"old_string":{"type":"string","description":"Exact text to replace"},"new_string":{"type":"string","description":"Replacement text"},"replace_all":{"type":"boolean","description":"Replace every occurrence"}},"required":["path","old_string","new_string"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Path       string `json:"path"`
				OldString  string `json:"old_string"`
				NewString  string `json:"new_string"`
				ReplaceAll bool   `json:"replace_all"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if deny := checkGate(ctx, "edit", a.Path); deny != "" {
				return "", errors.New(deny)
			}
			data, err := os.ReadFile(a.Path)
			if err != nil {
				return "", err
			}
			s := string(data)
			n := strings.Count(s, a.OldString)
			switch {
			case n == 0:
				return "", fmt.Errorf("old_string not found in %s", a.Path)
			case n > 1 && !a.ReplaceAll:
				return "", fmt.Errorf("old_string appears %d times in %s; make it unique or set replace_all", n, a.Path)
			}
			s = strings.ReplaceAll(s, a.OldString, a.NewString)

			if err := os.WriteFile(a.Path, []byte(s), 0o644); err != nil {
				return "", err
			}
			out := fmt.Sprintf("Replaced %d occurrence(s) in %s", n, a.Path)

			startLine := 0
			if n == 1 {
				startLine = 1 + strings.Count(string(data)[:strings.Index(string(data), a.OldString)], "\n")
			}
			if d := editDiff(a.OldString, a.NewString, startLine); d != "" {
				out += "\n```diff\n" + d + "\n```"
			}
			return out + lspDiagnostics(ctx, a.Path), nil
		},
	}
}

func editDiff(oldS, newS string, startLine int) string {
	o := strings.Split(strings.TrimSuffix(oldS, "\n"), "\n")
	n := strings.Split(strings.TrimSuffix(newS, "\n"), "\n")
	p := 0
	for p < len(o) && p < len(n) && o[p] == n[p] {
		p++
	}
	s := 0
	for s < len(o)-p && s < len(n)-p && o[len(o)-1-s] == n[len(n)-1-s] {
		s++
	}
	if p == len(o) && p == len(n) {
		return ""
	}
	var b strings.Builder
	rows := 0
	row := func(num int, mark, line string) {
		rows++
		if rows > editDiffMaxLines {
			return
		}
		if len(line) > 200 {
			line = line[:200] + "…"
		}
		if startLine > 0 {
			fmt.Fprintf(&b, "%d %s %s\n", num, mark, line)
		} else {
			fmt.Fprintf(&b, "%s %s\n", mark, line)
		}
	}
	if p > 0 {
		row(startLine+p-1, " ", o[p-1])
	}
	for i, l := range o[p : len(o)-s] {
		row(startLine+p+i, "-", l)
	}
	for i, l := range n[p : len(n)-s] {
		row(startLine+p+i, "+", l)
	}
	if s > 0 {
		row(startLine+len(o)-1, " ", o[len(o)-1])
	}
	out := strings.TrimSuffix(b.String(), "\n")
	if rows > editDiffMaxLines {
		out += fmt.Sprintf("\n… +%d more lines", rows-editDiffMaxLines)
	}
	return out
}

const editDiffMaxLines = 200
