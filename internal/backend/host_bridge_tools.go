package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

// Tools executed through the desktop host bridge. Names, schemas, descriptions and
// result text follow LiveAgent v1 (terminalTools.ts, tunnelManagerTools.ts) so
// existing prompts keep working.

// readTerminalHostDescription is the v1 description plus how to reach K-brain's own
// TerminalSession terminals, which share the tool name.
const readTerminalHostDescription = "Read recent output from a terminal session that belongs to the current project. This tool is read-only and cannot send input or control terminals. To read a TerminalSession started in this conversation, pass its session_id instead of terminal_id."

const readTerminalHostSchema = `{"type":"object","properties":{"terminal_id":{"type":"string"},"max_bytes":{"type":"number"},"session_id":{"type":"string","description":"Only for a TerminalSession started in this conversation: its session_id. Omit to read the LiveAgent desktop terminals of the current project."}}}`

const tunnelManagerDescription = "Manage temporary Remote HTTP/WebSocket/SSE tunnels through the Gateway. Use list to inspect active tunnels and their health, create to expose a localhost or IPv4/IPv6 http service, check to re-run health probes, and close to revoke a tunnel. Mutations also work while the gateway link is offline; they take effect once the link is restored."

const tunnelManagerSchema = `{"type":"object","properties":{"action":{"type":"string","enum":["list","create","close","check"],"description":"Tunnel action to perform."},"targetUrl":{"type":"string","description":"Required for action=create. HTTP target, e.g. http://localhost:3000, http://127.0.0.1:5173/app, or http://192.168.1.5:8080."},"name":{"type":"string","description":"Optional display name for a created tunnel."},"ttlSeconds":{"type":"number","enum":[0,900,3600,14400],"description":"Optional tunnel lifetime. Use 0 for unlimited. Defaults to 3600 seconds."},"id":{"type":"string","description":"Tunnel id. Required for action=close. Optional for action=check (omit to check all tunnels)."}},"required":["action"]}`

// tunnelCheckSettle is how long check waits for the asynchronous probes it
// triggered before sampling the state (v1 CHECK_SETTLE_DELAY_MS).
var tunnelCheckSettle = 2500 * time.Millisecond

// syncHostTools registers the host tools a connected bridge advertises on ag and
// removes them when it no longer does. Runs call it before they start, so a bridge
// that connects after the session loaded is picked up by the next run.
func (s *Server) syncHostTools(ag *agent.Agent) {
	if ag == nil {
		return
	}
	var out []tools.Tool
	if s.hostBridge.provides(hostToolReadTerminal) {
		out = append(out, s.readTerminalHostTool(ag))
	}
	if s.hostBridge.provides(hostToolTunnelManager) {
		out = append(out, s.tunnelManagerTool(ag))
	}
	ag.SetHostTools(out)
}

// ownReadTerminal is the agent's own ReadTerminal (TerminalSession output), which
// the host tool keeps reachable through session_id.
func ownReadTerminal(ag *agent.Agent) *tools.Tool {
	for i := range ag.Tools {
		if ag.Tools[i].Def.Function.Name == hostToolReadTerminal {
			tool := ag.Tools[i]
			return &tool
		}
	}
	return nil
}

func hostToolArgs(raw json.RawMessage) map[string]any {
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil || args == nil {
		return map[string]any{}
	}
	return args
}

func hostToolString(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

// hostReadTerminalMaxBytes clamps max_bytes like v1: 32 KiB by default, between
// 4 KiB and 128 KiB.
func hostReadTerminalMaxBytes(value any) int {
	var n float64
	switch v := value.(type) {
	case float64:
		n = v
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			n = math.NaN()
		} else {
			n = parsed
		}
	case nil:
		n = math.NaN()
	default:
		n = math.NaN()
	}
	n = math.Floor(n)
	if math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
		return 32 * 1024
	}
	return int(math.Min(128*1024, math.Max(4*1024, n)))
}

type hostTerminalSession struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Cwd     string `json:"cwd"`
	Shell   string `json:"shell"`
	Running bool   `json:"running"`
}

type hostTerminalReadTail struct {
	Sessions        []hostTerminalSession `json:"sessions"`
	SelectedSession *hostTerminalSession  `json:"selectedSession"`
	Output          string                `json:"output"`
	Truncated       bool                  `json:"truncated"`
}

func formatHostTerminalRead(result hostTerminalReadTail) string {
	if len(result.Sessions) == 0 {
		return "No terminal sessions are open for the current project."
	}
	var header string
	if selected := result.SelectedSession; selected != nil {
		header = strings.Join([]string{
			"terminal_id: " + selected.ID,
			"title: " + selected.Title,
			"cwd: " + selected.Cwd,
			"shell: " + selected.Shell,
			"running: " + strconv.FormatBool(selected.Running),
			"truncated: " + strconv.FormatBool(result.Truncated),
		}, "\n")
	} else {
		lines := []string{"Multiple terminal sessions are available:"}
		for _, session := range result.Sessions {
			state := "exited"
			if session.Running {
				state = "running"
			}
			lines = append(lines, fmt.Sprintf("- %s · %s · %s", session.ID, session.Title, state))
		}
		header = strings.Join(lines, "\n")
	}
	output := result.Output
	if output == "" {
		output = "(empty output)"
	}
	return header + "\n\n" + output
}

func (s *Server) readTerminalHostTool(ag *agent.Agent) tools.Tool {
	own := ownReadTerminal(ag)
	return tools.Tool{Def: ai.NewTool(hostToolReadTerminal, readTerminalHostDescription, readTerminalHostSchema), Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		args := hostToolArgs(raw)
		if sessionID := hostToolString(args, "session_id"); sessionID != "" && own != nil {
			forward := map[string]any{"session_id": sessionID}
			if value, ok := args["max_bytes"]; ok {
				forward["max_bytes"] = value
			}
			encoded, err := json.Marshal(forward)
			if err != nil {
				return "", err
			}
			return own.Run(ctx, encoded)
		}
		key := liveAgentProjectPathKey(ag.WorkingDir)
		if key == "" {
			return "", errors.New("No current project is selected.")
		}
		terminalID := hostToolString(args, "terminal_id")
		if err := s.authorizeHostRead(ctx, hostToolReadTerminal, terminalID); err != nil {
			return "", err
		}
		request := map[string]any{"project_path_key": key, "max_bytes": hostReadTerminalMaxBytes(args["max_bytes"])}
		if terminalID != "" {
			request["session_id"] = terminalID
		}
		var result hostTerminalReadTail
		if err := s.hostBridge.call(ctx, hostToolReadTerminal, "terminal_read_tail", request, &result); err != nil {
			return "", err
		}
		return formatHostTerminalRead(result), nil
	}}
}

type hostTunnelHealth struct {
	Status     string  `json:"status"`
	HTTPStatus float64 `json:"httpStatus"`
	Error      string  `json:"error"`
	CheckedAt  float64 `json:"checkedAt"`
	RTTMs      float64 `json:"rttMs"`
}

type hostTunnel struct {
	ID                string            `json:"id"`
	Slug              string            `json:"slug"`
	Name              string            `json:"name"`
	TargetURL         string            `json:"targetUrl"`
	PublicPath        string            `json:"publicPath"`
	CreatedAt         float64           `json:"createdAt"`
	ExpiresAt         float64           `json:"expiresAt"`
	ActiveConnections float64           `json:"activeConnections"`
	ProjectPathKey    string            `json:"projectPathKey"`
	Local             *hostTunnelHealth `json:"local"`
}

type hostTunnelState struct {
	Revision           float64           `json:"revision"`
	AgentOnline        bool              `json:"agentOnline"`
	Relay              *hostTunnelHealth `json:"relay"`
	Tunnels            []hostTunnel      `json:"tunnels"`
	GatewayUnsupported *bool             `json:"gatewayUnsupported"`
	// PublicBaseURL is not part of the bridge contract; a desktop that adds it to
	// the snapshot gets full public URLs instead of publicPath.
	PublicBaseURL string `json:"publicBaseUrl"`
}

func formatTunnelHealth(health *hostTunnelHealth) string {
	if health == nil || health.Status == "unknown" {
		return "unknown"
	}
	parts := []string{health.Status}
	if health.HTTPStatus > 0 {
		parts = append(parts, "HTTP "+formatJSNumber(health.HTTPStatus))
	}
	if health.Status == "ok" && health.RTTMs > 0 {
		parts = append(parts, formatJSNumber(health.RTTMs)+"ms")
	}
	if health.Status == "failed" && health.Error != "" {
		parts = append(parts, health.Error)
	}
	return strings.Join(parts, " ")
}

func formatJSNumber(n float64) string {
	return strconv.FormatFloat(n, 'f', -1, 64)
}

func formatTunnelRemaining(expiresAt float64, now time.Time) string {
	if expiresAt == 0 {
		return "unlimited"
	}
	seconds := math.Max(0, math.Floor(expiresAt-float64(now.UnixMilli())/1000))
	if seconds <= 0 {
		return "expired"
	}
	hours := math.Floor(seconds / 3600)
	minutes := math.Ceil(math.Mod(seconds, 3600) / 60)
	if hours <= 0 {
		return formatJSNumber(minutes) + "m"
	}
	if minutes > 0 && minutes < 60 {
		return formatJSNumber(hours) + "h " + formatJSNumber(minutes) + "m"
	}
	return formatJSNumber(hours) + "h"
}

func composeTunnelPublicURL(baseURL, publicPath string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	path := strings.TrimSpace(publicPath)
	if base == "" || path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

func formatTunnelSnapshotHealth(snapshot hostTunnelState) []string {
	link := "offline"
	if snapshot.AgentOnline {
		link = "online"
	}
	lines := []string{"link: " + link, "relay: " + formatTunnelHealth(snapshot.Relay)}
	if snapshot.GatewayUnsupported != nil && *snapshot.GatewayUnsupported {
		lines = append(lines, "gateway: connected gateway does not support tunnels (no public URLs)")
	}
	return lines
}

func formatTunnelLine(tunnel hostTunnel, publicBaseURL string, now time.Time) string {
	name := strings.TrimSpace(tunnel.Name)
	if name == "" {
		name = tunnel.TargetURL
	}
	lines := []string{"- " + name, "  id: " + tunnel.ID, "  target: " + tunnel.TargetURL}
	if publicURL := composeTunnelPublicURL(publicBaseURL, tunnel.PublicPath); publicURL != "" {
		lines = append(lines, "  public: "+publicURL)
	} else if tunnel.PublicPath != "" {
		lines = append(lines, "  publicPath: "+tunnel.PublicPath)
	}
	lines = append(lines, "  service: "+formatTunnelHealth(tunnel.Local), "  ttl: "+formatTunnelRemaining(tunnel.ExpiresAt, now))
	return strings.Join(lines, "\n")
}

func tunnelTTL(value any) (int, error) {
	if value == nil {
		return 3600, nil
	}
	if n, ok := value.(float64); ok {
		switch n {
		case 0, 900, 3600, 14400:
			return int(n), nil
		}
	}
	return 0, errors.New("TunnelManager.ttlSeconds must be 0, 900, 3600, or 14400.")
}

func (s *Server) tunnelManagerTool(ag *agent.Agent) tools.Tool {
	return tools.Tool{Def: ai.NewTool(hostToolTunnelManager, tunnelManagerDescription, tunnelManagerSchema), Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		text, err := s.runTunnelManager(ctx, hostToolArgs(raw))
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			var denied hostToolDenied
			if errors.As(err, &denied) || errors.Is(err, errHostBridgeNotConnected) {
				return "", err
			}
			return "", errors.New("TunnelManager failed: " + err.Error())
		}
		return text, nil
	}}
}

func (s *Server) tunnelState(ctx context.Context) (hostTunnelState, error) {
	var snapshot hostTunnelState
	err := s.hostBridge.call(ctx, hostToolTunnelManager, "gateway_tunnel_state", struct{}{}, &snapshot)
	return snapshot, err
}

func (s *Server) runTunnelManager(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	switch action {
	case "list":
		if err := s.authorizeHostRead(ctx, hostToolTunnelManager, "list"); err != nil {
			return "", err
		}
		snapshot, err := s.tunnelState(ctx)
		if err != nil {
			return "", err
		}
		now := time.Now()
		var lines []string
		if len(snapshot.Tunnels) == 0 {
			lines = append([]string{"No Remote tunnels are currently registered."}, formatTunnelSnapshotHealth(snapshot)...)
		} else {
			lines = append([]string{"Remote tunnels:"}, formatTunnelSnapshotHealth(snapshot)...)
			for _, tunnel := range snapshot.Tunnels {
				lines = append(lines, formatTunnelLine(tunnel, snapshot.PublicBaseURL, now))
			}
		}
		return strings.Join(lines, "\n"), nil
	case "create":
		targetURL := hostToolString(args, "targetUrl")
		if targetURL == "" {
			return "", errors.New("TunnelManager.targetUrl is required for action=create.")
		}
		ttl, err := tunnelTTL(args["ttlSeconds"])
		if err != nil {
			return "", err
		}
		// Publishing a local service on the internet asks by default.
		if err := s.authorizeHostTool(ctx, hostToolTunnelManager, "create "+targetURL); err != nil {
			return "", err
		}
		input := map[string]any{"targetUrl": targetURL, "ttlSeconds": ttl}
		if name := hostToolString(args, "name"); name != "" {
			input["name"] = name
		}
		known := map[string]bool{}
		if before, err := s.tunnelState(ctx); err == nil {
			for _, tunnel := range before.Tunnels {
				known[tunnel.ID] = true
			}
		} else if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err := s.hostBridge.call(ctx, hostToolTunnelManager, "gateway_tunnel_create", map[string]any{"input": input}, nil); err != nil {
			return "", err
		}
		snapshot, err := s.tunnelState(ctx)
		if err != nil {
			return "", err
		}
		var created []hostTunnel
		for _, tunnel := range snapshot.Tunnels {
			if !known[tunnel.ID] {
				created = append(created, tunnel)
			}
		}
		sort.SliceStable(created, func(i, j int) bool { return created[i].CreatedAt > created[j].CreatedAt })
		var tunnel *hostTunnel
		for i := range created {
			if created[i].TargetURL == targetURL {
				tunnel = &created[i]
				break
			}
		}
		if tunnel == nil && len(created) > 0 {
			tunnel = &created[0]
		}
		lines := []string{"Created Remote tunnel:"}
		if tunnel != nil {
			lines = append(lines, formatTunnelLine(*tunnel, snapshot.PublicBaseURL, time.Now()))
		}
		return strings.Join(append(lines, formatTunnelSnapshotHealth(snapshot)...), "\n"), nil
	case "close":
		id := hostToolString(args, "id")
		if id == "" {
			return "", errors.New("TunnelManager.id is required for action=close.")
		}
		if err := s.authorizeHostTool(ctx, hostToolTunnelManager, "close "+id); err != nil {
			return "", err
		}
		if err := s.hostBridge.call(ctx, hostToolTunnelManager, "gateway_tunnel_close", map[string]any{"tunnel_id": id}, nil); err != nil {
			return "", err
		}
		return "Closed Remote tunnel " + id + ".", nil
	case "check":
		id := hostToolString(args, "id")
		if err := s.authorizeHostRead(ctx, hostToolTunnelManager, strings.TrimSpace("check "+id)); err != nil {
			return "", err
		}
		request := map[string]any{}
		if id != "" {
			request["tunnel_id"] = id
		}
		if err := s.hostBridge.call(ctx, hostToolTunnelManager, "gateway_tunnel_check", request, nil); err != nil {
			return "", err
		}
		// Probes run asynchronously; wait briefly before sampling the state.
		timer := time.NewTimer(tunnelCheckSettle)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		}
		snapshot, err := s.tunnelState(ctx)
		if err != nil {
			return "", err
		}
		checked := snapshot.Tunnels
		if id != "" {
			checked = nil
			for _, tunnel := range snapshot.Tunnels {
				if tunnel.ID == id {
					checked = append(checked, tunnel)
				}
			}
			if len(checked) == 0 {
				return "", fmt.Errorf("No tunnel found for id %q.", id)
			}
		}
		lines := append([]string{"Tunnel health (sampled ~2.5s after triggering checks; probes are async and may still be settling):"}, formatTunnelSnapshotHealth(snapshot)...)
		now := time.Now()
		for _, tunnel := range checked {
			lines = append(lines, formatTunnelLine(tunnel, snapshot.PublicBaseURL, now))
		}
		return strings.Join(lines, "\n"), nil
	}
	return "", errors.New(`TunnelManager.action must be "list", "create", "close", or "check".`)
}

// hostToolDenied is a gate refusal, reported as is rather than as a tool failure.
type hostToolDenied struct{ err error }

func (d hostToolDenied) Error() string { return d.err.Error() }

// authorizeHostTool gates a host tool action. The bridge is checked first so the
// user is never asked about an action that cannot run.
func (s *Server) authorizeHostTool(ctx context.Context, tool, command string) error {
	if !s.hostBridge.provides(tool) {
		return errHostBridgeNotConnected
	}
	if err := tools.Authorize(ctx, tool, command); err != nil {
		return hostToolDenied{err}
	}
	return nil
}

// authorizeHostRead gates a host read (ReadTerminal, TunnelManager list/check) as
// read-only: it runs without a prompt, like Read, unless a deny or ask rule, a
// per-tool policy, plan mode or an AI review finding says otherwise. command is what
// rules match, e.g. TunnelManager(list) or ReadTerminal(term-1).
func (s *Server) authorizeHostRead(ctx context.Context, tool, command string) error {
	if !s.hostBridge.provides(tool) {
		return errHostBridgeNotConnected
	}
	if err := tools.AuthorizePoll(ctx, tool, command); err != nil {
		return hostToolDenied{err}
	}
	return nil
}

// liveAgentProjectPathKey is the desktop's project_path_key for a workspace path
// (LiveAgent runtime/project_path.rs): Windows drive, UNC and \\?\ paths use
// forward slashes, lower case and no trailing separator past the root; other
// paths keep their case and lose trailing slashes.
func liveAgentProjectPathKey(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if hasWindowsExtendedPrefix(trimmed) || hasWindowsDrivePrefix(trimmed) || hasWindowsUNCPrefix(trimmed) {
		stripped := trimmed
		switch {
		case hasWindowsExtendedUNCPrefix(trimmed):
			stripped = "//" + trimmed[8:]
		case hasWindowsExtendedPrefix(trimmed):
			stripped = trimmed[4:]
		}
		normalized := strings.ReplaceAll(stripped, `\`, "/")
		minLen := windowsProjectRootLen(normalized)
		for len(normalized) > minLen && strings.HasSuffix(normalized, "/") {
			normalized = normalized[:len(normalized)-1]
		}
		return strings.ToLower(normalized)
	}
	for len(trimmed) > 1 && strings.HasSuffix(trimmed, "/") {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return trimmed
}

func isPathSeparatorByte(b byte) bool { return b == '\\' || b == '/' }

func isASCIIAlpha(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }

func hasWindowsDrivePrefix(value string) bool {
	return len(value) >= 2 && isASCIIAlpha(value[0]) && value[1] == ':' && (len(value) == 2 || isPathSeparatorByte(value[2]))
}

func hasWindowsUNCPrefix(value string) bool {
	if len(value) < 2 || !isPathSeparatorByte(value[0]) || !isPathSeparatorByte(value[1]) {
		return false
	}
	rest := value[2:]
	end := strings.IndexAny(rest, `\/`)
	if end <= 0 {
		return false // no server, or no share after it
	}
	share := rest[end+1:]
	if next := strings.IndexAny(share, `\/`); next >= 0 {
		share = share[:next]
	}
	return share != ""
}

func hasWindowsExtendedPrefix(value string) bool {
	return len(value) >= 4 && isPathSeparatorByte(value[0]) && isPathSeparatorByte(value[1]) && value[2] == '?' && isPathSeparatorByte(value[3])
}

func hasWindowsExtendedUNCPrefix(value string) bool {
	return len(value) >= 8 && hasWindowsExtendedPrefix(value) && strings.EqualFold(value[4:7], "UNC") && isPathSeparatorByte(value[7])
}

func windowsProjectRootLen(value string) int {
	if len(value) >= 3 && isASCIIAlpha(value[0]) && value[1] == ':' && value[2] == '/' {
		return 3
	}
	if rest, ok := strings.CutPrefix(value, "//"); ok {
		parts := strings.SplitN(rest, "/", 3)
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			return 2
		}
		return 2 + len(parts[0]) + 1 + len(parts[1])
	}
	return 1
}
