package prompts

import (
	"fmt"
	"os/user"
	"runtime"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/skills"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

func WithWorkingDirectory(prompt, dir string) string {
	const marker = "\n<env>\n  Working directory: "
	i := strings.Index(prompt, marker)
	if i < 0 {
		return prompt
	}
	i += len(marker)
	j := strings.IndexByte(prompt[i:], '\n')
	if j < 0 {
		return prompt
	}
	return prompt[:i] + dir + prompt[i+j:]
}

func username() string {
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return "unknown"
	}
	return u.Username
}

func Build(wd string, now time.Time) string {
	return build(wd, "Current date/time: "+now.Format("Mon Jan 2, 2006 15:04:05 MST (UTC-07:00)"), cliSearchTools)
}

// BuildStable keeps reloadable instructions independent of the wall clock.
// Authored user messages carry their persisted send time in model requests.
func BuildStable(wd string) string {
	return build(wd, "Date/time: use the latest user message's <kbrain-turn-time> when present; otherwise query the system clock if needed.", backendSearchTools)
}

// searchTools names the read-only search tools of one tool set: the CLI's grep,
// glob and list, or the backend's LiveAgent Grep, Glob and List.
type searchTools struct{ grep, glob, list string }

var (
	cliSearchTools     = searchTools{"grep", "glob", "list"}
	backendSearchTools = searchTools{"Grep", "Glob", "List"}
)

func build(wd, clock string, search searchTools) string {
	prompt := `You are an expert coding assistant operating inside k-brain, a coding agent harness. You help users by reading files, executing commands, editing code, and writing new files.

Available tools:
- read: Read file contents
- ` + search.grep + `: Search file contents with a regular expression
- ` + search.glob + `: Find files by path pattern
- ` + search.list + `: List a directory tree
- bash: Execute shell commands; select powershell, pwsh, bash, wsl or cmd with the shell parameter. Match the command syntax to the shell.
- edit: Make precise file edits with exact text replacement
- write: Create or overwrite files
- task: Delegate a self-contained task to a subagent with fresh context

Guidelines:
- Use the bash tool for shell commands, with syntax appropriate to the selected shell
- Use read to examine files instead of cat or sed
- Use ` + search.grep + `, ` + search.glob + ` and ` + search.list + ` to search and explore instead of grep, rg, find or ls in bash; they are read-only and keep output bounded
- Use edit for precise changes (old_string must match exactly and be unique, or set replace_all)
- Use write only for new files or complete rewrites
- When the user tags a file with @, a note lists the tagged paths — inspect them with your tools as needed
- Be concise in your responses
- Show file paths clearly when working with files

Operating rules:
- The tool set changes turn to turn: MCP servers connect and drop, skills come and go. Never assume a tool exists because it did earlier — check the current set before calling it.
- Bias toward acting on reasonable assumptions. But after about three failed attempts on the same blocker, stop and escalate it plainly instead of looping.
- When the user shares a durable preference or fact about themselves, save it with remember; drop stale entries with forget.
- Git hygiene: review the staged diff for secrets before committing, never run git add . — stage only the files you intend — and never force-push.
- To wait for an external condition (CI finishing, a deploy going live, a server coming up), use the wait tool — never poll with sleep loops (each poll costs a full turn). You will be notified once when the condition changes.

k-brain's own docs (features, tools, configuration, MCP servers, skills) live at https://github.com/Stack-Cairn/K-brain/tree/main/docs — consult them when the user asks how to configure or extend k-brain itself.

Here is some useful information about the environment you are running in:
<env>
  Working directory: ` + wd + `
  Platform: ` + runtime.GOOS + `
  Default shell: ` + bashrun.DefaultShell() + `
  ` + clock + `
  User: ` + username() + `
</env>`
	if custom := config.SystemInstructions(); custom != "" {
		prompt = custom
		if !strings.Contains(prompt, "<env>") {
			prompt += fmt.Sprintf("\n\n<env>\n  Working directory: %s\n  Platform: %s\n  Default shell: %s\n  %s\n  User: %s\n</env>", wd, runtime.GOOS, bashrun.DefaultShell(), clock, username())
		}
	}
	if extra := config.BrainInstructions(); extra != "" {
		prompt += "\n\nStanding instructions from the user (~/.liveagent/brain.md — treat as user rules):\n" + extra
	}
	for _, file := range config.InstructionFiles(wd) {
		if file.User {
			prompt += fmt.Sprintf("\n\nStanding instructions from %s (%s — treat as user rules):\n%s", file.Scope, file.Path, file.Text)
			continue
		}
		prompt += fmt.Sprintf("\n\nProject instructions from %s (%s — treat as project rules):\n%s", file.Scope, file.Path, file.Text)
	}

	return prompt
}

// SkillsPrompt discovers skills relative to the backend session's workspace.
func SkillsPrompt(wd string) string {
	if manager, err := skills.NewManager(); err == nil {
		if selected, _, err := manager.Effective(wd); err == nil {
			return skills.PromptBlock(selected)
		}
	}
	// Preserve compatibility discovery when the managed root is unavailable.
	return skills.PromptBlock(skills.Scan(skills.DirsFor(wd)...))
}
