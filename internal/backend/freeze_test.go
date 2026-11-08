package backend

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// within fails the test when fn does not return in d. fn runs on its own
// goroutine so a hang shows up as a failure, not as a stuck test binary.
func within(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	start := time.Now()
	select {
	case <-done:
		t.Logf("%s took %v", what, time.Since(start).Round(time.Millisecond))
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v", what, d)
	}
}

func getStatus(t *testing.T, url string) int {
	t.Helper()
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Errorf("GET %s: %v", url, err)
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

// A session whose lock is held (a run start switching its model holds rt.mu
// and then takes s.mu) must not let /v1/sessions/exists park on that rt.mu
// while holding s.mu: that closed the lock cycle and stopped every request.
func TestSessionsExistDoesNotHoldTheServerLockWhileWaitingForASession(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backendServer, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	created := createTestSession(t, server.URL)
	if err := store.Save(created.ID, 0, []ai.Message{{Role: "user", Content: "hi"}}, "fixture-model", "fixture"); err != nil {
		t.Fatal(err)
	}
	rt, err := backendServer.loadRuntimeByID(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer backendServer.releaseRuntime(rt)

	rt.mu.Lock()
	held := true
	// On failure, free the session first so the cleanup does not hang on
	// the very cycle the test detected.
	defer func() {
		if held {
			rt.mu.Unlock()
		}
	}()
	existsDone := make(chan struct{})
	go func() {
		defer close(existsDone)
		doJSON(t, http.MethodPost, server.URL+"/v1/sessions/exists", map[string]any{"ids": []string{created.ID}}, nil)
	}()
	// Give the request time to reach the session lock.
	time.Sleep(300 * time.Millisecond)
	within(t, 5*time.Second, "taking the server lock while /v1/sessions/exists waits for a busy session", func() {
		backendServer.mu.Lock()
		//nolint:staticcheck // the lock is only probed
		backendServer.mu.Unlock()
	})
	held = false
	rt.mu.Unlock()
	within(t, 5*time.Second, "/v1/sessions/exists after the session is free", func() { <-existsDone })
}

// A client that requests a large reply and never reads it must not keep the
// session locked: the reply used to be written while rt.mu was held, so the
// session (and through it the backend) waited on that client's socket.
func TestUnreadReplyDoesNotKeepTheSessionLocked(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	created := createTestSession(t, server.URL)
	big := strings.Repeat("x", 4<<20)
	messages := []ai.Message{{Role: "user", Content: big}, {Role: "assistant", Content: "ok"}}
	if err := store.Save(created.ID, 0, messages, "fixture-model", "fixture"); err != nil {
		t.Fatal(err)
	}

	addr := strings.TrimPrefix(server.URL, "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(4 << 10)
	}
	fmt.Fprintf(conn, "GET /v1/sessions/%s HTTP/1.1\r\nHost: %s\r\n\r\n", created.ID, addr)
	// Read only the status line, then stop reading.
	if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)

	within(t, 10*time.Second, "a second request to the session whose reply is not being read", func() {
		if code := getStatus(t, server.URL+"/v1/sessions/"+created.ID+"/history?limit=1"); code != http.StatusOK {
			t.Errorf("history status %d", code)
		}
	})
}

// An MCP server that accepts the connection and never answers initialize
// must not hold up health, the session list, or opening a session.
func TestWedgedMCPServerDoesNotBlockTheBackend(t *testing.T) {
	release := make(chan struct{})
	wedged := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer wedged.Close()
	defer close(release)

	root := t.TempDir()
	live, err := mcp.NewLiveManager(context.Background(), filepath.Join(root, "mcp.json"), mcp.LiveSettings{Servers: []mcp.LiveServer{{ID: "wedged", Enabled: true, Transport: "http", URL: wedged.URL, TimeoutMs: 600000}}})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()

	store, err := session.Open(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &scriptedClient{response: "ok"}
	backendServer, err := New(Options{
		Store:    store,
		EventDir: filepath.Join(root, "events"),
		MCP:      live,
		Factory: func(_ context.Context, _ string, selected protocol.ModelRef) (*agent.Agent, error) {
			ag := agent.New(client, selected.Model, 1024, "system")
			ag.ModelName, ag.Provider = selected.Model, selected.Provider
			return ag, nil
		},
		Models:     []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}},
		DefaultCWD: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(backendServer)
	defer func() {
		server.Close()
		_ = backendServer.Close()
	}()

	within(t, 10*time.Second, "/v1/health with a wedged MCP server", func() {
		if code := getStatus(t, server.URL+"/v1/health"); code != http.StatusOK {
			t.Errorf("health status %d", code)
		}
	})
	var created protocol.Session
	within(t, 10*time.Second, "creating a session with a wedged MCP server", func() {
		created = createTestSession(t, server.URL)
	})
	within(t, 10*time.Second, "listing sessions with a wedged MCP server", func() {
		if code := getStatus(t, server.URL+"/v1/sessions"); code != http.StatusOK {
			t.Errorf("sessions status %d", code)
		}
	})
	within(t, 10*time.Second, "reading a session with a wedged MCP server", func() {
		if code := getStatus(t, server.URL+"/v1/sessions/"+created.ID); code != http.StatusOK {
			t.Errorf("session status %d", code)
		}
	})
	within(t, 10*time.Second, "MCP status with a wedged MCP server", func() {
		_ = live.Statuses()
	})
}
