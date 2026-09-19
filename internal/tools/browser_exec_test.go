package tools

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/browser"
)

type fakeBackend struct {
	calls []string
	err   error

	info  browser.PageInfo
	eval  string
	ax    string
	tabs  []browser.Tab
	shot  []byte
	found bool
	boxX  float64
	boxY  float64
	mode  browser.Mode
}

func (f *fakeBackend) log(call string) error {
	f.calls = append(f.calls, call)
	return f.err
}

func (f *fakeBackend) Info(context.Context) (browser.PageInfo, error) {
	return f.info, f.log("Info()")
}

func (f *fakeBackend) Navigate(_ context.Context, url string) error {
	return f.log("Navigate(" + url + ")")
}
func (f *fakeBackend) Back(context.Context) error { return f.log("Back()") }
func (f *fakeBackend) Eval(_ context.Context, expr string) (string, error) {
	return f.eval, f.log("Eval(" + expr + ")")
}

func (f *fakeBackend) ClickAt(_ context.Context, x, y float64) error {
	return f.log(fmtCall("ClickAt", x, y))
}

func (f *fakeBackend) TypeText(_ context.Context, text string) error {
	return f.log("TypeText(" + text + ")")
}

func (f *fakeBackend) PressKey(_ context.Context, key string) error {
	return f.log("PressKey(" + key + ")")
}

func (f *fakeBackend) Fill(_ context.Context, sel, text string) error {
	return f.log("Fill(" + sel + "," + text + ")")
}

func (f *fakeBackend) Scroll(_ context.Context, dy float64) error {
	return f.log(fmtCall("Scroll", dy))
}
func (f *fakeBackend) WaitLoad(context.Context) error { return f.log("WaitLoad()") }
func (f *fakeBackend) WaitElement(_ context.Context, sel string, visible bool) (bool, error) {
	return f.found, f.log("WaitElement(" + sel + "," + boolStr(visible) + ")")
}

func (f *fakeBackend) Screenshot(_ context.Context, maxDim int) ([]byte, error) {
	return f.shot, f.log(fmtCall("Screenshot", float64(maxDim)))
}
func (f *fakeBackend) AXTree(context.Context) (string, error) { return f.ax, f.log("AXTree()") }
func (f *fakeBackend) BoxModel(_ context.Context, id int) (float64, float64, error) {
	return f.boxX, f.boxY, f.log(fmtCall("BoxModel", float64(id)))
}

func (f *fakeBackend) Tabs(context.Context) ([]browser.Tab, error) { return f.tabs, f.log("Tabs()") }

func (f *fakeBackend) UseTab(_ context.Context, id string) error { return f.log("UseTab(" + id + ")") }

func (f *fakeBackend) UploadFiles(_ context.Context, sel string, paths []string) error {
	return f.log("UploadFiles(" + sel + "," + strings.Join(paths, "|") + ")")
}

func (f *fakeBackend) HandleDialog(accept bool, prompt string) error {
	return f.log("HandleDialog(" + boolStr(accept) + "," + prompt + ")")
}
func (f *fakeBackend) Mode() browser.Mode         { return f.mode }
func (f *fakeBackend) Obtained() browser.Obtained { return browser.ObtainedLaunched }
func (f *fakeBackend) Close() error               { return f.log("Close()") }

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func fmtCall(name string, nums ...float64) string {
	var b strings.Builder
	b.WriteString(name + "(")
	for i, n := range nums {
		if i > 0 {
			b.WriteString(",")
		}
		data, _ := json.Marshal(n)
		b.Write(data)
	}
	b.WriteString(")")
	return b.String()
}

func execOne(t *testing.T, b browser.Backend, code string) (string, []byte, error) {
	t.Helper()
	prog, err := parseHelperProgram(code)
	if err != nil {
		t.Fatalf("parse %q: %v", code, err)
	}
	if len(prog) != 1 {
		t.Fatalf("want 1 statement from %q, got %d", code, len(prog))
	}
	return prog[0].exec(t.Context(), b)
}

func TestExecHelpers(t *testing.T) {
	tests := []struct {
		name  string
		code  string
		setup func(*fakeBackend)
		out   string
		calls []string
	}{

		{name: "goto", code: `goto("http://93.184.216.34/")`, calls: []string{"Navigate(http://93.184.216.34/)"}},
		{name: "goto non-http skips checks", code: `goto("about:blank")`, calls: []string{"Navigate(about:blank)"}},
		{name: "back", code: `back()`, calls: []string{"Back()"}},
		{
			name:  "info",
			code:  `info()`,
			setup: func(f *fakeBackend) { f.info = browser.PageInfo{URL: "http://x/", Title: "T", Width: 800} },
			out:   `{"URL":"http://x/","Title":"T","Width":800,"Height":0,"ScrollX":0,"ScrollY":0,"PageWidth":0,"PageHeight":0}`,
			calls: []string{"Info()"},
		},
		{
			name:  "js",
			code:  `js("document.title")`,
			setup: func(f *fakeBackend) { f.eval = `"hello"` },
			out:   `"hello"`,
			calls: []string{"Eval(document.title)"},
		},
		{name: "click", code: `click(10, 20.5)`, calls: []string{"ClickAt(10,20.5)"}},
		{name: "type", code: `type("hi there")`, calls: []string{"TypeText(hi there)"}},
		{name: "type coerces number arg", code: `type(42)`, calls: []string{"TypeText(42)"}},
		{name: "type coerces array arg", code: `type([1,2])`, calls: []string{"TypeText([1,2])"}},
		{name: "press", code: `press(Enter)`, calls: []string{"PressKey(Enter)"}},
		{name: "fill", code: `fill("#q", "paper towels")`, calls: []string{"Fill(#q,paper towels)"}},
		{name: "scroll", code: `scroll(-120)`, calls: []string{"Scroll(-120)"}},
		{name: "scroll defaults to -300", code: `scroll()`, calls: []string{"Scroll(-300)"}},
		{name: "scroll defaults on non-number", code: `scroll("down")`, calls: []string{"Scroll(-300)"}},
		{name: "waitLoad", code: `waitLoad()`, calls: []string{"WaitLoad()"}},
		{
			name:  "waitFor found",
			code:  `waitFor(".ok", true)`,
			setup: func(f *fakeBackend) { f.found = true },
			out:   "true",
			calls: []string{"WaitElement(.ok,true)"},
		},
		{name: "waitFor defaults visible false", code: `waitFor(".ok")`, out: "false", calls: []string{"WaitElement(.ok,false)"}},
		{
			name:  "ax",
			code:  `ax()`,
			setup: func(f *fakeBackend) { f.ax = `[{"role":"button"}]` },
			out:   `[{"role":"button"}]`,
			calls: []string{"AXTree()"},
		},
		{
			name:  "box",
			code:  `box(7)`,
			setup: func(f *fakeBackend) { f.boxX, f.boxY = 12.34, 56.78 },
			out:   `{"x":12.3,"y":56.8}`,
			calls: []string{"BoxModel(7)"},
		},
		{
			name:  "tabs",
			code:  `tabs()`,
			setup: func(f *fakeBackend) { f.tabs = []browser.Tab{{TargetID: "t1", Title: "one", URL: "http://a/"}} },
			out:   `[{"TargetID":"t1","Title":"one","URL":"http://a/"}]`,
			calls: []string{"Tabs()"},
		},
		{name: "useTab", code: `useTab("t1")`, calls: []string{"UseTab(t1)"}},
		{name: "upload array", code: `upload("#f", ["/tmp/a.png", "/tmp/b.png"])`, calls: []string{"UploadFiles(#f,/tmp/a.png|/tmp/b.png)"}},
		{name: "upload single string", code: `upload("#f", "/tmp/a.png")`, calls: []string{"UploadFiles(#f,/tmp/a.png)"}},
		{name: "dialog accept", code: `dialog(true)`, calls: []string{"HandleDialog(true,)"}},
		{name: "dialog defaults to accept", code: `dialog()`, calls: []string{"HandleDialog(true,)"}},
		{name: "dialog with prompt text", code: `dialog(false, "answer")`, calls: []string{"HandleDialog(false,answer)"}},
		{
			name:  "screenshot",
			code:  `screenshot()`,
			setup: func(f *fakeBackend) { f.shot = []byte("jpegbytes") },
			out:   "(screenshot captured: 9 bytes, jpeg, ≤1568px)",
			calls: []string{"Screenshot(1568)"},
		},
		{name: "print literal", code: `print("hello")`, out: "hello"},
		{name: "print number", code: `print(42)`, out: "42"},
		{
			name:  "print nested call",
			code:  `print(js("1+1"))`,
			setup: func(f *fakeBackend) { f.eval = "2" },
			out:   "2",
			calls: []string{"Eval(1+1)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &fakeBackend{mode: browser.ModeHeadless}
			if tt.setup != nil {
				tt.setup(b)
			}
			out, shot, err := execOne(t, b, tt.code)
			if err != nil {
				t.Fatalf("exec: %v", err)
			}
			if out != tt.out {
				t.Errorf("out = %q, want %q", out, tt.out)
			}
			if got := strings.Join(b.calls, " "); got != strings.Join(tt.calls, " ") {
				t.Errorf("calls = %v, want %v", b.calls, tt.calls)
			}
			if tt.name == "screenshot" && string(shot) != "jpegbytes" {
				t.Errorf("screenshot bytes not returned: %q", shot)
			} else if tt.name != "screenshot" && shot != nil {
				t.Errorf("unexpected screenshot: %q", shot)
			}
		})
	}
}

func TestExecArgErrors(t *testing.T) {
	tests := []struct {
		name, code, want string
	}{
		{"goto missing url", `goto()`, "goto: missing arg 1"},
		{"js missing expr", `js()`, "js: missing arg 1"},
		{"click missing x", `click()`, "click: missing arg 1"},
		{"click missing y", `click(10)`, "click: missing arg 2"},
		{"click non-numeric x", `click("a", 2)`, "click: arg 1 must be a number"},
		{"type missing text", `type()`, "type: missing arg 1"},
		{"press missing key", `press()`, "press: missing arg 1"},
		{"fill missing selector", `fill()`, "fill: missing arg 1"},
		{"fill missing text", `fill("#q")`, "fill: missing arg 2"},
		{"waitFor missing selector", `waitFor()`, "waitFor: missing arg 1"},
		{"box missing id", `box()`, "box: missing arg 1"},
		{"box non-numeric", `box("x")`, "box: arg 1 must be a number"},
		{"useTab missing id", `useTab()`, "useTab: missing arg 1"},
		{"upload missing selector", `upload()`, "upload: missing arg 1"},
		{"upload missing paths", `upload("#f")`, "upload: missing paths array"},
		{"upload non-string path", `upload("#f", [1, 2])`, "upload: paths must be strings"},
		{"upload bad paths type", `upload("#f", 7)`, "upload: paths must be an array or string"},
		{"unknown helper", `frobnicate("x")`, `unknown helper "frobnicate"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &fakeBackend{mode: browser.ModeHeadless}
			_, _, err := execOne(t, b, tt.code)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
			if len(b.calls) != 0 {
				t.Errorf("backend must not be called on an arg error: %v", b.calls)
			}
		})
	}
}

func TestExecBackendErrors(t *testing.T) {
	boom := errors.New("boom")
	for _, code := range []string{
		`goto("http://93.184.216.34/")`, `back()`, `info()`, `js("1")`, `click(1,2)`,
		`type("x")`, `press(Enter)`, `fill("#q","x")`, `scroll(-10)`, `waitLoad()`,
		`waitFor(".ok")`, `ax()`, `box(1)`, `tabs()`, `useTab("t")`,
		`upload("#f","/tmp/a")`, `dialog(true)`, `screenshot()`,
	} {
		t.Run(code, func(t *testing.T) {
			b := &fakeBackend{mode: browser.ModeHeadless, err: boom}
			out, shot, err := execOne(t, b, code)
			if !errors.Is(err, boom) {
				t.Fatalf("err = %v, want boom", err)
			}
			if out != "" || shot != nil {
				t.Errorf("want empty result on error, got out=%q shot=%q", out, shot)
			}
		})
	}
}

func TestExecGotoSafety(t *testing.T) {
	tests := []struct {
		name, code string
		mode       browser.Mode
		wantErr    string
		wantCalls  int
	}{
		{name: "metadata blocked in headless", code: `goto("http://169.254.169.254/latest/")`, mode: browser.ModeHeadless, wantErr: "cloud-metadata"},
		{name: "metadata blocked in live", code: `goto("http://169.254.169.254/latest/")`, mode: browser.ModeLive, wantErr: "cloud-metadata"},
		{name: "private blocked in headless", code: `goto("http://127.0.0.1:8080/")`, mode: browser.ModeHeadless, wantErr: "private/internal address"},
		{name: "private allowed in live", code: `goto("http://127.0.0.1:8080/")`, mode: browser.ModeLive, wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &fakeBackend{mode: tt.mode}
			_, _, err := execOne(t, b, tt.code)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("exec: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
			if len(b.calls) != tt.wantCalls {
				t.Errorf("calls = %v, want %d", b.calls, tt.wantCalls)
			}
		})
	}
}

func TestRunBrowserCodeProgram(t *testing.T) {
	b := &fakeBackend{mode: browser.ModeHeadless, eval: `"title"`, info: browser.PageInfo{URL: "http://93.184.216.34/"}}
	out, err := runBrowserCode(t.Context(), b, "# label\ngoto(\"http://93.184.216.34/\")\nprint(js(\"document.title\"))\nwaitLoad()", "")
	if err != nil {
		t.Fatal(err)
	}
	if out != "\"title\"\n" {
		t.Errorf("out = %q", out)
	}
	want := "Navigate(http://93.184.216.34/) Eval(document.title) WaitLoad() Info()"
	if got := strings.Join(b.calls, " "); got != want {
		t.Errorf("calls = %q, want %q", got, want)
	}
}

func TestRunBrowserCodeParseError(t *testing.T) {
	b := &fakeBackend{mode: browser.ModeHeadless}
	if _, err := runBrowserCode(t.Context(), b, "not a call", ""); err == nil {
		t.Fatal("malformed program must error")
	}
	if len(b.calls) != 0 {
		t.Errorf("no backend calls expected: %v", b.calls)
	}
}

func TestRunBrowserCodeStatementError(t *testing.T) {
	b := &fakeBackend{mode: browser.ModeHeadless}
	out, err := runBrowserCode(t.Context(), b, `print("first")`+"\n"+`box("x")`, "")
	if err == nil || !strings.Contains(err.Error(), `box("x")`) {
		t.Fatalf("err = %v, want the failing statement in it", err)
	}
	if out != "first\n" {
		t.Errorf("partial output lost: %q", out)
	}
}

func TestRunBrowserCodeAttachments(t *testing.T) {
	for _, vision := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			b := &fakeBackend{mode: browser.ModeHeadless, shot: []byte("jpeg")}
			code := "screenshot(); screenshot()"
			if fail {
				code += `; box("x")`
			}
			tool := Tool{Def: ai.NewTool("capture", "", `{}`), Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
				return runBrowserCode(ctx, b, code, "")
			}}
			result := ExecuteResult(t.Context(), []Tool{tool}, "capture", nil, vision)
			want := 0
			if vision {
				want = 2
			}
			if len(result.Parts) != want {
				t.Fatalf("vision=%v fail=%v: %+v", vision, fail, result)
			}
			if vision && result.Parts[0].ImageURL.URL != ai.ImagePart("jpg", []byte("jpeg")).ImageURL.URL {
				t.Fatal("wrong screenshot")
			}
			if strings.HasPrefix(result.Text, "Error:") != fail {
				t.Fatalf("failure lost: %q", result.Text)
			}
			if strings.Contains(result.Text, "screenshot(s) attached") != (vision && !fail) {
				t.Fatalf("incorrect attach note: %q", result.Text)
			}
		}
	}
}

func TestNeutralizeIfBlocked(t *testing.T) {
	tests := []struct {
		name      string
		info      browser.PageInfo
		infoErr   error
		wantMsg   bool
		wantCalls []string
	}{
		{name: "safe url", info: browser.PageInfo{URL: "http://93.184.216.34/"}, wantCalls: []string{"Info()"}},
		{name: "blank url", wantCalls: []string{"Info()"}},
		{name: "info error", infoErr: errors.New("nope"), wantCalls: []string{"Info()"}},
		{
			name:      "pending dialog is left alone",
			info:      browser.PageInfo{URL: "http://169.254.169.254/", Dialog: &browser.Dialog{Type: "alert"}},
			wantCalls: []string{"Info()"},
		},
		{
			name:      "blocked url neutralized",
			info:      browser.PageInfo{URL: "http://169.254.169.254/latest/"},
			wantMsg:   true,
			wantCalls: []string{"Info()", "Navigate(about:blank)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &fakeBackend{mode: browser.ModeHeadless, info: tt.info, err: tt.infoErr}
			msg := neutralizeIfBlocked(t.Context(), b)
			if tt.wantMsg != strings.Contains(msg, "neutralized to about:blank") {
				t.Errorf("msg = %q, wantMsg = %v", msg, tt.wantMsg)
			}
			if got := strings.Join(b.calls, " "); got != strings.Join(tt.wantCalls, " ") {
				t.Errorf("calls = %v, want %v", b.calls, tt.wantCalls)
			}
		})
	}
}

func TestRunBrowserCodeNeutralizesOnError(t *testing.T) {
	b := &fakeBackend{mode: browser.ModeHeadless, info: browser.PageInfo{URL: "http://169.254.169.254/"}}
	if _, err := runBrowserCode(t.Context(), b, `box("x")`, ""); err == nil {
		t.Fatal("want error")
	}
	want := "Info() Navigate(about:blank)"
	if got := strings.Join(b.calls, " "); got != want {
		t.Errorf("calls = %q, want %q", got, want)
	}
}

func TestBrowserExecArgErrors(t *testing.T) {
	old := Browser
	Browser = browser.NewManager(browser.ModeHeadless)
	defer func() { Browser = old }()

	tests := []struct {
		name, args, want string
	}{
		{"bad json", `{"code":`, "unexpected end of JSON input"},
		{"blank code", `{"code":"  "}`, "no code provided"},
		{"bad session mode prefix", `{"code":"info()","session":"bogus:x"}`, "unknown mode prefix"},
		{"bad session name", `{"code":"info()","session":"live:not a name"}`, "invalid session name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := Execute(t.Context(), []Tool{BrowserExec()}, "browser_exec", json.RawMessage(tt.args))
			if !strings.HasPrefix(out, "Error") || !strings.Contains(out, tt.want) {
				t.Fatalf("out = %q, want error containing %q", out, tt.want)
			}
		})
	}
}

func TestParseEdgeCases(t *testing.T) {
	prog, err := parseHelperProgram(`info(); # trailing comment
js("say \"hi\"; ok")`)
	if err != nil {
		t.Fatal(err)
	}
	if len(prog) != 2 {
		t.Fatalf("want 2 statements, got %d: %+v", len(prog), prog)
	}
	if prog[1].name != "js" || prog[1].args[0] != `say "hi"; ok` {
		t.Errorf("escaped quotes in arg: %+v", prog[1].args)
	}

	for _, code := range []string{`goto(bad value)`, `print(foo(bad value))`} {
		_, err := parseHelperProgram(code)
		if err == nil || !strings.Contains(err.Error(), "bad value") {
			t.Errorf("%s: err = %v, want a bad-value error", code, err)
		}
	}
}

func TestRunBrowserCodeAppendsSafetyNote(t *testing.T) {
	b := &fakeBackend{mode: browser.ModeHeadless, info: browser.PageInfo{URL: "http://169.254.169.254/"}}
	out, err := runBrowserCode(t.Context(), b, `js("location='http://169.254.169.254/'")`, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "neutralized to about:blank") {
		t.Errorf("missing safety note: %q", out)
	}
}
