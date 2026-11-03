package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func profileDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var rel []string
	switch runtime.GOOS {
	case "darwin":
		rel = []string{
			"Library/Application Support/Google/Chrome",
			"Library/Application Support/Google/Chrome Canary",
			"Library/Application Support/Comet",
			"Library/Application Support/Arc/User Data",
			"Library/Application Support/Dia/User Data",
			"Library/Application Support/Microsoft Edge",
			"Library/Application Support/Microsoft Edge Beta",
			"Library/Application Support/Microsoft Edge Dev",
			"Library/Application Support/Microsoft Edge Canary",
			"Library/Application Support/BraveSoftware/Brave-Browser",
		}
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		rel = []string{
			"Google/Chrome/User Data",
			"Google/Chrome SxS/User Data",
			"Google/Chrome Beta/User Data",
			"Google/Chrome Dev/User Data",
			"Chromium/User Data",
			"Microsoft/Edge/User Data",
			"Microsoft/Edge Beta/User Data",
			"Microsoft/Edge Dev/User Data",
			"Microsoft/Edge SxS/User Data",
			"BraveSoftware/Brave-Browser/User Data",
		}
		var out []string
		for _, r := range rel {
			out = append(out, filepath.Join(local, filepath.FromSlash(r)))
		}
		return out
	default:
		rel = []string{
			".config/google-chrome",
			".config/chromium",
			".config/chromium-browser",
			".config/microsoft-edge",
			".config/microsoft-edge-beta",
			".config/microsoft-edge-dev",
			".var/app/org.chromium.Chromium/config/chromium",
			".var/app/com.google.Chrome/config/google-chrome",
			".var/app/com.brave.Browser/config/BraveSoftware/Brave-Browser",
			".var/app/com.microsoft.Edge/config/microsoft-edge",
		}
	}
	var out []string
	for _, r := range rel {
		out = append(out, filepath.Join(home, filepath.FromSlash(r)))
	}
	return out
}

func parseDevToolsActivePort(data []byte) (port int, wsPath string, err error) {
	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 {
		return 0, "", fmt.Errorf("DevToolsActivePort: want 2 lines, got %d", len(lines))
	}
	port, err = strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return 0, "", fmt.Errorf("DevToolsActivePort port: %w", err)
	}
	wsPath = strings.TrimSpace(lines[1])
	if wsPath == "" {
		return 0, "", errors.New("DevToolsActivePort: empty ws path")
	}
	return port, wsPath, nil
}

func browserRunningForProfile(base string) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	target, err := os.Readlink(filepath.Join(base, "SingletonLock"))
	if err != nil {
		return false
	}
	pidStr := target[strings.LastIndex(target, "-")+1:]
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	return proc.Signal(os.Signal(sigzero())) == nil
}

func portLive(port int) bool {
	for _, host := range []string{"127.0.0.1", "::1"} {
		if hostPortLive(host, port) {
			return true
		}
	}
	return false
}

func chromeWSURL(ctx context.Context, base string) (string, error) {

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/json/version", nil)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("%w: Chrome is reachable, but the per-session 'Allow remote debugging' popup has not been accepted — click Allow in Chrome, then retry", ErrPermissionBlocked)
	}
	if resp.StatusCode != http.StatusOK {
		return "", nil
	}
	var v struct {
		Browser              string `json:"Browser"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v); err != nil {
		return "", nil
	}

	family := []string{"Chrom", "Edge", "Brave"}
	recognised := false
	for _, f := range family {
		if strings.Contains(v.Browser, f) {
			recognised = true
			break
		}
	}
	if !recognised {
		return "", nil
	}
	return v.WebSocketDebuggerURL, nil
}

func resolveWSURL(ctx context.Context, scheme, host string, port int, wsPath string, awaitApproval bool) (string, error) {
	if scheme == "" {
		scheme = "http"
	}
	wsScheme := "ws"
	if scheme == "https" {
		wsScheme = "wss"
	}
	var hosts []string
	if host == "::1" {
		hosts = []string{"::1"}
	} else if host != "" {
		hosts = []string{host}
	} else {
		hosts = []string{"127.0.0.1", "::1"}
	}
	var permErr error
	for _, h := range hosts {
		base := scheme + "://" + net.JoinHostPort(h, strconv.Itoa(port))
		ws, err := chromeWSURL(ctx, base)
		if err != nil {
			if errors.Is(err, ErrPermissionBlocked) {
				permErr = err
			}
			continue
		}
		if ws != "" {
			return ws, nil
		}
	}
	// A DevToolsActivePort file names the browser websocket directly. Chrome's
	// chrome://inspect remote-debugging switch serves no /json endpoints and
	// holds the websocket handshake open until the user clicks Allow, so for
	// the user's own browser (awaitApproval) a short probe would always time
	// out and trigger a second prompt. Hand the candidate to connectLive
	// instead: it waits for the approval and rejects non-browser endpoints.
	// k-brain's own dedicated Chrome never prompts, so it is still probed.
	if wsPath != "" && awaitApproval {
		for _, h := range hosts {
			if hostPortLive(h, port) {
				return wsScheme + "://" + net.JoinHostPort(h, strconv.Itoa(port)) + wsPath, nil
			}
		}
	}
	if permErr != nil {
		return "", permErr
	}
	if wsPath != "" {
		for _, h := range hosts {
			if wsUpgradeAnswers(ctx, scheme, h, port, wsPath) {
				return wsScheme + "://" + net.JoinHostPort(h, strconv.Itoa(port)) + wsPath, nil
			}
		}
	}
	return "", fmt.Errorf("%s: no debuggable Chromium endpoint", net.JoinHostPort(hosts[0], strconv.Itoa(port)))
}

func wsUpgradeAnswers(ctx context.Context, scheme, host string, port int, path string) bool {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		scheme+"://"+net.JoinHostPort(host, strconv.Itoa(port))+path, nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusSwitchingProtocols
}

func hostPortLive(host string, port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// DiscoverLiveWS returns the first live-browser websocket candidate.
func DiscoverLiveWS(ctx context.Context) (string, error) {
	candidates, err := discoverLiveCandidates(ctx)
	if err != nil {
		return "", err
	}
	return candidates[0], nil
}

// discoverLiveCandidates lists every websocket endpoint that may belong to the
// user's running browser, in preference order. Candidates taken from a
// DevToolsActivePort file are not verified here; connectLive does that.
func discoverLiveCandidates(ctx context.Context) ([]string, error) {
	ws := os.Getenv("K_BRAIN_CDP_WS")
	if ws != "" {
		return []string{ws}, nil
	}
	httpURL := os.Getenv("K_BRAIN_CDP_URL")
	if httpURL != "" {
		scheme, host, port := splitEndpoint(httpURL)
		ws, err := resolveWSURL(ctx, scheme, host, port, "", true)
		if err != nil {
			return nil, err
		}
		return []string{ws}, nil
	}
	var candidates []string
	seen := map[string]bool{}
	add := func(ws string) {
		if ws != "" && !seen[ws] {
			seen[ws] = true
			candidates = append(candidates, ws)
		}
	}
	var sawStaleFile bool
	var permErr error
	for _, base := range profileDirs() {
		ws, stale, err := discoverProfileWS(ctx, base, true)
		if err == nil {
			add(ws)
			continue
		}
		if stale {
			sawStaleFile = true
		}
		if errors.Is(err, ErrPermissionBlocked) && permErr == nil {
			permErr = err
		}
	}

	for _, port := range []int{9222, 9223} {
		if !portLive(port) {
			continue
		}
		ws, err := resolveWSURL(ctx, "", "", port, "", true)
		if err == nil {
			add(ws)
			continue
		}
		if errors.Is(err, ErrPermissionBlocked) && permErr == nil {
			permErr = err
		}
	}
	if len(candidates) > 0 {
		return candidates, nil
	}
	if permErr != nil {
		return nil, permErr
	}
	if sawStaleFile {
		return nil, fmt.Errorf("%w: a closed browser left a stale DevToolsActivePort file — reopen Chrome with remote debugging enabled (chrome://inspect/#remote-debugging), or run in dedicated/headless mode", ErrNoLiveBrowser)
	}
	return nil, fmt.Errorf("%w: no supported Chromium-family browser with remote debugging is running — enable chrome://inspect/#remote-debugging in Chrome, start Chrome with --remote-debugging-port=9222, or use dedicated/headless mode", ErrNoLiveBrowser)
}

func DiscoverWSForProfile(ctx context.Context, base string) (ws string, ok bool) {
	ws, stale, err := discoverProfileWS(ctx, base, false)
	return ws, err == nil && !stale
}

func discoverProfileWS(ctx context.Context, base string, awaitApproval bool) (ws string, stale bool, err error) {
	data, err := os.ReadFile(filepath.Join(base, "DevToolsActivePort"))
	if err != nil {
		return "", false, err
	}
	port, wsPath, err := parseDevToolsActivePort(data)
	if err != nil {
		return "", false, err
	}
	if !portLive(port) {
		return "", true, fmt.Errorf("stale DevToolsActivePort in %s", base)
	}
	if !browserRunningForProfile(base) {
		return "", false, fmt.Errorf("no live process holds %s", base)
	}
	ws, err = resolveWSURL(ctx, "", "", port, wsPath, awaitApproval)
	return ws, false, err
}

func splitHostPort(httpURL string) (string, int) {
	u := strings.TrimPrefix(strings.TrimPrefix(httpURL, "http://"), "https://")
	host, portStr, err := net.SplitHostPort(strings.TrimSuffix(u, "/"))
	if err != nil {
		return u, 80
	}
	port, _ := strconv.Atoi(portStr)
	return host, port
}

func splitEndpoint(rawurl string) (scheme, host string, port int) {
	scheme = "http"
	if strings.HasPrefix(rawurl, "https://") {
		scheme = "https"
	}
	if strings.HasPrefix(rawurl, "wss://") {
		scheme = "https"
	}
	host, port = splitHostPort(rawurl)
	return scheme, host, port
}
