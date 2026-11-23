# Permission modes

K-brain owns authorization. Clients send `options.permission_mode` with an HTTP run request; they render approval events and send answers without executing tools themselves. `/v1/health` advertises the supported `capabilities.permission_modes` list. An unsupported mode must never silently become automatic execution.

| Value | Behavior |
| --- | --- |
| `plan` | Read-only planning; uses the agent's existing plan-mode tool filter and `ExitPlanMode` flow. |
| `normal` | Confirm actions requiring authorization; explicit per-tool rules still apply. |
| `auto_edit` | Automatically authorize `Write`/`Edit` and CLI `write`/`edit`; other gated actions require confirmation. Explicit per-tool rules still apply. |
| `auto_review` | Use the configured isolated AI reviewer for this run even if global AI approval is off. Low-risk reviews may proceed; unknown, invalid, missing-model or elevated-risk reviews require human approval. |
| `full_access` | Allow gated actions by default; retain explicit per-tool ask/deny rules and the configured AI reviewer. |
| `always` | Always allow interactive approval requests, bypass AI review, and retain explicit deny rules, plan restrictions, computer configuration denials and workspace/sandbox constraints. |

An explicit `approval_policy: deny` takes precedence over every permission mode. Existing requests without `permission_mode` retain the previous approval policy behavior. A legacy `plan_mode_enabled: true` also retains its read-only restriction regardless of the selected mode.

TUI: `/permissions normal|plan|auto_review|auto_edit|full_access|always`. Shift+Tab cycles Normal → Plan → Auto review → Always allow. The mode chosen in a folder is kept for that folder (`permissionModes` in `projects.json`; Plan is not kept). A folder with no choice starts in `permissionMode` from `config.json`; first-run setup writes `normal` there, and a config without the key starts in `always` as before. `--cautious` starts that run in `normal`. Reviewer provider/model settings are shared with the existing AI approval settings; selecting a mode does not overwrite the global configuration.

CLI: `kn run --permission-mode auto_review "task"`. Non-interactive runs reject operations that require human approval. `--permission-mode always` bypasses AI approval for that run without changing global settings.

LiveAgent uses HTTP to submit runs and receive backend events. Its Gateway WebSocket protocol transports the same selected mode and stable client request identity; Gateway translates modes into this HTTP contract. Reconnecting a transport must recover the accepted request rather than submit a second action.

## Verification

The HTTP integration tests use a real server and filesystem mutations, with a scripted main model and localhost HTTP reviewer. They cover all six modes, explicit deny precedence, low/unknown/malformed review and missing reviewer configuration. CLI tests execute actual file tools for all six modes; unavailable human approval leaves files unchanged. TUI tests cover automatic decisions, manual fallbacks and plan precedence. Backend, CLI, TUI and approval packages passed the Go race detector.

LiveAgent's `permission-modes-backend.test.mjs` additionally launches the built executable and verifies the frontend HTTP client against actual Write/Bash execution and permission events. No production model service is required for these regressions.
