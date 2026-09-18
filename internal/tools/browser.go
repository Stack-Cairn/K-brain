package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/browser"
)

var Browser *browser.Manager

const browserDescription = `Drive a real web browser. The ` + "`code`" + ` argument is JS-like pseudocode using the pre-defined async helpers below; stdout you ` + "`print(...)`" + ` comes back in the result. Start code with a one-line comment describing the step for the user in plain, non-technical language, max 60 chars (e.g. ` + "`# Searching Amazon for paper towels`" + `) — the UI displays it as the step label.

STATE: the browser persists across calls (same tab, cookies, JS context); code variables do NOT. For multi-item tasks, write results incrementally with the write tool and read them back at the end; verify counts before answering.

Batch each sub-procedure (navigate, wait, extract, act) into one call — do not spend a call per action. For an isolated concurrent session (parallel tasks that must not share tabs), pass session="<name>" and reuse it; prefix "<mode>:<name>" (live|dedicated|headless) to override the default mode for that session.

HELPERS (each line in code is one helper call, ` + "`;" + `"-separated): ` +
	"`goto(url)`" + ` first navigation or navigate current tab; ` +
	"`back()`" + `; ` +
	"`info()`" + ` {url,title,viewport,scroll}; ` +
	"`js(expr)`" + ` evaluate JS in the page and print its value (extraction workhorse); ` +
	"`click(x,y)`" + ` viewport-px click; ` +
	"`type(text)`" + ` insert into focused element; ` +
	"`press(key)`" + ` Enter/Tab/Backspace/Arrow*/etc with real key events; ` +
	"`fill(selector,text)`" + ` React/Vue-safe input fill; ` +
	"`scroll(dy)`" + ` wheel at viewport center; ` +
	"`waitLoad()`" + `, ` + "`waitFor(selector[,visible])`" + `; ` +
	"`ax()`" + ` accessibility tree as JSON [{role,name,backendDOMNodeId}] — filter with js() before printing, it is thousands of nodes; then ` + "`box(backendDOMNodeId)`" + ` gives click coords; ` +
	"`tabs()`" + `, ` + "`useTab(targetId)`" + `, ` + "`upload(selector,[paths])`" + `, ` +
	"`dialog(accept[,promptText])`" + ` answer a native JS dialog; ` +
	"`screenshot()`" + ` captures the page — when the model supports images the screenshot is attached to this tool's result.

Login walls: stop and ask the user; never guess credentials. In live mode the browser IS the user's browser with their sessions — treat it as acting on their behalf.`

func BrowserExec() Tool {
	return Tool{
		Def: ai.NewTool("browser_exec",
			browserDescription,
			`{"type":"object","properties":{"code":{"type":"string","description":"Newline/semicolon-separated helper calls; print(...) output is returned."},"session":{"type":"string","description":"Named isolated session (default 'default'); prefix mode e.g. 'headless:scrape'."},"timeout":{"type":"number","description":"Seconds before the call is cancelled (default 60)."}},"required":["code"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			if Browser == nil {
				return "", errors.New("browser subsystem not initialized")
			}
			var a struct {
				Code    string  `json:"code"`
				Session string  `json:"session"`
				Timeout float64 `json:"timeout"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if strings.TrimSpace(a.Code) == "" {
				return "", errors.New("no code provided — e.g. goto(\"https://example.com\"); print(info())")
			}
			if a.Timeout <= 0 {
				a.Timeout = 60
			}
			ctx, cancel := context.WithTimeout(ctx, secondsDuration(a.Timeout))
			defer cancel()

			sess, err := Browser.Session(a.Session)
			if err != nil {
				return "", err
			}
			return sess.Do(ctx, func(b browser.Backend) (string, error) {
				return runBrowserCode(ctx, b, a.Code, a.Session)
			})
		},
	}
}

func runBrowserCode(ctx context.Context, b browser.Backend, code, session string) (string, error) {
	prog, err := parseHelperProgram(code)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	var shots [][]byte
	for _, st := range prog {
		res, shot, err := st.exec(ctx, b)
		if err != nil {

			neutralizeIfBlocked(ctx, b)
			return out.String(), fmt.Errorf("%s: %w", st, err)
		}
		if res != "" {
			fmt.Fprintln(&out, res)
		}
		if shot != nil {
			shots = append(shots, shot)
		}
	}

	if msg := neutralizeIfBlocked(ctx, b); msg != "" {
		fmt.Fprintln(&out, msg)
	}
	if len(shots) > 0 && ScreenshotSink != nil {
		ScreenshotSink(shots)
		fmt.Fprintf(&out, "\n(%d screenshot(s) attached to your context — inspect directly with your vision)", len(shots))
	}
	return out.String(), nil
}

func neutralizeIfBlocked(ctx context.Context, b browser.Backend) string {
	info, err := b.Info(ctx)
	if err != nil || info.URL == "" || info.Dialog != nil {
		return ""
	}
	if err := browser.CheckURL(ctx, info.URL); err != nil {
		_ = b.Navigate(ctx, "about:blank")
		return fmt.Sprintf("(safety: post-navigation URL check failed — %s — page neutralized to about:blank)", err)
	}
	return ""
}

var ScreenshotSink func(jpegs [][]byte)

func secondsDuration(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }
