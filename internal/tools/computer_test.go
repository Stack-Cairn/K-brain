package tools

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/computer"
)

func TestComputerExecGates(t *testing.T) {
	oldP, oldA := ComputerPolicy, ComputerApprover
	defer func() { ComputerPolicy, ComputerApprover = oldP, oldA }()
	ComputerPolicy, ComputerApprover = nil, nil

	out := Execute(t.Context(), []Tool{ComputerExec()}, "computer_exec", []byte(`{"code":"print(chrome_state())"}`))
	if !strings.HasPrefix(out, "Error") {
		t.Fatalf("want error, got %q", out[:80])
	}
}

func TestGateApp(t *testing.T) {
	oldP, oldA := ComputerPolicy, ComputerApprover
	defer func() { ComputerPolicy, ComputerApprover = oldP, oldA }()

	ComputerPolicy = computer.NewPolicy([]string{"Google Chrome"}, []string{"Finder"}, true)
	ComputerApprover = nil

	if err := gateApp("Google Chrome"); err != nil {
		t.Errorf("allowed app blocked: %v", err)
	}
	if err := gateApp("Finder"); err == nil || !strings.Contains(err.Error(), "policy") {
		t.Errorf("denied app must fail: %v", err)
	}
	err := gateApp("Safari")
	if err == nil {
		t.Fatal("unlisted app must need approval")
	}
	ComputerApprover = func(app string) bool { return app == "Safari" }
	if err := gateApp("Safari"); err != nil {
		t.Errorf("approver-consent must unblock: %v", err)
	}

	ComputerApprover = nil
	if err := gateApp("Safari"); err != nil {
		t.Errorf("approval must persist for the session: %v", err)
	}
}

func TestIsStale(t *testing.T) {
	if !IsStale(&computer.StaleError{Msg: "state changed"}) {
		t.Error("StaleError must be stale")
	}
	if IsStale(errors.New("other")) || IsStale(nil) {
		t.Error("non-stale errors must not be stale")
	}
}

func TestShorten(t *testing.T) {
	if got := shorten("short", 10); got != "short" {
		t.Errorf("shorten under limit: %q", got)
	}
	if got := shorten("abcdefghij", 5); got != "abcd…" {
		t.Errorf("shorten over limit: %q", got)
	}
}

func TestGenerations(t *testing.T) {
	noteGeneration("GenApp", &computer.AppState{Generation: 7})
	if got := genFor("genapp"); got != 7 {
		t.Errorf("genFor: %d", got)
	}
	noteGeneration("GenApp", nil)
	if got := genFor("GenApp"); got != 7 {
		t.Errorf("genFor after nil note: %d", got)
	}
	if got := genFor("NeverSeen"); got != 0 {
		t.Errorf("unknown app gen: %d", got)
	}
}

func TestSummarize(t *testing.T) {
	st := &computer.AppState{
		Generation: 3,
		App:        "TextEdit",
		Elements: []computer.AXElement{{
			Index: 0, Role: "AXButton", Title: "OK", Value: "v", Desc: "d",
			Position: []float64{10, 20}, Size: []float64{30, 40}, Focused: true,
		}},
		Screenshot: &computer.Screenshot{Bytes: 123},
	}
	out := summarize(st)
	for _, want := range []string{
		"app=TextEdit generation=3 elements=1",
		"screenshot: 123 bytes jpeg attached",
		`[0] AXButton title="OK" value="v" desc="d" at(10,20 30x40) focused`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summarize missing %q in:\n%s", want, out)
		}
	}
	st.Screenshot = &computer.Screenshot{Err: "no grant"}
	if out := summarize(st); !strings.Contains(out, "screenshot: unavailable (no grant)") {
		t.Errorf("summarize screenshot error: %s", out)
	}
}

func withComputerPolicy(t *testing.T, p *computer.Policy) {
	t.Helper()
	oldP, oldA := ComputerPolicy, ComputerApprover
	t.Cleanup(func() { ComputerPolicy, ComputerApprover = oldP, oldA })
	ComputerPolicy, ComputerApprover = p, nil
}

func TestRunComputerCodePortablePaths(t *testing.T) {
	withComputerPolicy(t, computer.NewPolicy([]string{"Google Chrome"}, []string{"Finder"}, true))
	ctx := t.Context()

	if out, err := runComputerCode(ctx, "# label\nprint(\"hi\")"); err != nil || out != "hi\n" {
		t.Errorf("print: %q %v", out, err)
	}
	if out, err := runComputerCode(ctx, `print(42)`); err != nil || out != "42\n" {
		t.Errorf("print number: %q %v", out, err)
	}
	if _, err := runComputerCode(ctx, `frobnicate("x")`); err == nil || !strings.Contains(err.Error(), "unknown helper") {
		t.Errorf("unknown helper: %v", err)
	}
	if _, err := runComputerCode(ctx, `goto("x" `); err == nil {
		t.Error("parse error must surface")
	}

	if _, err := runComputerCode(ctx, `tell("Finder", "activate")`); err == nil || !strings.Contains(err.Error(), "policy") {
		t.Errorf("denied app: %v", err)
	}

	if _, err := runComputerCode(ctx, `chrome_goto("http://169.254.169.254/latest")`); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("ssrf block: %v", err)
	}

	if _, err := runComputerCode(ctx, `chrome_js()`); err == nil || !strings.Contains(err.Error(), "missing arg") {
		t.Errorf("missing arg: %v", err)
	}
	if _, err := runComputerCode(ctx, `click("Google Chrome")`); err == nil || !strings.Contains(err.Error(), "click needs") {
		t.Errorf("click arity: %v", err)
	}
	if _, err := runComputerCode(ctx, `chrome_activate("w", 1)`); err == nil || !strings.Contains(err.Error(), "must be a number") {
		t.Errorf("argNum type check: %v", err)
	}
}

func TestRunComputerCodeOsascriptTierUnsupported(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin: would drive the real Chrome")
	}
	withComputerPolicy(t, computer.NewPolicy([]string{"Google Chrome"}, nil, true))
	ctx := t.Context()
	for _, code := range []string{
		`chrome_state()`, `chrome_tabs()`, `chrome_back()`, `chrome_reload()`,
		`chrome_js("1+1")`, `chrome_find("example")`,
		`chrome_activate(1, 2)`, `chrome_close(1, 2)`,
		`chrome_new_tab("http://93.184.216.34/")`, `chrome_goto("http://93.184.216.34/")`,
		`tell("Google Chrome", "activate")`,
	} {
		if _, err := runComputerCode(ctx, code); err == nil || !errors.Is(err, computer.ErrUnsupportedPlatform) {
			t.Errorf("%s: want ErrUnsupportedPlatform, got %v", code, err)
		}
	}
}

func TestHelperUnavailable(t *testing.T) {
	t.Setenv("K_BRAIN_COMPUTER_BIN", filepath.Join(t.TempDir(), "missing-helper"))
	computer.ResetShared()
	t.Cleanup(computer.ResetShared)
	if _, err := helper(); err == nil || !strings.Contains(err.Error(), "k-brain-computer driver") {
		t.Errorf("helper: %v", err)
	}
	withComputerPolicy(t, computer.NewPolicy([]string{"TestApp"}, nil, true))
	if _, err := runComputerCode(t.Context(), `apps()`); err == nil || !strings.Contains(err.Error(), "k-brain-computer driver") {
		t.Errorf("apps without helper: %v", err)
	}
}

func fakeNativeHelper(t *testing.T) {
	t.Helper()
	const src = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func reply(enc *json.Encoder, id, result any) {
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func main() {
	fmt.Println("k-brain-computer/1")
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	enc := json.NewEncoder(os.Stdout)
	state := map[string]any{
		"generation": 2, "app": "TestApp",
		"elements": []map[string]any{{"index": 0, "role": "AXButton", "title": "OK", "enabled": true}},
		"screenshot": map[string]any{"jpegBase64": "aGVsbG8=", "bytes": 5},
	}
	for sc.Scan() {
		var req map[string]any
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		id := req["id"]
		params, _ := req["params"].(map[string]any)
		switch req["method"] {
		case "handshake":
			reply(enc, id, map[string]any{"version": "k-brain-computer/1"})
		case "apps":
			reply(enc, id, []map[string]any{{"name": "Finder", "bundleId": "com.apple.finder", "pid": 1, "active": true}})
		case "permissions.request":
			reply(enc, id, map[string]any{"accessibility": true, "screenRecording": true})
		case "state", "ax":
			reply(enc, id, state)
		case "click":
			if g, _ := params["gen"].(float64); g != 2 {
				_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": 4, "message": "state changed since generation"}})
				continue
			}
			next := map[string]any{}
			for k, v := range state {
				next[k] = v
			}
			next["generation"] = 3
			reply(enc, id, next)
		case "type":
			reply(enc, id, map[string]any{"action": "typed", "stateUnavailable": "no AX grant", "hint": "grant it"})
		case "press", "scroll", "set", "select", "menu":
			if k, _ := params["key"].(string); k == "BADJSON" {
				reply(enc, id, "not a state object")
				continue
			}
			var rows []map[string]any
			i := 0
			for k, v := range params {
				rows = append(rows, map[string]any{"index": i, "role": "AXParam", "title": k, "value": fmt.Sprint(v)})
				i++
			}
			reply(enc, id, map[string]any{"generation": 2, "app": "TestApp", "elements": rows})
		case "screenshot":
			reply(enc, id, map[string]any{"jpegBase64": "aGVsbG8=", "bytes": 5})
		default:
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "unknown method"}})
		}
	}
}
`
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-computer"+func() string {
		if runtime.GOOS == "windows" {
			return ".exe"
		}
		return ""
	}())
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fakecomputer\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	goBin := "go"
	if out, err := exec.CommandContext(t.Context(), "go", "env", "GOROOT").Output(); err == nil {
		if cand := filepath.Join(strings.TrimSpace(string(out)), "bin", "go"); fileExists(cand) {
			goBin = cand
		}
	}
	cmd := exec.CommandContext(t.Context(), goBin, "build", "-o", bin, ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake helper: %v\n%s", err, out)
	}
	t.Setenv("K_BRAIN_COMPUTER_BIN", bin)
	computer.ResetShared()
	t.Cleanup(computer.ResetShared)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestNativeTierWithFakeHelper(t *testing.T) {
	fakeNativeHelper(t)
	withComputerPolicy(t, computer.NewPolicy([]string{"TestApp"}, nil, true))
	shots := &attachments{enabled: true}
	defer shots.close()
	ctx := context.WithValue(t.Context(), attachmentKey{}, shots)

	out, err := runComputerCode(ctx, `print(apps())`)
	if err != nil || !strings.Contains(out, "com.apple.finder") {
		t.Fatalf("apps: %q %v", out, err)
	}
	out, err = runComputerCode(ctx, `print(permissions())`)
	if err != nil || !strings.Contains(out, `"accessibility":true`) {
		t.Fatalf("permissions: %q %v", out, err)
	}

	delete(appGenerations, "testapp")
	if _, err := runComputerCode(ctx, `click("TestApp", 0)`); !IsStale(err) {
		t.Fatalf("pre-state click: want stale error, got %v", err)
	}

	out, err = runComputerCode(ctx, "state(\"TestApp\")\nclick(\"TestApp\", 0)")
	if err != nil {
		t.Fatalf("state+click: %v", err)
	}
	if !strings.Contains(out, "generation=2") || !strings.Contains(out, "generation=3") {
		t.Errorf("want fresh state folded into the mutation:\n%s", out)
	}
	if !strings.Contains(out, "2 screenshot(s) attached") || len(shots.parts) != 2 || shots.parts[0].ImageURL.URL != "data:image/jpeg;base64,aGVsbG8=" {
		t.Errorf("screenshot attachments: %q, %d shots", out, len(shots.parts))
	}
	if g := genFor("TestApp"); g != 3 {
		t.Errorf("generation after mutation: %d", g)
	}

	out, err = runComputerCode(ctx, `type("TestApp", "hi")`)
	if err != nil || !strings.Contains(out, "typed") || !strings.Contains(out, "no AX grant") {
		t.Fatalf("ack path: %q %v", out, err)
	}

	out, err = runComputerCode(ctx, `screenshot("TestApp")`)
	if err != nil || !strings.Contains(out, "screenshot captured: 5 bytes") {
		t.Fatalf("screenshot: %q %v", out, err)
	}
}

func TestNativeMutationParams(t *testing.T) {
	fakeNativeHelper(t)
	withComputerPolicy(t, computer.NewPolicy([]string{"TestApp"}, nil, true))
	ctx := t.Context()

	if _, err := runComputerCode(ctx, `state("TestApp")`); err != nil {
		t.Fatalf("state: %v", err)
	}
	for _, tc := range []struct {
		code string
		want []string
	}{
		{`press("TestApp", "super+c")`, []string{`title="key" value="super+c"`, `title="gen" value="2"`, `title="app" value="TestApp"`}},
		{`scroll("TestApp", 4, "down", 3)`, []string{`title="index" value="4"`, `title="dir" value="down"`, `title="clicks" value="3"`}},
		{`scroll("TestApp", 4)`, []string{`title="index" value="4"`}},
		{`set("TestApp", 2, "hello")`, []string{`title="index" value="2"`, `title="value" value="hello"`}},
		{`select("TestApp", 2, "word")`, []string{`title="index" value="2"`, `title="target" value="word"`}},
		{`menu("TestApp", 2, "AXShowMenu")`, []string{`title="index" value="2"`, `title="action" value="AXShowMenu"`}},
	} {
		out, err := runComputerCode(ctx, tc.code)
		if err != nil {
			t.Errorf("%s: %v", tc.code, err)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: missing %s in:\n%s", tc.code, w, out)
			}
		}
	}

	if out, err := runComputerCode(ctx, `scroll("TestApp", 4)`); err != nil || strings.Contains(out, `title="dir"`) {
		t.Errorf("scroll without dir: %q %v", out, err)
	}

	if _, err := runComputerCode(ctx, `state("TestApp")`); err != nil {
		t.Fatal(err)
	}
	out, err := runComputerCode(ctx, `click("TestApp", 10, 20)`)
	if err != nil {
		t.Fatalf("pixel click: %v", err)
	}
	if !strings.Contains(out, "generation=3") {
		t.Errorf("pixel click state fold-in: %s", out)
	}

	if out, err := runComputerCode(ctx, `ax("TestApp")`); err != nil || strings.Contains(out, "screenshot(s) attached") {
		t.Errorf("ax: %q %v", out, err)
	}

	if _, err := runComputerCode(ctx, `press("TestApp", "BADJSON")`); err == nil || !strings.Contains(err.Error(), "cannot unmarshal") {
		t.Errorf("non-state reply: %v", err)
	}

	withComputerPolicy(t, computer.NewPolicy(nil, []string{"TestApp"}, true))
	if _, err := runComputerCode(ctx, `press("TestApp", "Return")`); err == nil || !strings.Contains(err.Error(), "policy") {
		t.Errorf("denied mutation: %v", err)
	}
}

func TestComputerArgAndGateErrors(t *testing.T) {
	withComputerPolicy(t, computer.NewPolicy(nil, []string{"Google Chrome", "Denied"}, true))
	ctx := t.Context()
	for _, tc := range []struct{ code, want string }{

		{`state()`, "missing arg 1"},
		{`screenshot()`, "missing arg 1"},
		{`click()`, "missing arg 1"},
		{`click("A", "x")`, "must be a number"},
		{`click("A", "x", "y")`, "must be a number"},
		{`click("A", 1, "y")`, "must be a number"},
		{`type()`, "missing arg 1"},
		{`type("A")`, "missing arg 2"},
		{`press()`, "missing arg 1"},
		{`press("A")`, "missing arg 2"},
		{`scroll()`, "missing arg 1"},
		{`scroll("A")`, "missing arg 2"},
		{`set()`, "missing arg 1"},
		{`set("A")`, "missing arg 2"},
		{`set("A", 1)`, "missing arg 3"},
		{`select()`, "missing arg 1"},
		{`select("A")`, "missing arg 2"},
		{`menu()`, "missing arg 1"},
		{`menu("A", "x")`, "must be a number"},
		{`menu("A", 1)`, "missing arg 3"},
		{`tell()`, "missing arg 1"},
		{`tell("A")`, "missing arg 2"},
		{`chrome_goto()`, "missing arg 1"},
		{`chrome_activate("w", 1)`, "must be a number"},
		{`chrome_activate(1, "i")`, "must be a number"},
		{`chrome_close(1, "i")`, "must be a number"},
		{`chrome_find()`, "missing arg 1"},

		{`state("Denied")`, "policy"},
		{`screenshot("Denied")`, "policy"},
		{`chrome_state()`, "policy"},
		{`chrome_tabs()`, "policy"},
		{`chrome_back()`, "policy"},
		{`chrome_reload()`, "policy"},
		{`chrome_js("1+1")`, "policy"},
		{`chrome_find("x")`, "policy"},
		{`chrome_goto("http://93.184.216.34/")`, "policy"},
		{`chrome_activate(1, 2)`, "policy"},
		{`chrome_close(1, 2)`, "policy"},
	} {
		_, err := runComputerCode(ctx, tc.code)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want %q, got %v", tc.code, tc.want, err)
		}
	}

	if _, err := runComputerCode(ctx, `tell("Denied", 42)`); err == nil || !strings.Contains(err.Error(), "policy") {
		t.Errorf("number arg coercion: %v", err)
	}
	if _, err := runComputerCode(ctx, `tell("Denied", true)`); err == nil || !strings.Contains(err.Error(), "policy") {
		t.Errorf("bool arg coercion: %v", err)
	}
}

func TestGateAppNoPolicy(t *testing.T) {
	withComputerPolicy(t, nil)
	if err := gateApp("Anything"); err == nil || !strings.Contains(err.Error(), "no policy installed") {
		t.Errorf("nil policy: %v", err)
	}
}

func TestNativeHelpersWithoutDriver(t *testing.T) {
	t.Setenv("K_BRAIN_COMPUTER_BIN", filepath.Join(t.TempDir(), "missing-helper"))
	computer.ResetShared()
	t.Cleanup(computer.ResetShared)
	withComputerPolicy(t, computer.NewPolicy([]string{"TestApp"}, nil, true))
	for _, code := range []string{
		`permissions()`, `state("TestApp")`, `ax("TestApp")`, `screenshot("TestApp")`,
		`click("TestApp", 0)`,
	} {
		if _, err := runComputerCode(t.Context(), code); err == nil || !strings.Contains(err.Error(), "k-brain-computer driver") {
			t.Errorf("%s: %v", code, err)
		}
	}
}
