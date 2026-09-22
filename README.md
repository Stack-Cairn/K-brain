<div align="center">

# K-brain · 氪脑

**A terminal coding agent built in Go, with a reusable backend for other frontends.**

Read code, edit files, run commands, and verify results in one session.

Start with **`kn`**, short for **K**e **N**ao, the Chinese name 氪脑.

[![Go](https://img.shields.io/badge/Go-1.27%2B-00ADD8?logo=go&logoColor=white)](go.mod)
[![Platforms](https://img.shields.io/badge/Windows%20%7C%20Linux%20%7C%20macOS-x64%20%7C%20ARM64-555)](https://github.com/Stack-Cairn/K-brain/releases)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

[简体中文](README.zh-CN.md) · **English**

[Quickstart](#quickstart) · [Usage](#usage) · [Building from source](#building-from-source) · [Documentation](#documentation) · [Repository layout](#repository-layout)

<p align="center">
  <img src="docs/assets/k-brain-tui.png" alt="K-brain TUI" width="900">
</p>

</div>

---

## Quickstart

### 1. Install

**macOS / Linux**

```sh
curl -fsSL https://raw.githubusercontent.com/Stack-Cairn/K-brain/main/install.sh | sh
```

**Windows PowerShell**

```powershell
& ([scriptblock]::Create((Invoke-WebRequest -UseBasicParsing https://raw.githubusercontent.com/Stack-Cairn/K-brain/main/install.ps1).Content))
```

The installers download platform binaries from [GitHub Releases](https://github.com/Stack-Cairn/K-brain/releases) and verify SHA-256 checksums. Windows installs to `%LOCALAPPDATA%\Programs\k-brain` by default, verifies the release version, and puts that directory first in the current process PATH and the saved user PATH. Restart other open terminals to pick up the change. If `kn --version` still shows `dev`, use `Get-Command kn -All` to locate an older executable or an alias. If a suitable release is not available, [build from source](#building-from-source).

<details>
<summary>Manual downloads: Windows, Linux, macOS × x64, ARM64</summary>

Main assets are named `k-brain-<os>-<arch>`, with `.exe` on Windows. `os` is `windows`, `linux`, or `darwin`; `arch` is `x64` or `arm64`. Rename the main binary to `kn` (`kn.exe` on Windows), put it on PATH, and make it executable on Linux/macOS.

For computer-use, rename `k-brain-computer-<os>-<arch>` to `k-brain-computer` (with `.exe` on Windows) and place it beside the main binary. See [Platform notes](#platform-notes) for runtime dependencies.

</details>

### 2. Configure an API

Edit `~/.k-brain/config.json`, or `$HOME\.k-brain\config.json` on Windows. The file supports JSONC comments and trailing commas. Set `K_BRAIN_HOME` to use another configuration directory.

```json
{
  "language": "en",
  "defaultModel": "model1",
  "providers": {
    "demo": {
      "name": "demo",
      "api": "openai-completions",
      "baseUrl": "https://api.example.com/v1",
      "apiKey": "YOUR_API_KEY",
      "models": [
        {"id": "model1", "contextWindow": 128000, "maxTokens": 8192},
        {"id": "model2", "contextWindow": 128000, "maxTokens": 8192}
      ]
    }
  }
}
```

Replace `baseUrl`, `apiKey`, `model1`, and `model2` with your provider's values. K-brain supports third-party API-key connections only, with no built-in account login, OAuth, or subscription authentication.

`apiKey` also accepts an environment reference such as `${MY_API_KEY}` or a local credential command prefixed with `!`. Commands support quoted paths and arguments and execute directly, without an implicit shell or pipeline expansion. Credentials come from standard output with surrounding whitespace removed. Each command has a 5-second timeout and a 64 KiB standard-output limit; failures discard partial output. Credential commands in MCP headers and environment variables follow the same rules.

| `api` | Protocol |
| --- | --- |
| `openai-completions` | OpenAI Chat Completions |
| `openai-responses` | OpenAI Responses |
| `anthropic-messages` | Anthropic Messages |

- Use an API prefix such as `https://api.example.com/v1` for `baseUrl`, not a complete `/chat/completions` endpoint.
- Declare models inside each provider's `models` array. The legacy top-level `models` format is not supported.
- `contextWindow` is the context token limit; `maxTokens` is the output token limit. Set them to the model's actual limits. Explicit configuration takes precedence over the model catalog.
- `/model refresh` fetches `baseUrl + "/models"`, which is `/v1/models` in this example. Use `/model` to select a model.
- Restart after editing the file. The TUI can open without API credentials, but model requests require a valid configuration.

### Prompt hierarchy

K-brain assembles instructions in this order: the system prompt, the user prompt file, then project prompt files. The default system prompt is seeded into `~/.k-brain/system.md` and can be edited with `/system`; `kn run` can replace it for one invocation with `-system` or `-system-file`.

- **User-level instructions**: `~/.k-brain/brain.md` (Windows: `%USERPROFILE%\.k-brain\brain.md`). It is created on first start and can be edited with `/brain`.
- **Project-level instructions**: `AGENTS.md` and `.k-brain/brain.md` in the workspace or any parent directory. Files are loaded from the project root toward the current directory, so a closer file is appended later and can refine parent rules.
- Project prompt files are read by the TUI, `kn run`, and ACP from their requested working directory. Comments and blank lines are ignored.

All three protocols send mixed text and image inputs in order. Responses uses `input_image`; Anthropic uses base64 or URL image sources and retains all system instructions. Image support still depends on the selected model and endpoint. Anthropic inline images must use JPEG, PNG, GIF, or WebP. Invalid image references fail locally instead of being silently omitted. Stored conversations retain their content blocks, and ACP session loading replays inline images alongside the surrounding text.

Browser and Computer-use screenshots are attached to the originating tool result and retained in session history. Parallel calls and subagents keep their images separate; the selected model must support images. This attachment path is shared by the agent backend across TUI, CLI, and ACP, without changing which tools each entry point enables.

The complete user guide is in [`docs/user-guide/`](docs/user-guide/README.md), including prompt files, configuration, sessions, extensions, sandbox, and ACP.

Optional lifecycle hooks can run a local command for an agent event. The command receives a JSON event in `K_BRAIN_HOOK_EVENT`; a non-zero `PreToolUse` hook denies that tool call.

```json
{
  "hooks": {
    "SessionStart": [{"command": "echo session started"}],
    "PreToolUse": [{"command": "echo $K_BRAIN_HOOK_EVENT", "shell": "bash", "timeout": 10}]
  }
}
```

Supported events are `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, and `Stop`. Hooks use the configured shell on Windows (`powershell`, `pwsh`, `bash`, `wsl`, or `cmd`) and the platform shell elsewhere.

CLI, TUI, and ACP use the same lifecycle event dispatcher for configured hooks and enabled plugin hooks. Events include the session ID and working directory; commands run in that directory and inherit the active sandbox policy. Session-start hooks run after the final session ID is assigned, once per activation. TUI model previews and model changes do not repeat them. Failed ACP initialization releases MCP connections and removes only the newly created session; failed restores preserve the saved history.

### Session files

Sessions live under `~/.k-brain/sessions/<project-id>/<session-id>/session.jsonl` (or `$K_BRAIN_HOME/sessions`). Project IDs are stable hashes of the workspace path, keeping sessions grouped by project while exposing each full session ID.

```text
~/.k-brain/
  config.json
  system.md
  brain.md
  sessions/<project-id>/
    <session-id>/
      session.jsonl
```

Run `kn sessions` to list project-grouped session IDs. Use `kn sessions search <query>`, `kn sessions archive <id>`, or `kn sessions delete <id>` for navigation. Resume with `kn --resume <session-id>`; `/status` shows the current session file. The system prompt is stored in `~/.k-brain/system.md` and edited with `/system`; user standing instructions are stored in `~/.k-brain/brain.md` and edited with `/brain`. JSONL records include metadata, messages, tasks, compactions, schedules, and rewind snapshot references. SQLite storage and migration are no longer supported; existing database files are not read or modified.

Project trust decisions use TOML at `~/.k-brain/trusted_folders.toml` (Windows: `~\.k-brain\trusted_folders.toml`), with one `[folders."<absolute-path>"]` table containing `trusted` and `decided_at` fields.

### OS sandbox

Configure command isolation in `config.json`:

```json
{"sandbox":{"mode":"strict","backend":"auto","network":false,"writable":[],"readOnly":[]}}
```

Linux uses Bubblewrap namespaces, macOS uses Seatbelt, and Windows uses WSL2 plus Bubblewrap (`backend: "wsl"`). Strict mode fails closed when its backend is unavailable. Shell, plugin, and background tools inherit the policy.

### Plugins

Put a plugin manifest at `.k-brain/plugins/<name>/plugin.json` or `~/.k-brain/plugins/<name>/plugin.json`:

```json
{"name":"sample","version":"1.0.0","command":["sample-plugin"],"prompt":"Additional instructions","tools":[{"name":"lookup","description":"Look up a value","inputSchema":{"type":"object"}}]}
```

The process receives one JSONL `tool.invoke` request and returns one JSON-RPC response. Plugins are explicitly enabled and inherit the sandbox. Manage them with `kn plugins list|install|enable|disable|remove|reload` or `/plugins`.

### 3. Start a task

Run this in your project directory:

```sh
kn
```

Describe the task directly:

```text
Read this project, find the cause of the failing tests, fix it, run the relevant tests, and summarize the changes.
```

The TUI uses a full-screen terminal view with a bottom-anchored input area. The session title appears at the bottom right after the first turn. Use `kn -c` to continue the latest session in the current directory, or `kn --resume` to open the session picker.

Previewing or switching models keeps the active session, todos, queued guidance, and background tasks. Existing subtasks keep their model for follow-up; new subtasks inherit the selected model unless `taskModel` overrides it. Model changes wait until the current turn or compaction has finished. The TUI saves and restores model-specific usage so switching models preserves prior usage and its pricing attribution.

## Usage

### Interactive sessions

| Input | Action |
| --- | --- |
| `/language` · `/language zh_cn` · `/language zh_Hant` · `/language en` | Choose the interface language (Simplified Chinese / Traditional Chinese / English) |
| `/model` · `/effort` | Switch models and adjust reasoning effort |
| `/context` · `/compact` | Inspect context and compact it manually |
| `/rewind` · `/fork` | Rewind to an earlier turn or branch a session |
| `/forks` · `/export` · `/import` | Show the session tree, export Markdown/JSONL/HTML, or import JSONL |
| `/title` · `/resume` | Rename or resume a session |
| `/diff [--staged] [--stat]` | Inspect tracked Git changes locally, without a model request |
| `/copy [N] [file]` | Copy the Nth latest assistant message with text, or save it to a new file |
| `/prompts [refresh]` | List or reload Markdown prompt templates |
| `/privacy on|off|toggle|status` | Enable the local masking gateway; status reports rule count, in-memory mappings, and body limit |
| `/mcp` · `/tasks` | Manage MCP connections and inspect background tasks |
| `!command` · `!!command` | Run a shell command; the first adds output to the conversation, the second stays local |
| `/help` | Show all commands and keyboard shortcuts |

Use `Ctrl+P` for the command palette, `Ctrl+J` for a newline, `Esc` to interrupt the current task, and `PgUp/PgDn` to scroll. Launch with `kn -cautious` to request confirmation before commands or file writes.

<details>
<summary>Markdown prompt templates</summary>

Put global templates in `~/.k-brain/prompts/` and project templates in a trusted project's `.k-brain/prompts/`. Discovery is non-recursive. Project templates override global templates of the same name; built-in commands take precedence.

Example `audit.md`:

```markdown
---
description: Review a module
argument-hint: "<module> [focus]"
---
Review $1, focusing on ${2:-correctness and test coverage}.
Additional requirements: ${@:3}
```

Run `/prompts refresh`, then enter `/audit "internal/agent" "concurrency"`. The template expands into the input area; press Enter again to send it.

Supported substitutions: `$1`, `$2`, `$@`, `$ARGUMENTS`, `${1:-default}`, `${@:-default}`, `${ARGUMENTS:-default}`, `${@:N}`, and `${@:N:L}`. Arguments accept single or double quotes. Templates do not perform shell or environment-variable expansion. Metadata supports simple single-line fields, not full YAML. Files are limited to 1 MiB.

</details>

### Interface language

Run `/language` to open the language picker, then use ↑/↓ and Enter to apply or Esc to cancel. `/language zh_cn`, `/language zh_Hant`, and `/language en` switch directly and save `language` to `~/.k-brain/config.json`. English is the default; unknown values fall back to English.

`/export` chooses Markdown (`.md`), structured JSONL (`.jsonl`), or HTML (`.html`) from the file extension. `/import <path>` appends messages from a JSONL transcript, and `/forks` shows the current session and its fork relationships.

The slash-command descriptions, help, input hints, primary navigation labels, and language/model picker controls switch immediately. Command names, model/provider identifiers, conversation content, and tool output remain unchanged. This setting does not instruct the model to reply in a specific language; some detailed diagnostics and secondary panels still use English.

### External prompt editor

Press `Ctrl+G` to edit the current draft, or `/editor` to start a new prompt in an external editor. K-brain uses `VISUAL`, then `EDITOR`, falling back to `notepad.exe` on Windows or `vi` on Linux/macOS. Commands accept quoted paths and arguments, such as `code --wait`; shell expressions are not expanded. Save and close the editor to return to the TUI, then press Enter to send. Editing is disabled during an active turn. Failed or oversized edits are retained in a temporary file for recovery.

### MCP servers

Declare MCP servers in the `mcp` object in `~/.k-brain/config.json`, or use `kn mcp add <name> -- <command...>` and `kn mcp add <name> --url <url>`. Use `kn mcp list`, `kn mcp test <name>`, and `kn mcp remove <name>` to manage them; `/mcp` manages connections in the TUI. External Claude/Codex configuration files are not discovered or imported. ACP clients can still explicitly provide servers for their sessions.

The old `mcpImport` field and `kn mcp import` command have been removed. Delete `mcpImport` from existing configuration files; unknown fields are rejected.

### Headless use and backend integration

```sh
kn run "Explain this project's structure"
git diff | kn run "Review these changes"
kn run --format json --quiet --max-turns 8 --timeout 5m "Find and fix the build failure"
```

`--format json` emits newline-delimited JSON events (NDJSON), not one JSON document.

Each `tool_start`, `tool_output`, and `tool_end` event includes the provider call `id`, so parallel calls can be correlated. `tool_output` contains the cumulative output snapshot for that call. The stream is serialized; a broken stdout pipe cancels the run and returns a non-zero error without emitting a false `done` event.

`kn run` saves sessions under project directories, just like the TUI; `--no-session` disables persistence and cannot be combined with `--resume`. Storage failures are reported rather than silently starting a fresh or unsaved conversation. The run timeout also cancels piped-input waits, credential commands, model-catalog requests, and session-start hooks.

ACP supports `session/new`, `session/prompt`, `session/cancel`, `session/close`, `session/list`, `session/load`, and `session/resume`. Model requests and tools are cancelled on editor disconnect, explicit `session/cancel`, or a backend request deadline. A second prompt in a busy session is rejected without interrupting the current turn. Permissions are isolated per session.

Use `session/set_mode` to select `auto`, `ask`, or `plan`. Plan exposes file reading, questions, and todo planning tools; command execution, file edits, and external tools are blocked. Mode changes affect subsequent tool checks in the current turn; they do not undo actions already executed. Switching to Plan also prevents an outstanding permission response from authorizing a new write. New and restored sessions start in Auto.

`session/load` replays the saved conversation; `session/resume` restores it without replay. Both retain the session ID for memory and prompt caching and reject sessions that are already active or loading. Closing a session waits for its turn and persistence to finish before releasing its MCP connections. Session storage failures are reported explicitly, including when the model request also fails.

Installation memory lives in `~/.k-brain/memory.md`. Session memory lives beside its transcript at `~/.k-brain/sessions/<project>/<session-id>/memory.md` and is refreshed before each turn across all three entry points. Deleting a session also removes its memory. Old flat `<session-id>.memory.md` files are not imported. Subagents keep their fresh context without installation or parent-session memory. ACP discovers project skills from the requested working directory.

The TUI, ACP, and `kn run` share session history mapping: summaries and conversation records are saved together. Switching between these entry points retains the latest summary and messages without overwriting the original conversation. Completed compactions are saved even if the following model request is cancelled. The final answer produced at `--max-turns` is also saved for the next turn.

History and cumulative usage are saved together across all three entry points, including model/provider attribution, subagent totals, and cache read/write counts. A dedicated compaction model records its own usage. Anthropic input counts are normalized to include cache reads and writes. TUI cost estimates use the recorded provider's catalog prices; unavailable or ambiguous prices stay unknown.

The status bar, compaction log, and rewind picker share separate calculations for ordinary input, output, cache reads, and cache writes. The model catalog's `pricing` object accepts `prompt`, `completion`, `input_cache_read`, and `input_cache_write` as decimal strings in dollars **per token**. Both ordinary input and output prices must be present. Omitted cache prices use the input price as an estimate; an explicit `"0"` means free. Invalid prices or inconsistent token counts leave the cost unknown, and a turn with missing prices does not show a partial total. Estimates currently use flat rates; context-based pricing tiers, one-hour cache-write rates, and title-generation costs are not included.

Chat Completions, Responses, and Anthropic Messages share SSE framing and report malformed events, provider errors, and streams that end without a completion signal. Failed requests retain usage already received and do not execute pending tools. Chat accepts `[DONE]` or a finish reason followed by a clean close; Responses and Anthropic return as soon as their terminal event arrives. Chat does not automatically retry after receiving generated content, even when no streaming callbacks are registered.

When a streamed reply reaches its output limit, K-brain preserves the partial text and usage, adds a truncation notice, and discards tool calls from that response. This includes Responses `incomplete` with reason `max_output_tokens`; other incomplete reasons remain errors. Saved messages retain normalized `stop_reason` and provider `raw_stop_reason` metadata, which is omitted from subsequent API requests. ACP returns `max_tokens`; `kn run --format json` includes `stopReason: "length"` in its `done` event. Send a follow-up to continue. A truncated reply cannot end a TUI goal through a completion marker. Non-streaming requests return their partial text and an `OutputLimitError`, so a truncated compaction summary cannot replace the original history.

All three protocols retry temporary HTTP failures (408, 409, 429, and 5xx) and connection failures before a successful response. They honor `x-should-retry`, `retry-after-ms`, and `Retry-After`; a server delay above 60 seconds returns an error immediately. Waiting can be cancelled. In `config.json`, `maxRetries` counts all attempts including the first: `1` disables retries; `0` or omission uses eight attempts. Responses and Anthropic do not restart an accepted response stream. Chat also stops retrying after any usage block, including cache-only usage.

In the TUI, `/compact retry` undoes the latest compaction. `/rewind` and `/fork` resolve positions against stored message sequences, including after repeated compactions. Forks retain the applicable summaries and workspace snapshot references. A snapshot still used by another session is preserved when rewinding. New TUI sessions receive a session cache key before their first model request.

`session/list` returns up to 100 sessions per page. Pass `nextCursor` as the next request's `cursor`, keeping the same `cwd` filter. Filtering happens before pagination; archived sessions, empty sessions, and subagent transcripts are excluded. Results use descending update time with session ID as the tie-breaker. Restart from the first page to see sessions added or updated during browsing.

Subagents inherit the current model unless `taskModel` is explicitly set in `config.json`; use `taskProvider` to select its provider. Leaving `taskModel` unset does not trigger a model-catalog request. The TUI's **Subagent model → current model** option clears the override.

At creation, subagents copy the parent's execution tool set, including custom replacements, MCP tools, and plugin tools. Removed tools stay removed. Parent-session tools for questions, task orchestration, todos, and memory are not inherited; Go tool integrations can also set `NoInherit` to exclude a tool. Plan-mode changes continue to apply to existing subagents.

| Entry point | Purpose |
| --- | --- |
| `kn acp` | Connect ACP editors over standard input/output |
| `kn mcp serve` | Expose built-in file and command tools to other clients |
| `kn sessions` | List saved sessions |
| `kn update` | Update the installed version |

The execution core is separate from the TUI and shares model adapters, tool loops, context compaction, background subtasks, and JSONL session files. Current backend entry points are CLI, ACP, and MCP—not a standalone HTTP service. This repository does not include a Desktop frontend.

### Platform notes

- **Windows shell**: prefers PowerShell 7, falling back to Windows PowerShell. Set `K_BRAIN_SHELL` to `pwsh`, `powershell`, `bash`, `wsl`, `cmd`, or an executable path. Native Windows does not support Unix-style interactive PTY forwarding.
- **Computer-use**: Windows uses PowerShell/UIA, macOS uses Python/PyObjC, and Linux uses Python/AT-SPI2. Linux X11 has GTK/Xvfb integration coverage. macOS awaits real-desktop validation; a general Wayland desktop-input portal is not implemented. See the runtime setup below.

Computer-use runtime setup: Windows uses the built-in PowerShell. macOS requires Accessibility and Screen Recording permissions for the terminal/interpreter. Linux requires an active graphical session with DISPLAY and D-Bus. Set `K_BRAIN_COMPUTER_BIN` to override the helper path.

```sh
# macOS
python3 -m venv ~/.k-brain/computer-venv
~/.k-brain/computer-venv/bin/python -m pip install pyobjc-framework-Cocoa pyobjc-framework-Quartz pyobjc-framework-ApplicationServices
export K_BRAIN_COMPUTER_PYTHON="$HOME/.k-brain/computer-venv/bin/python"

# Debian / Ubuntu (X11)
sudo apt-get install python3-pyatspi python3-pil python3-pil.imagetk xdotool
export K_BRAIN_COMPUTER_PYTHON=/usr/bin/python3
```

## Building from source

Requires Git and **Go 1.27+**. The version requirement is recorded in [`go.mod`](go.mod).

```sh
git clone https://github.com/Stack-Cairn/K-brain.git
cd K-brain
go install ./cmd/kn ./cmd/k-brain-computer
```

Ensure Go's binary directory, usually `~/go/bin`, is on PATH. To run or build directly from the repository:

```sh
go run ./cmd/kn
go build -o kn ./cmd/kn
```

On Windows, use `go build -o kn.exe ./cmd/kn`, then `.\kn.exe`. Build or install `cmd/k-brain-computer` separately when using computer-use.

## Documentation

- [Build and release workflow](.github/workflows/build.yml)
- CLI help: `kn --help`, `kn run --help`; TUI help: `/help`

## Repository layout

| Path | Responsibility |
| --- | --- |
| `cmd/kn` | Main binary, TUI, headless execution, and protocol entry points |
| `cmd/k-brain-computer` | Computer-use RPC helper |
| `internal/agent` | Model/tool loop and subtask coordination |
| `internal/ai` · `internal/routing` | Protocol adapters, streaming, and model routing |
| `internal/config` | Configuration, model catalogs, and local settings |
| `internal/tools` | Shell, file access, and editing tools |
| `internal/prompts` · `internal/skills` | Prompts, templates, and skill loading |
| `internal/session` · `internal/memory` | Session persistence and durable memory |
| `internal/mcp` · `internal/acp` · `internal/lsp` | Tool, editor, and language-server protocols |
| `internal/browser` · `internal/computer` | Browser and desktop automation |
| `internal/tui` | Terminal layout, input, and event presentation |

## Development

Start with tests for the changed packages, then run broader checks:

```sh
go test ./internal/computer/... -count=1
go vet ./internal/computer/...
go test ./...
```

Some integration tests need platform tools, a graphical session, or explicit environment opt-ins. A successful build does not imply a passing full test suite. Additional tasks are in [`Taskfile.yaml`](Taskfile.yaml).

The GitHub Actions release workflow runs **only on pushed `v*` tags**. It cross-compiles Windows, Linux, and macOS for x64 / ARM64, generates checksums, and publishes release assets. Ordinary branch pushes do not trigger this workflow.

## References and acknowledgments

K-brain references the architecture and implementation ideas of [context-labs/whip](https://github.com/context-labs/whip/). Its terminal interaction, execution core, and module organization also draw on [OpenAI Codex](https://github.com/openai/codex), [Grok Build](https://x.ai/cli), [Pi](https://github.com/badlogic/pi-mono), and [Claude Code](https://github.com/anthropics/claude-code). This README's organization is inspired by Codex and Grok Build.

K-brain is independently maintained and does not represent those projects or their developers.
## community

[LINUX DO](https://linux.do)

## License

[Apache License 2.0](LICENSE).
