package tui

import (
	"sort"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/i18n"
)

type registryEntry struct {
	Name     string
	Args     string
	Hint     string
	Keybind  string
	Category string
}

var registry = []registryEntry{
	{Name: "/btw", Args: "<question>", Hint: "ask a side question without changing this session", Category: "Agent"},
	{Name: "/archive", Args: "[id]", Hint: "archive this session or a session id", Category: "Session"},
	{Name: "/brain", Hint: "edit persistent standing instructions in brain.md", Category: "Agent"},
	{Name: "/copy", Args: "[N] [file]", Hint: "copy the Nth latest reply or save it to a file", Category: "Session"},
	{Name: "/diff", Args: "[--staged|--stat]", Hint: "inspect tracked Git changes locally", Category: "Session"},
	{Name: "/prompts", Args: "[refresh]", Hint: "list or reload Markdown prompt templates", Category: "Agent"},
	{Name: "!!cmd", Hint: "run a local shell command without sending output to the model", Category: "App"},
	{Name: "/cd", Args: "[dir]", Hint: "change working directory (bare prints it)", Category: "Session"},
	{Name: "/clear", Hint: "reset conversation", Category: "Session"},
	{Name: "/compact", Args: "[model]|fresh|off|retry|log", Hint: "compact the conversation now", Category: "Session"},
	{Name: "/computer-use", Args: "[task]", Hint: "drive the desktop; allow|deny <app>", Category: "Agent"},
	{Name: "/context", Hint: "show context sources and token estimates", Category: "Session"},
	{Name: "/context-doctor", Hint: "audit fresh-session injections and their token cost", Category: "Session"},
	{Name: "/doctor", Hint: "inspect terminal and session diagnostics", Category: "App"},
	{Name: "/editor", Hint: "edit a prompt in $VISUAL / $EDITOR", Keybind: "ctrl+g", Category: "App"},
	{Name: "/effort", Args: "[level]", Hint: "reasoning effort: off·low·medium·high", Category: "Agent"},
	{Name: "/ancient", Args: "[on|off|toggle]", Hint: "toggle ancient vertical writing mode", Category: "Display"},
	{Name: "/export", Args: "[path]", Hint: "export transcript as markdown, JSONL, or HTML", Category: "Session"},
	{Name: "/fork", Args: "[name]", Hint: "copy the conversation into a new session", Category: "Session"},
	{Name: "/forks", Hint: "show the current session tree", Category: "Session"},
	{Name: "/privacy", Args: "[on|off|toggle|status]", Hint: "toggle the local privacy masking gateway", Category: "App"},
	{Name: "/import", Args: "<jsonl path>", Hint: "import messages from a JSONL transcript", Category: "Session"},
	{Name: "/goal", Args: "<text>", Hint: "keep working until the goal is met (resume | clear)", Category: "Session"},
	{Name: "/goal-from-context", Args: "[n]", Hint: "form a goal from recent messages and pursue it", Category: "Session"},
	{Name: "/language", Args: "[zh_cn|zh_Hant|en]", Hint: "choose the interface language", Category: "Display"},
	{Name: "/help", Hint: "show all commands and keybindings", Category: "App"},
	{Name: "/info", Hint: "show session details (alias: /session-info)", Category: "Session"},
	{Name: "/mcp", Args: "[name] [reconnect|enable|disable]", Hint: "MCP server status", Category: "Session"},
	{Name: "/mcps", Args: "[name] [reconnect|enable|disable]", Hint: "MCP server status", Category: "Session"},
	{Name: "/memory", Args: "[n]", Hint: "list saved memories; mark entry n done", Category: "Session"},
	{Name: "/model", Args: "<name> [provider]", Hint: "switch model (refresh pulls the catalog)", Category: "Agent"},
	{Name: "/model-for-session", Args: "<name>", Hint: "switch model for this session only", Category: "Agent"},
	{Name: "/mouse", Hint: "toggle mouse capture", Category: "Display"},
	{Name: "/new", Hint: "start a fresh session (alias: /clear)", Category: "Session"},
	{Name: "/new-context", Hint: "start a fresh context window and preserve raw history", Category: "Session"},
	{Name: "/permissions", Args: "[normal|plan|always]", Hint: "change execution mode", Category: "Agent"},
	{Name: "/plan", Hint: "show the current model-managed plan", Category: "Session"},
	{Name: "/plugins", Args: "[list|enable NAME|disable NAME|reload]", Hint: "manage local plugins", Category: "App"},
	{Name: "/pwd", Hint: "print working directory", Category: "Session"},
	{Name: "/quit", Hint: "exit", Keybind: "ctrl+c ctrl+c", Category: "App"},
	{Name: "/rename", Args: "[title]", Hint: "retitle this session", Category: "Session"},
	{Name: "/search", Args: "<query>", Hint: "search sessions and message text", Category: "Session"},
	{Name: "/tag", Args: "[add|remove] <tag>", Hint: "manage session tags", Category: "Session"},
	{Name: "/report", Hint: "bug report: issue link + environment snippet", Category: "App"},
	{Name: "/review", Args: "<branch> [--fix]", Hint: "review changes against a branch", Category: "Agent"},
	{Name: "/rewind", Hint: "browse turns and rewind the conversation (f forks)", Category: "Session"},
	{Name: "/resume", Args: "[id]", Hint: "resume a previous session", Category: "Session"},
	{Name: "/schedule", Args: "@every 10m|@at <time> <prompt>", Hint: "schedule a wakeup; list | cancel", Category: "Session"},
	{Name: "/session-info", Hint: "show session details (alias: /status)", Category: "Session"},
	{Name: "/subagent", Args: "[-m model] <prompt>", Hint: "spawn a background subagent", Category: "Session"},
	{Name: "/subagents", Args: "[id]", Hint: "subagent dock / live view (alias /tasks)", Keybind: "ctrl+t", Category: "Session"},
	{Name: "/status", Hint: "show session details (alias: /session-info)", Category: "Session"},
	{Name: "/system", Hint: "edit the system prompt in system.md", Category: "Agent"},
	{Name: "/theme", Args: "[light|dark|auto]", Hint: "color scheme", Category: "Display"},
	{Name: "/title", Args: "[title]", Hint: "retitle this session (alias: /rename)", Category: "Session"},
	{Name: "/undo", Hint: "rewind the conversation to an earlier turn", Category: "Session"},
	{Name: "/view-plan", Hint: "show the current model-managed plan (alias: /plan)", Category: "Session"},
	{Name: "!cmd", Hint: "run a shell command; output joins the conversation", Category: "App"},
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

func helpText() string { return helpTextFor(i18n.English) }

func helpTextFor(language string) string {
	tr := func(s string) string { return i18n.Text(language, s) }
	var b strings.Builder
	for _, e := range slashRegistry() {
		b.WriteString(e.Name + " " + e.helpHint(language) + "\n")
	}
	b.WriteString(tr(palHintRewind+" — "+palDescRewind) + "\n")
	b.WriteString("!cmd " + registryFind("!cmd").helpHint(language) + "\n")
	b.WriteString("!!cmd " + registryFind("!!cmd").helpHint(language) + "\n")
	b.WriteString(tr("Tab — complete"))
	for _, hint := range []string{
		"Shift+Tab — cycle Normal / Plan / Always allow",
		"Ctrl+K — clear the conversation",
		"Ctrl+T — focus the subagents dock (↑/↓ select, Enter opens, Esc Backs out)",
		palHintThinking + " — toggle thinking tokens",
		"Ctrl+E — expand the last tool result",
		"Ctrl+J / Shift+Enter — newline",
		"Ctrl+G — edit the current draft in an external editor",
		"Ctrl+V — paste image",
		"Esc — interrupt the agent",
		"Esc Esc (idle) — " + palDescRewind + " (↑/↓ browse, Enter rewinds, f forks)",
		"while busy with queued messages: ↑/↓ select, Del removes",
		"PgUp/PgDn — scroll · wheel — scroll · drag — select/copy text",
		palHintQuit + " — quit",
	} {
		b.WriteString(" · " + tr(hint))
	}
	return b.String()
}

func (e registryEntry) helpHint(language string) string {
	description := "— " + i18n.Text(language, e.Hint)
	if e.Args != "" {
		return e.Args + " " + description
	}
	return description
}
