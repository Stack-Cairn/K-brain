package tui

import (
	"sort"
	"strings"
)

type registryEntry struct {
	Name     string
	Hint     string
	Keybind  string
	Category string
}

var registry = []registryEntry{
	{Name: "/copy", Hint: "[N] [file] — copy the Nth latest reply or save it to a file", Category: "Session"},
	{Name: "/diff", Hint: "[--staged|--stat] — inspect tracked Git changes locally", Category: "Session"},
	{Name: "/prompts", Hint: "[refresh] — list or reload Markdown prompt templates", Category: "Agent"},
	{Name: "!!cmd", Hint: "— run a local shell command without sending output to the model", Category: "App"},
	{Name: "/cd", Hint: "[dir] — change working directory (bare prints it)", Category: "Session"},
	{Name: "/clear", Hint: "— reset conversation", Category: "Session"},
	{Name: "/compact", Hint: "[model]|off|retry|log — compact the conversation now", Category: "Session"},
	{Name: "/computer-use", Hint: "[task] — drive this Mac; allow|deny <app>", Category: "Agent"},
	{Name: "/context", Hint: "— show context sources and token estimates", Category: "Session"},
	{Name: "/context-doctor", Hint: "— audit fresh-session injections and their token cost", Category: "Session"},
	{Name: "/doctor", Hint: "— inspect terminal and session diagnostics", Category: "App"},
	{Name: "/effort", Hint: "[level] — reasoning effort: off·low·medium·high", Category: "Agent"},
	{Name: "/export", Hint: "[path] — write the transcript to a markdown file", Category: "Session"},
	{Name: "/fork", Hint: "[name] — copy the conversation into a new session", Category: "Session"},
	{Name: "/goal", Hint: "<text> — keep working until the goal is met (resume | clear)", Category: "Session"},
	{Name: "/goal-from-context", Hint: "[n] — form a goal from recent messages and pursue it", Category: "Session"},
	{Name: "/help", Hint: "— show all commands and keybindings", Category: "App"},
	{Name: "/info", Hint: "— show session details (alias: /session-info)", Category: "Session"},
	{Name: "/mcp", Hint: "[name] [reconnect|enable|disable] — MCP server status", Category: "Session"},
	{Name: "/mcps", Hint: "[name] [reconnect|enable|disable] — MCP server status", Category: "Session"},
	{Name: "/me", Hint: "— edit your standing instructions in $EDITOR", Category: "Agent"},
	{Name: "/memory", Hint: "[n] — list saved memories; mark entry n done", Category: "Session"},
	{Name: "/model", Hint: "<name> [provider] — switch model (refresh pulls the catalog)", Category: "Agent"},
	{Name: "/model-for-session", Hint: "<name> — switch model for this session only", Category: "Agent"},
	{Name: "/mouse", Hint: "— toggle mouse capture", Category: "Display"},
	{Name: "/new", Hint: "— start a fresh session (alias: /clear)", Category: "Session"},
	{Name: "/plan", Hint: "— show the current model-managed plan", Category: "Session"},
	{Name: "/pwd", Hint: "— print working directory", Category: "Session"},
	{Name: "/quit", Hint: "— exit", Keybind: "ctrl+c ctrl+c", Category: "App"},
	{Name: "/rename", Hint: "[title] — retitle this session", Category: "Session"},
	{Name: "/report", Hint: "— bug report: issue link + environment snippet", Category: "App"},
	{Name: "/rewind", Hint: "— browse turns and rewind the conversation (f forks)", Category: "Session"},
	{Name: "/resume", Hint: "[id] — resume a previous session", Category: "Session"},
	{Name: "/schedule", Hint: "@every 10m|@at <time> <prompt> — schedule a wakeup; list | cancel", Category: "Session"},
	{Name: "/session-info", Hint: "— show session details (alias: /status)", Category: "Session"},
	{Name: "/subagent", Hint: "[-m model] <prompt> — spawn a background subagent", Category: "Session"},
	{Name: "/subagents", Hint: "[id] — subagent dock / live view (alias /tasks)", Keybind: "ctrl+t", Category: "Session"},
	{Name: "/status", Hint: "— show session details (alias: /session-info)", Category: "Session"},
	{Name: "/theme", Hint: "[light|dark|auto] — color scheme", Category: "Display"},
	{Name: "/title", Hint: "[title] — retitle this session (alias: /rename)", Category: "Session"},
	{Name: "/undo", Hint: "— rewind the conversation to an earlier turn", Category: "Session"},
	{Name: "/view-plan", Hint: "— show the current model-managed plan (alias: /plan)", Category: "Session"},
	{Name: "!cmd", Hint: "— run a shell command; output joins the conversation", Category: "App"},
}

func sortEntries(es []registryEntry) {
	sort.Slice(es, func(i, j int) bool { return es[i].Name < es[j].Name })
}

func slashRegistry() []registryEntry {
	var out []registryEntry
	for _, e := range registry {
		if strings.HasPrefix(e.Name, "/") {
			out = append(out, e)
		}
	}
	sortEntries(out)
	return out
}

func registryFind(name string) *registryEntry {
	for i := range registry {
		if registry[i].Name == name {
			return &registry[i]
		}
	}
	return nil
}

func (m *model) dispatches(name string) bool {
	before := len(m.blocks)
	m.command(name)
	for _, b := range m.blocks[before:] {
		if strings.Contains(b.text, "unknown command") {
			return false
		}
	}
	return true
}

func helpText() string {
	var b strings.Builder
	for _, e := range slashRegistry() {
		b.WriteString(e.Name + " " + e.Hint + "\n")
	}
	b.WriteString(palHintRewind + " — " + palDescRewind + "\n")
	b.WriteString("!cmd " + registryFind("!cmd").Hint + "\n")
	b.WriteString("!!cmd " + registryFind("!!cmd").Hint + "\n")
	b.WriteString("tab — complete")
	for _, hint := range []string{
		"ctrl+k — clear the conversation",
		"ctrl+t — focus the subagents dock (↑/↓ select, enter opens, esc backs out)",
		palHintThinking + " — toggle thinking tokens",
		"ctrl+e — expand the last tool result",
		"ctrl+j / shift+enter — newline",
		"ctrl+v — paste image",
		"esc — interrupt the agent",
		"esc esc (idle) — " + palDescRewind + " (↑/↓ browse, enter rewinds, f forks)",
		"while busy with queued messages: ↑/↓ select, del removes",
		"PgUp/PgDn — scroll · wheel — scroll · drag — select/copy text",
		palHintQuit + " — quit",
	} {
		b.WriteString(" · " + hint)
	}
	return b.String()
}
