package agent

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// ReminderTag wraps context the harness, not the tool, adds to a tool result.
const ReminderTag = "system-reminder"

// ToolOutcome is what a Reminder sees about a finished tool call.
type ToolOutcome struct {
	ID   string
	Name string
	Args string
	// Result is the tool's result after the PostToolUse hook.
	Result tools.Result
	// MutatedPaths lists, in order and without duplicates, the resolved paths the call's
	// file tools reported mutating (write, edit, LiveAgent Write/Edit/Delete).
	MutatedPaths []string
}

// Reminder runs after every tool call and may return context for the model. The text
// is wrapped in a <system-reminder> block and appended to the tool result; "" adds
// nothing. Reminders run on the tool call's goroutine, so they must be safe for
// concurrent use and should return promptly.
type Reminder interface {
	Remind(ctx context.Context, call ToolOutcome) string
}

// ReminderFunc adapts a function to Reminder.
type ReminderFunc func(ctx context.Context, call ToolOutcome) string

func (f ReminderFunc) Remind(ctx context.Context, call ToolOutcome) string { return f(ctx, call) }

// DefaultReminders are installed on every new agent.
func DefaultReminders() []Reminder {
	return []Reminder{LSPDiagnosticsReminder{}}
}

// WrapReminder wraps text in a <system-reminder> block.
func WrapReminder(text string) string {
	return "<" + ReminderTag + ">\n" + text + "\n</" + ReminderTag + ">"
}

// appendReminders runs reminders in order and appends each non-empty one, wrapped,
// to the tool output.
func appendReminders(ctx context.Context, reminders []Reminder, out string, call ToolOutcome) string {
	for _, r := range reminders {
		if r == nil || ctx.Err() != nil {
			continue
		}
		text := strings.TrimSpace(r.Remind(ctx, call))
		if text == "" {
			continue
		}
		if out != "" {
			out += "\n\n"
		}
		out += WrapReminder(text)
	}
	return out
}

// mutationSet collects the paths one tool call mutates.
type mutationSet struct {
	mu    sync.Mutex
	paths []string
	seen  map[string]bool
}

func (m *mutationSet) record(path string) {
	if path == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen == nil {
		m.seen = map[string]bool{}
	}
	if !m.seen[path] {
		m.seen[path] = true
		m.paths = append(m.paths, path)
	}
}

func (m *mutationSet) snapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.paths...)
}

// maxDiagnosedFiles bounds the files one call waits on diagnostics for; a directory
// Delete or a bulk rewrite must not stall the turn on the language server.
const maxDiagnosedFiles = 8

// LSPDiagnosticsReminder reports language-server diagnostics for the files a
// successful tool call mutated, using the process-wide tools.LSP manager.
type LSPDiagnosticsReminder struct{}

func (LSPDiagnosticsReminder) Remind(ctx context.Context, call ToolOutcome) string {
	lsp := tools.LSP
	if lsp == nil || call.Result.Failed || len(call.MutatedPaths) == 0 {
		return ""
	}
	var blocks []string
	waited := 0
	for _, path := range call.MutatedPaths {
		if waited == maxDiagnosedFiles || ctx.Err() != nil {
			break
		}
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			continue
		}
		waited++
		if block := strings.TrimSpace(lsp.WaitDiagnostics(ctx, path)); block != "" {
			blocks = append(blocks, block)
		}
	}
	return strings.Join(blocks, "\n\n")
}
