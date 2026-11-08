package sshmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/sshhosts"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// ToolName is the tool's name, as in LiveAgent v1.
const ToolName = tools.SSHManagerTool

// toolDescription follows LiveAgent v1's SSHManager description, adapted to
// how K-brain logs in: missing passwords, keyboard-interactive prompts and
// unknown host keys are asked of the user by K-brain itself instead of in a
// separate terminal tab.
const toolDescription = `Manage SSH sessions and remote SFTP files for SSH hosts explicitly associated with the current project. Use host_id from list_hosts. Sessions are reused by default: exec and SFTP actions called with host_id run on the host's already-running session (one connection per host, shared across tasks and turns) and only open a session when none is running; create_session likewise returns the running session for that host instead of connecting again. Do not call create_session before every task — call exec or SFTP actions with host_id directly, or keep using a session_id you already have. If list_hosts reports credential=saved, K-brain already has the configured password/private key/passphrase; do not ask the user to paste credentials into chat. If list_hosts reports credential=missing, never request the secret in chat: for a password host, connecting shows the user a secure login form for the password; a private-key host cannot connect until the user configures the key in Settings > SSH. If list_hosts reports credential=interactive, the host uses keyboard-interactive login (for example MFA): connecting shows the server's prompts to the user in a secure login form. A host key seen for the first time is confirmed by the user in a question card; a changed host key is refused and must be resolved by the user in Settings > SSH. Set session_strategy="new" only when an additional, separate SSH connection is really needed, then use the returned session_id for follow-up operations. Use session_strategy="require_existing" to fail instead of implicitly connecting. Do not combine session_id with session_strategy="new".`

const toolSchema = `{
  "type": "object",
  "properties": {
    "action": {"type": "string", "enum": ["list_hosts","list_sessions","create_session","read_session","send_input","resize_session","close_session","exec","sftp_list","sftp_stat","sftp_read_text","sftp_write_text","sftp_mkdir","sftp_rename","sftp_delete","sftp_upload","sftp_download","sftp_transfer_status","sftp_cancel_transfer"], "description": "SSH/SFTP action to perform."},
    "host_id": {"type": "string", "description": "Authorized SSH host id from list_hosts. Required when no session_id is supplied."},
    "session_id": {"type": "string", "description": "SSH session id from list_sessions or create_session."},
    "session_strategy": {"type": "string", "enum": ["reuse_or_create","new","require_existing"], "description": "Session resolution for create_session, exec and SFTP actions when session_id is omitted. Defaults to \"reuse_or_create\": reuse the host's running session and connect only when none is running. Use \"new\" only when an additional SSH connection is intentional; use \"require_existing\" to avoid implicit connection."},
    "title": {"type": "string", "description": "Optional title when a new session is opened."},
    "cols": {"type": "number", "description": "PTY columns for create/resize."},
    "rows": {"type": "number", "description": "PTY rows for create/resize."},
    "sftp_enabled": {"type": "boolean", "description": "Whether create_session needs SFTP. Defaults to true; a reused session without SFTP gets it enabled in place (no new connection)."},
    "data": {"type": "string", "description": "Raw PTY input for send_input, including newlines/control chars."},
    "command": {"type": "string", "description": "Remote command for exec."},
    "cwd": {"type": "string", "description": "Optional remote working directory for exec."},
    "timeout_ms": {"type": "number", "description": "Exec timeout in milliseconds."},
    "max_bytes": {"type": "number", "description": "Maximum captured bytes for output."},
    "path": {"type": "string", "description": "Remote SFTP path."},
    "from_path": {"type": "string", "description": "Source path for rename."},
    "to_path": {"type": "string", "description": "Target path for rename."},
    "content": {"type": "string", "description": "Text content for sftp_write_text."},
    "offset": {"type": "number", "description": "Byte offset for sftp_read_text."},
    "limit": {"type": "number", "description": "Byte limit for sftp_read_text."},
    "overwrite": {"type": "boolean", "description": "Whether upload/download/write may overwrite existing files."},
    "recursive": {"type": "boolean", "description": "Recursive directory transfer/delete."},
    "local_path": {"type": "string", "description": "Local workspace path for upload/download. Accepts workspace-relative paths exactly as returned by file tools, absolute paths inside the workspace, ~/..., or file:// values."},
    "remote_path": {"type": "string", "description": "Remote path for upload/download."},
    "transfer_id": {"type": "string", "description": "SFTP transfer id."}
  },
  "required": ["action"]
}`

var actions = map[string]bool{
	"list_hosts": true, "list_sessions": true, "create_session": true, "read_session": true, "send_input": true,
	"resize_session": true, "close_session": true, "exec": true, "sftp_list": true, "sftp_stat": true,
	"sftp_read_text": true, "sftp_write_text": true, "sftp_mkdir": true, "sftp_rename": true, "sftp_delete": true,
	"sftp_upload": true, "sftp_download": true, "sftp_transfer_status": true, "sftp_cancel_transfer": true,
}

// ToolOptions configure the tool.
type ToolOptions struct {
	// Interaction says, for a call's context, what the call may ask the
	// user. Nil means nobody can be asked.
	Interaction func(context.Context) Interaction
}

// Tool is SSHManager for the hosts and sessions of m. The project is the
// call's working directory (tools.WorkingDir).
func Tool(m *Manager, opts ToolOptions) tools.Tool {
	r := &runner{m: m, opts: opts}
	return tools.Tool{Def: ai.NewTool(ToolName, toolDescription, toolSchema), Run: r.run}
}

type runner struct {
	m    *Manager
	opts ToolOptions
}

// args are the call's arguments, read as loosely as LiveAgent v1 reads them.
type args map[string]any

func (a args) str(key string) string {
	v, _ := a[key].(string)
	return strings.TrimSpace(v)
}

func (a args) raw(key string) string {
	v, _ := a[key].(string)
	return v
}

func (a args) require(key string) (string, error) {
	if v := a.str(key); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("SSHManager.%s is required.", key)
}

func (a args) boolean(key string, fallback bool) bool {
	if v, ok := a[key].(bool); ok {
		return v
	}
	return fallback
}

// number reads a JavaScript-style number (a JSON number or a numeric
// string); ok is false when the value is absent or not a finite number.
func (a args) number(key string) (float64, bool) {
	switch v := a[key].(type) {
	case float64:
		return v, !math.IsInf(v, 0) && !math.IsNaN(v)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil && !math.IsInf(f, 0) && !math.IsNaN(f)
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// clamp is v1's normalizePositiveInt.
func (a args) clamp(key string, fallback, lo, hi int) int {
	n, ok := a.number(key)
	if !ok {
		return fallback
	}
	return int(math.Min(float64(hi), math.Max(float64(lo), math.Floor(n))))
}

// optionalClamp is v1's normalizeOptionalPositiveInt; ok is false when the
// value is absent.
func (a args) optionalClamp(key string, lo, hi int) (int, bool) {
	if v, present := a[key]; !present || v == nil || v == "" {
		return 0, false
	}
	n, ok := a.number(key)
	if !ok {
		return 0, false
	}
	return int(math.Min(float64(hi), math.Max(float64(lo), math.Floor(n)))), true
}

func (r *runner) run(ctx context.Context, raw json.RawMessage) (string, error) {
	text, err := r.dispatch(ctx, raw)
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("SSHManager failed: " + err.Error())
	}
	return text, nil
}

func (r *runner) dispatch(ctx context.Context, raw json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	a := args{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", errors.New("SSHManager arguments must be a JSON object.")
		}
	}
	action := a.str("action")
	if !actions[action] {
		return "", errors.New("SSHManager.action is invalid.")
	}
	workdir := tools.WorkingDir(ctx)
	project := sshhosts.ProjectPathKey(workdir)
	if project == "" {
		return "", errors.New("No current project is selected.")
	}
	hosts := r.m.Hosts().ProjectHosts(project)
	if len(hosts) == 0 {
		return "", errors.New("No SSH hosts are associated with the current project.")
	}
	allowed := make(map[string]sshhosts.Host, len(hosts))
	for _, h := range hosts {
		allowed[h.ID] = h
	}
	c := &call{runner: r, ctx: ctx, a: a, action: action, workdir: workdir, project: project, hosts: hosts, allowed: allowed}
	if !tools.SSHManagerReadOnly(action) {
		if err := c.validate(); err != nil {
			return "", err
		}
		command, _ := tools.SSHManagerGateCommand(raw)
		if err := tools.Authorize(ctx, ToolName, command); err != nil {
			return "", err
		}
	}
	return c.do()
}

// call is one SSHManager invocation.
type call struct {
	*runner
	ctx     context.Context
	a       args
	action  string
	workdir string
	project string
	hosts   []sshhosts.Host
	allowed map[string]sshhosts.Host
}

// validate checks what can be checked before the user is asked to approve
// the call, so a malformed call fails without a prompt.
func (c *call) validate() error {
	a := c.a
	if _, err := c.strategy(); err != nil {
		return err
	}
	if c.action != "create_session" && a.str("session_id") != "" && a.str("session_strategy") == string(NewSession) {
		switch c.action {
		case "exec", "sftp_list", "sftp_stat", "sftp_read_text", "sftp_write_text", "sftp_mkdir", "sftp_rename", "sftp_delete", "sftp_upload", "sftp_download":
			return errors.New("SSHManager.session_strategy=new cannot be combined with session_id.")
		}
	}
	var required []string
	switch c.action {
	case "create_session":
		required = []string{"host_id"}
	case "send_input", "resize_session", "close_session", "sftp_cancel_transfer":
		required = []string{"session_id"}
	case "exec":
		required = []string{"command"}
	case "sftp_read_text", "sftp_write_text", "sftp_mkdir", "sftp_delete":
		required = []string{"path"}
	case "sftp_rename":
		required = []string{"from_path", "to_path"}
	case "sftp_upload", "sftp_download":
		required = []string{"local_path", "remote_path"}
	}
	if c.action == "sftp_cancel_transfer" {
		required = append(required, "transfer_id")
	}
	for _, key := range required {
		if _, err := a.require(key); err != nil {
			return err
		}
	}
	if c.action == "send_input" && a.raw("data") == "" {
		return errors.New("SSHManager.data is required.")
	}
	if c.a.str("session_id") == "" && c.needsSession() {
		id, err := a.require("host_id")
		if err != nil {
			return err
		}
		if _, ok := c.allowed[id]; !ok {
			return errors.New("SSH host is not associated with the current project.")
		}
	}
	return nil
}

// needsSession reports actions that resolve a session from host_id when no
// session_id is given.
func (c *call) needsSession() bool {
	return c.action == "exec" || strings.HasPrefix(c.action, "sftp_") && c.action != "sftp_transfer_status" && c.action != "sftp_cancel_transfer"
}

func (c *call) strategy() (Strategy, error) {
	switch s := Strategy(c.a.str("session_strategy")); s {
	case "":
		return ReuseOrCreate, nil
	case ReuseOrCreate, NewSession, RequireExisting:
		return s, nil
	}
	return "", errors.New("SSHManager.session_strategy is invalid.")
}

func (c *call) interaction() Interaction {
	if c.opts.Interaction == nil {
		return Interaction{}
	}
	return c.opts.Interaction(c.ctx)
}

func (c *call) do() (string, error) {
	switch c.action {
	case "list_hosts":
		return c.listHosts(), nil
	case "list_sessions":
		return c.listSessions()
	case "create_session":
		return c.createSession()
	case "read_session":
		return c.readSession()
	case "send_input":
		return c.sendInput()
	case "resize_session":
		return c.resizeSession()
	case "close_session":
		s, err := c.session()
		if err != nil {
			return "", err
		}
		c.m.CloseSession(s)
		return fmt.Sprintf("Closed SSH session %s.", s.ID), nil
	case "exec":
		return c.exec()
	case "sftp_transfer_status":
		s, err := c.session()
		if err != nil {
			return "", err
		}
		id, err := c.a.require("transfer_id")
		if err != nil {
			return "", err
		}
		status, err := s.TransferStatus(id)
		if err != nil {
			return "", err
		}
		return pretty(status)
	case "sftp_cancel_transfer":
		s, err := c.session()
		if err != nil {
			return "", err
		}
		id, _ := c.a.require("transfer_id")
		if err := s.CancelTransfer(id); err != nil {
			return "", err
		}
		return fmt.Sprintf("Cancelled SFTP transfer %s.", id), nil
	}
	return c.sftp()
}

func (c *call) listHosts() string {
	lines := []string{
		"Authorized SSH hosts:",
		"credential=saved means K-brain already has the configured SSH credential; do not ask the user for that password/key. Call exec or SFTP actions with host_id directly: they reuse the host's running session and only connect when none is running, so there is no need to call create_session first.",
		"credential=missing means no password/key is saved: for a password host SSHManager asks the user for it in a secure login form when it connects; never ask for the secret in chat. A private-key host must be configured in Settings > SSH first.",
		"credential=interactive means the host logs in via keyboard-interactive prompts (for example MFA): SSHManager shows the server's prompts to the user in a secure login form when it connects.",
	}
	for _, h := range c.hosts {
		label := h.Name
		if label == "" {
			label = h.Endpoint()
		}
		lines = append(lines, fmt.Sprintf("- %s · %s · %s · auth=%s · credential=%s", h.ID, label, h.Endpoint(), h.AuthType, h.Credential()))
	}
	return strings.Join(lines, "\n")
}

func formatSession(info SessionInfo) string {
	return fmt.Sprintf("- %s · host=%s · %s · sftp=%t · running=%t · created_at=%d · updated_at=%d · %s",
		info.SessionID, info.HostID, info.Status, info.SFTPEnabled, info.Running, info.CreatedAt, info.UpdatedAt, info.Title)
}

func formatResolution(r Resolved) []string {
	return []string{
		"session_id: " + r.Session.ID,
		"session_strategy: " + r.Strategy,
		"session_reused: " + strconv.FormatBool(r.Reused),
		"session_created: " + strconv.FormatBool(r.Created),
	}
}

func (c *call) listSessions() (string, error) {
	hostID := c.a.str("host_id")
	if hostID != "" {
		if _, ok := c.allowed[hostID]; !ok {
			return "", errors.New("SSH host is not associated with the current project.")
		}
	}
	var lines []string
	for _, s := range c.m.Sessions(c.project) {
		if _, ok := c.allowed[s.HostID]; !ok || (hostID != "" && s.HostID != hostID) {
			continue
		}
		lines = append(lines, formatSession(s.Info()))
	}
	if len(lines) == 0 {
		return "No SSH sessions are open for the current project.", nil
	}
	return strings.Join(append([]string{"Project SSH sessions:"}, lines...), "\n"), nil
}

// session is the session named by session_id.
func (c *call) session() (*Session, error) {
	id, err := c.a.require("session_id")
	if err != nil {
		return nil, err
	}
	s, ok := c.m.Session(c.project, id)
	if !ok {
		return nil, errors.New("SSH session not found in the current project.")
	}
	if _, ok := c.allowed[s.HostID]; !ok {
		return nil, errors.New("SSH session host is not authorized for the current project.")
	}
	return s, nil
}

// acquire finds or opens the session of host per the call's strategy.
func (c *call) acquire(host sshhosts.Host, needsSFTP bool) (Resolved, error) {
	strategy, err := c.strategy()
	if err != nil {
		return Resolved{}, err
	}
	title := c.a.str("title")
	r, err := c.m.Acquire(c.ctx, c.project, host, strategy, title, c.interaction())
	if errors.Is(err, errNoSession) {
		return Resolved{}, errors.New("No reusable SSH session exists for this host in the current project.")
	}
	if err != nil {
		return Resolved{}, err
	}
	if needsSFTP {
		if err := r.Session.EnableSFTP(c.ctx); err != nil {
			return Resolved{}, err
		}
	}
	return r, nil
}

// resolve is v1's resolveSession: session_id when given, else the host's
// session per session_strategy.
func (c *call) resolve(needsSFTP bool) (Resolved, error) {
	if c.a.str("session_id") != "" {
		strategy, err := c.strategy()
		if err != nil {
			return Resolved{}, err
		}
		if strategy == NewSession {
			return Resolved{}, errors.New("SSHManager.session_strategy=new cannot be combined with session_id.")
		}
		s, err := c.session()
		if err != nil {
			return Resolved{}, err
		}
		if needsSFTP {
			if err := s.EnableSFTP(c.ctx); err != nil {
				return Resolved{}, err
			}
		}
		return Resolved{Session: s, Strategy: "session_id", Reused: true}, nil
	}
	hostID, err := c.a.require("host_id")
	if err != nil {
		return Resolved{}, err
	}
	host, ok := c.allowed[hostID]
	if !ok {
		return Resolved{}, errors.New("SSH host is not associated with the current project.")
	}
	return c.acquire(host, needsSFTP)
}

func (c *call) createSession() (string, error) {
	hostID, err := c.a.require("host_id")
	if err != nil {
		return "", err
	}
	host, ok := c.allowed[hostID]
	if !ok {
		return "", errors.New("SSH host is not associated with the current project.")
	}
	r, err := c.acquire(host, c.a.boolean("sftp_enabled", true))
	if err != nil {
		return "", err
	}
	if r.Created {
		cols, hasCols := c.a.optionalClamp("cols", 20, 400)
		rows, hasRows := c.a.optionalClamp("rows", 6, 200)
		if hasCols || hasRows {
			r.Session.setPTYSize(cols, rows)
		}
	}
	head := "Reusing the running SSH session for this host (no new connection was opened):"
	if r.Created {
		head = "Created SSH session:"
	}
	lines := append([]string{head, formatSession(r.Session.Info())}, formatResolution(r)...)
	return strings.Join(lines, "\n"), nil
}

func (c *call) readSession() (string, error) {
	s, err := c.session()
	if err != nil {
		return "", err
	}
	out, err := s.ReadShell(c.a.clamp("max_bytes", DefaultReadBytes, MinReadBytes, MaxReadBytes))
	if err != nil {
		return "", err
	}
	text := out.Output
	if text == "" {
		text = "(empty output)"
	}
	return strings.Join([]string{
		"session_id: " + s.ID,
		"host_id: " + s.HostID,
		"truncated: " + strconv.FormatBool(out.Truncated),
		"",
		text,
	}, "\n"), nil
}

func (c *call) sendInput() (string, error) {
	s, err := c.session()
	if err != nil {
		return "", err
	}
	data := c.a.raw("data")
	if data == "" {
		return "", errors.New("SSHManager.data is required.")
	}
	if err := s.SendInput(data); err != nil {
		return "", err
	}
	// v1 reports the JavaScript string length (UTF-16 code units).
	return fmt.Sprintf("Sent %d characters to SSH session %s.", len(utf16.Encode([]rune(data))), s.ID), nil
}

func (c *call) resizeSession() (string, error) {
	s, err := c.session()
	if err != nil {
		return "", err
	}
	cols := c.a.clamp("cols", 80, 20, 400)
	rows := c.a.clamp("rows", 24, 6, 200)
	if err := s.Resize(cols, rows); err != nil {
		return "", err
	}
	return fmt.Sprintf("Resized SSH session %s to %dx%d.", s.ID, cols, rows), nil
}

func (c *call) exec() (string, error) {
	command, err := c.a.require("command")
	if err != nil {
		return "", err
	}
	r, err := c.resolve(false)
	if err != nil {
		return "", err
	}
	timeout := DefaultExecTimeout
	if ms, ok := c.a.optionalClamp("timeout_ms", int(MinExecTimeout/time.Millisecond), int(MaxExecTimeout/time.Millisecond)); ok {
		timeout = time.Duration(ms) * time.Millisecond
	}
	maxBytes := DefaultExecBytes
	if n, ok := c.a.optionalClamp("max_bytes", MinExecBytes, MaxExecBytes); ok {
		maxBytes = n
	}
	result, err := r.Session.Exec(c.ctx, command, c.a.str("cwd"), timeout, maxBytes)
	if err != nil {
		return "", err
	}
	exitCode := "unknown"
	if result.ExitCode != nil {
		exitCode = strconv.Itoa(*result.ExitCode)
	}
	stdout, stderr := result.Stdout, result.Stderr
	if stdout == "" {
		stdout = "(empty)"
	}
	if stderr == "" {
		stderr = "(empty)"
	}
	lines := append(formatResolution(r),
		"exit_code: "+exitCode,
		"timed_out: "+strconv.FormatBool(result.TimedOut),
		"",
		"stdout:",
		stdout,
		"",
		"stderr:",
		stderr,
	)
	return strings.Join(lines, "\n"), nil
}

func (c *call) sftp() (string, error) {
	r, err := c.resolve(true)
	if err != nil {
		return "", err
	}
	s, a, ctx := r.Session, c.a, c.ctx
	var result any
	switch c.action {
	case "sftp_list":
		result, err = s.List(ctx, a.str("path"))
	case "sftp_stat":
		result, err = s.Stat(ctx, a.str("path"))
	case "sftp_read_text":
		offset := 0
		if n, ok := a.optionalClamp("offset", 0, math.MaxInt32); ok {
			offset = n
		}
		maxBytes := DefaultReadTextBytes
		key := "limit"
		if _, ok := a[key]; !ok || a[key] == nil {
			key = "max_bytes"
		}
		if n, ok := a.optionalClamp(key, MinReadTextBytes, MaxReadTextBytes); ok {
			maxBytes = n
		}
		result, err = s.ReadText(ctx, a.str("path"), int64(offset), maxBytes)
	case "sftp_write_text":
		result, err = s.WriteText(ctx, a.str("path"), a.raw("content"), a.boolean("overwrite", true), true)
	case "sftp_mkdir":
		result, err = s.Mkdir(ctx, a.str("path"))
	case "sftp_delete":
		result, err = s.Delete(ctx, a.str("path"), a.boolean("recursive", false))
	case "sftp_rename":
		result, err = s.Rename(ctx, a.str("from_path"), a.str("to_path"))
	case "sftp_upload", "sftp_download":
		result, err = c.transfer(s)
	default:
		return "", errors.New("Unsupported SSHManager action.")
	}
	if err != nil {
		return "", err
	}
	return pretty(result)
}

func (c *call) transfer(s *Session) (any, error) {
	upload := c.action == "sftp_upload"
	access := "write"
	if upload {
		access = "read"
	}
	local, err := tools.ResolveWorkspacePath(c.ctx, c.a.raw("local_path"), access, "SSHManager.local_path", true)
	if err != nil {
		return nil, err
	}
	remote, err := c.a.require("remote_path")
	if err != nil {
		return nil, err
	}
	state, err := s.StartTransfer(c.ctx, TransferRequest{
		Upload: upload, Local: local, Remote: remote,
		Recursive: c.a.boolean("recursive", false), Overwrite: c.a.boolean("overwrite", false),
		Display: c.display,
	})
	if err != nil {
		return nil, err
	}
	return TransferResponse{Transfer: state}, nil
}

// display renders a local path relative to the working directory when it is
// inside it, as v1 reports transfer paths.
func (c *call) display(p string) string {
	if c.workdir != "" {
		if rel, err := filepath.Rel(c.workdir, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(p)
}

// pretty is JSON.stringify(v, null, 2).
func pretty(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}
