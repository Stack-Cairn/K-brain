package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

type Mode string

const (
	ModeLive Mode = "live"

	ModeDedicated Mode = "dedicated"

	ModeHeadless Mode = "headless"

	ModeExtension Mode = "extension"
)

var ErrPermissionBlocked = errors.New("chrome permission-blocked")

var ErrNoLiveBrowser = errors.New("no live browser with remote debugging found")

var errDetachFailed = errors.New("detach failed: rod internal layout changed")

type Backend interface {
	Info(ctx context.Context) (PageInfo, error)

	Navigate(ctx context.Context, url string) error

	Back(ctx context.Context) error

	Eval(ctx context.Context, expression string) (string, error)

	ClickAt(ctx context.Context, x, y float64) error

	TypeText(ctx context.Context, text string) error

	PressKey(ctx context.Context, key string) error

	Fill(ctx context.Context, selector, text string) error

	Scroll(ctx context.Context, dy float64) error

	WaitLoad(ctx context.Context) error

	WaitElement(ctx context.Context, selector string, visible bool) (bool, error)

	Screenshot(ctx context.Context, maxDim int) (jpeg []byte, err error)

	AXTree(ctx context.Context) (string, error)

	BoxModel(ctx context.Context, backendNodeID int) (x, y float64, err error)

	Tabs(ctx context.Context) ([]Tab, error)

	UseTab(ctx context.Context, targetID string) error

	UploadFiles(ctx context.Context, selector string, paths []string) error

	Close() error

	Mode() Mode

	Obtained() Obtained

	HandleDialog(accept bool, promptText string) error
}

type PageInfo struct {
	URL, Title            string
	Width, Height         int
	ScrollX, ScrollY      float64
	PageWidth, PageHeight float64
	Dialog                *Dialog `json:",omitempty"`
}

type Dialog struct {
	Type, Message, DefaultPrompt string
}

type Tab struct {
	TargetID, Title, URL string
}

type Browser struct {
	mode       Mode
	browser    *rod.Browser
	page       *rod.Page
	launcher   *launcher.Launcher
	obtained   Obtained
	profileDir string
}

type Obtained int

const (
	ObtainedLive Obtained = iota
	ObtainedLaunched
	ObtainedReattached
)

const (
	DriverRod      = "rod"
	DriverChromedp = "chromedp"
)

var Drivers = []string{DriverRod, DriverChromedp}

var Driver = func() string {
	if d := os.Getenv("K_BRAIN_BROWSER_DRIVER"); d != "" {
		return d
	}
	return "rod"
}()

func SetDriver(d string) {
	if os.Getenv("K_BRAIN_BROWSER_DRIVER") != "" {
		return
	}
	switch d {
	case DriverRod, DriverChromedp:
		Driver = d
	}
}

func Open(ctx context.Context, mode Mode) (Backend, error) {
	return OpenNamed(ctx, mode, "default")
}

func OpenNamed(ctx context.Context, mode Mode, sessionName string) (Backend, error) {
	if mode == ModeExtension {
		return openExtension(ctx)
	}
	if Driver == "chromedp" {
		return openChromedp(ctx, mode, sessionName)
	}
	return openRod(ctx, mode, sessionName)
}

func openRod(ctx context.Context, mode Mode, sessionName string) (*Browser, error) {
	b := &Browser{mode: mode}
	switch mode {
	case ModeLive:
		ws, err := DiscoverLiveWS(ctx)
		if err != nil {
			if !errors.Is(err, ErrNoLiveBrowser) {
				return nil, err
			}

			fb, ferr := openRod(ctx, ModeDedicated, sessionName)
			if ferr != nil {
				return nil, fmt.Errorf("%w; dedicated fallback failed: %w", err, ferr)
			}
			return fb, nil
		}
		b.browser = rod.New().ControlURL(ws)
		if err := b.browser.Connect(); err != nil {
			msg := err.Error()
			if strings.Contains(msg, "403") || strings.Contains(msg, "permission") || strings.Contains(msg, "timed out") {
				return nil, fmt.Errorf("%w: Chrome's 'Allow remote debugging?' popup needs a click (or enable chrome://inspect/#remote-debugging): %s", ErrPermissionBlocked, msg)
			}
			return nil, fmt.Errorf("connect to live browser: %w", err)
		}
		b.obtained = ObtainedLive
	case ModeDedicated, ModeHeadless:
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		profileDir := dedicatedProfileDir(home, sessionName)
		b.profileDir = profileDir

		if ws, ok := DiscoverWSForProfile(ctx, profileDir); ok {
			b.browser = rod.New().ControlURL(ws)
			if err := b.browser.Connect(); err == nil {
				b.obtained = ObtainedReattached
				break
			}
			b.browser = nil
		}
		newLauncher := func() *launcher.Launcher {
			l := launcher.New().
				UserDataDir(profileDir).
				Set("remote-debugging-port", "0").
				Leakless(true).
				Headless(mode == ModeHeadless)
			if os.Getenv("K_BRAIN_BROWSER_DEBUG") != "" {
				l = l.Set("enable-logging", "stderr").Set("v", "1")
			}
			if bin := os.Getenv("ROD_BROWSER_BIN"); bin != "" {
				l = l.Bin(bin)
			}
			return l
		}
		l, ws, err := launchDedicated(ctx, profileDir, newLauncher)
		if err != nil {
			return nil, err
		}
		b.launcher = l
		b.obtained = ObtainedLaunched
		b.browser = rod.New().ControlURL(ws)
		if err := b.browser.Connect(); err != nil {
			l.Kill()
			l.Cleanup()
			return nil, fmt.Errorf("connect to launched chrome: %w", err)
		}
	default:
		return nil, fmt.Errorf("unknown browser mode %q", mode)
	}
	b.browser = b.browser.Context(ctx)
	if err := b.attachPage(); err != nil {

		detach(b.browser)
		if b.launcher != nil {
			b.launcher.Kill()
			b.launcher.Cleanup()
		}
		return nil, err
	}
	return b, nil
}

func launchDedicated(ctx context.Context, profileDir string, newLauncher func() *launcher.Launcher) (*launcher.Launcher, string, error) {
	l := newLauncher()
	ws, err := l.Launch()
	if err == nil {
		return l, ws, nil
	}
	if killProfileChromeQuiet(ctx, profileDir) {
		if l2 := newLauncher(); l2 != nil {
			if ws2, err2 := l2.Launch(); err2 == nil {
				return l2, ws2, nil
			}
		}
	}
	quarantine := profileDir + ".stale-" + time.Now().Format("20060102150405")
	if rerr := os.Rename(profileDir, quarantine); rerr != nil {
		return nil, "", fmt.Errorf("launch dedicated chrome: %w", err)
	}
	l = newLauncher()
	ws, err = l.Launch()
	if err != nil {
		return nil, "", fmt.Errorf("launch dedicated chrome after profile quarantine: %w", err)
	}
	return l, ws, nil
}

func killProfileChromeQuiet(ctx context.Context, profileDir string) bool {
	ws, ok := DiscoverWSForProfile(ctx, profileDir)
	if !ok {
		return false
	}
	b := rod.New().ControlURL(ws)
	if err := b.Context(ctx).Connect(); err != nil {
		return false
	}
	_ = b.Close()
	time.Sleep(300 * time.Millisecond)
	return true
}

func internalURL(u string) bool {
	for _, p := range []string{"chrome://", "chrome-untrusted://", "devtools://", "chrome-extension://", "about:"} {
		if strings.HasPrefix(u, p) {
			return true
		}
	}
	return false
}

func (b *Browser) attachPage() error {
	pages, err := b.browser.Pages()
	if err != nil {
		return fmt.Errorf("list pages: %w", err)
	}
	var blank *rod.Page
	for _, p := range pages {
		info, err := p.Info()
		if err != nil {
			continue
		}
		if info.Type != "page" {
			continue
		}
		if !internalURL(info.URL) {
			b.page = p
			break
		}
		if blank == nil && (info.URL == "about:blank" || strings.HasPrefix(info.URL, "chrome://newtab") || strings.HasPrefix(info.URL, "edge://newtab")) {
			blank = p
		}
	}
	if b.page == nil {
		b.page = blank
	}
	if b.page == nil {
		b.page, err = b.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
		if err != nil {
			return fmt.Errorf("create tab: %w", err)
		}
	}

	return nil
}

func (b *Browser) Mode() Mode { return b.mode }

func (b *Browser) Obtained() Obtained { return b.obtained }

func (b *Browser) Close() error {
	if b.browser == nil {
		return nil
	}
	var err error
	switch {
	case b.mode == ModeLive || b.obtained == ObtainedReattached:

		if !detach(b.browser) {
			err = errDetachFailed
		}
	case b.mode == ModeDedicated:

		if !detach(b.browser) {
			err = errDetachFailed
		}
	default:
		err = b.browser.Close()
		if b.launcher != nil {
			b.launcher.Kill()
			b.launcher.Cleanup()
		}
	}
	b.browser = nil
	return err
}

func (b *Browser) Info(ctx context.Context) (PageInfo, error) {
	if d, err := b.pendingDialog(ctx); err == nil && d != nil {
		return PageInfo{Dialog: d}, nil
	}
	res, err := b.runtimeEval(ctx, `JSON.stringify({url:location.href,title:document.title,w:innerWidth,h:innerHeight,sx:scrollX,sy:scrollY,pw:document.documentElement.scrollWidth,ph:document.documentElement.scrollHeight})`)
	if err != nil {
		return PageInfo{}, err
	}
	var raw struct {
		URL, Title     string
		W, H           int
		SX, SY, PW, PH float64
	}
	if err := json.Unmarshal([]byte(res.Result.Value.String()), &raw); err != nil {
		return PageInfo{}, err
	}
	return PageInfo{URL: raw.URL, Title: raw.Title, Width: raw.W, Height: raw.H, ScrollX: raw.SX, ScrollY: raw.SY, PageWidth: raw.PW, PageHeight: raw.PH}, nil
}

func (b *Browser) pendingDialog(ctx context.Context) (*Dialog, error) {

	return nil, nil
}

func (b *Browser) HandleDialog(accept bool, promptText string) error {
	wait, handle := b.page.Timeout(2 * time.Second).HandleDialog()
	_ = wait()
	return handle(&proto.PageHandleJavaScriptDialog{Accept: accept, PromptText: promptText})
}

func (b *Browser) Navigate(ctx context.Context, url string) error {
	if b.page == nil {
		return errors.New("no attached tab")
	}
	p := b.page.Context(ctx)
	if err := p.Navigate(url); err != nil {
		return err
	}

	return b.WaitLoad(ctx)
}

func (b *Browser) Back(ctx context.Context) error {
	p := b.page.Context(ctx)
	if err := p.NavigateBack(); err != nil {
		return err
	}
	return b.WaitLoad(ctx)
}

func (b *Browser) Eval(ctx context.Context, expression string) (string, error) {
	res, err := b.runtimeEval(ctx, expression)
	if err != nil && strings.Contains(err.Error(), "Illegal return statement") {
		res, err = b.runtimeEval(ctx, "(function(){"+expression+"})()")
	}
	if err != nil {
		return "", err
	}
	if res.ExceptionDetails != nil || (res.Result.Subtype == "error") {
		desc := res.Result.Description
		if res.ExceptionDetails != nil && res.ExceptionDetails.Text != "" {
			desc = res.ExceptionDetails.Text + ": " + desc
		}
		return "", fmt.Errorf("JavaScript evaluation failed: %s; expression: %.160s", desc, expression)
	}
	if res.Result.Type == proto.RuntimeRemoteObjectTypeUndefined {
		return "null", nil
	}
	data, err := res.Result.Value.MarshalJSON()
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (b *Browser) runtimeEval(ctx context.Context, expression string) (*proto.RuntimeEvaluateResult, error) {
	return proto.RuntimeEvaluate{
		Expression:    expression,
		ReturnByValue: true,
		AwaitPromise:  true,
	}.Call(b.page.Context(ctx))
}

func (b *Browser) ClickAt(ctx context.Context, x, y float64) error {
	m := proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMousePressed, X: x, Y: y, Button: proto.InputMouseButtonLeft, ClickCount: 1}
	if err := m.Call(b.page.Context(ctx)); err != nil {
		return err
	}
	m.Type = proto.InputDispatchMouseEventTypeMouseReleased
	return m.Call(b.page.Context(ctx))
}

func (b *Browser) TypeText(ctx context.Context, text string) error {
	return proto.InputInsertText{Text: text}.Call(b.page.Context(ctx))
}

var keyDefs = map[string]struct {
	Code string
	Key  int
	Text string
}{
	"Enter":      {"Enter", 13, "\r"},
	"Tab":        {"Tab", 9, "\t"},
	"Backspace":  {"Backspace", 8, ""},
	"Escape":     {"Escape", 27, ""},
	"Delete":     {"Delete", 46, ""},
	" ":          {"Space", 32, " "},
	"ArrowLeft":  {"ArrowLeft", 37, ""},
	"ArrowUp":    {"ArrowUp", 38, ""},
	"ArrowRight": {"ArrowRight", 39, ""},
	"ArrowDown":  {"ArrowDown", 40, ""},
	"Home":       {"Home", 36, ""},
	"End":        {"End", 35, ""},
	"PageUp":     {"PageUp", 33, ""},
	"PageDown":   {"PageDown", 34, ""},
}

func (b *Browser) PressKey(ctx context.Context, key string) error {
	p := b.page.Context(ctx)
	def, ok := keyDefs[key]
	if !ok && len(key) == 1 {
		def = struct {
			Code string
			Key  int
			Text string
		}{key, int(key[0]), key}
		ok = true
	}
	if !ok {
		return fmt.Errorf("unknown key %q", key)
	}
	down := proto.InputDispatchKeyEvent{
		Type: proto.InputDispatchKeyEventTypeKeyDown,
		Key:  key, Code: def.Code,
		WindowsVirtualKeyCode: def.Key, NativeVirtualKeyCode: def.Key,
	}
	if err := down.Call(p); err != nil {
		return err
	}
	if def.Text != "" {
		if err := (proto.InputDispatchKeyEvent{Type: proto.InputDispatchKeyEventTypeChar, Text: def.Text, Key: key, Code: def.Code}).Call(p); err != nil {
			return err
		}
	}
	up := down
	up.Type = proto.InputDispatchKeyEventTypeKeyUp
	return up.Call(p)
}

func (b *Browser) Fill(ctx context.Context, selector, text string) error {
	sel, _ := json.Marshal(selector)
	focused, err := b.Eval(ctx, fmt.Sprintf(`(()=>{const e=document.querySelector(%s);if(!e)return false;e.focus();return true})()`, sel))
	if err != nil {
		return err
	}
	if focused != "true" {
		return fmt.Errorf("fill: element not found: %s", selector)
	}
	if err := b.PressKey(ctx, "Backspace"); err != nil {
		return err
	}
	if _, err := b.Eval(ctx, fmt.Sprintf(`(()=>{const e=document.querySelector(%s);if(!e)return;const s=window.getSelection(),r=document.createRange();e.select&&e.select();r.selectNodeContents(e);s.removeAllRanges();s.addRange(r)})()`, sel)); err != nil {
		return err
	}
	if err := b.PressKey(ctx, "Backspace"); err != nil {
		return err
	}
	for _, ch := range text {
		if err := b.PressKey(ctx, string(ch)); err != nil {
			return err
		}
	}
	_, err = b.Eval(ctx, fmt.Sprintf(`(()=>{const e=document.querySelector(%s);if(!e)return;e.dispatchEvent(new Event('input',{bubbles:true}));e.dispatchEvent(new Event('change',{bubbles:true}))})()`, sel))
	return err
}

func (b *Browser) Scroll(ctx context.Context, dy float64) error {
	info, err := b.Info(ctx)
	if err != nil {
		return err
	}
	return (proto.InputDispatchMouseEvent{
		Type: proto.InputDispatchMouseEventTypeMouseWheel,
		X:    float64(info.Width) / 2, Y: float64(info.Height) / 2,
		DeltaX: 0, DeltaY: dy,
	}).Call(b.page.Context(ctx))
}

func (b *Browser) WaitLoad(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		res, err := b.Eval(ctx, "document.readyState")
		if err == nil && res == `"complete"` {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (b *Browser) WaitElement(ctx context.Context, selector string, visible bool) (bool, error) {
	sel, _ := json.Marshal(selector)
	expr := fmt.Sprintf(`!!document.querySelector(%s)`, sel)
	if visible {
		expr = fmt.Sprintf(`(()=>{const e=document.querySelector(%s);if(!e)return false;if(typeof e.checkVisibility==='function')return e.checkVisibility({checkOpacity:true,checkVisibilityCSS:true});const s=getComputedStyle(e);return s.display!=='none'&&s.visibility!=='hidden'&&s.opacity!=='0'})()`, sel)
	}
	t := time.NewTicker(300 * time.Millisecond)
	defer t.Stop()
	for {
		res, err := b.Eval(ctx, expr)
		if err != nil {
			return false, err
		}
		if res == "true" {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, nil
		case <-t.C:
		}
	}
}

func (b *Browser) Screenshot(ctx context.Context, maxDim int) ([]byte, error) {
	quality := 80
	p := b.page.Context(ctx)
	req := &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatJpeg, Quality: &quality}
	if maxDim > 0 {
		metrics, err := proto.PageGetLayoutMetrics{}.Call(p)
		if err == nil && metrics.CSSLayoutViewport != nil {
			w := float64(metrics.CSSLayoutViewport.ClientWidth)
			h := float64(metrics.CSSLayoutViewport.ClientHeight)
			scale := 1.0
			if max(w, h) > float64(maxDim) {
				scale = float64(maxDim) / max(w, h)
			}
			req.Clip = &proto.PageViewport{X: 0, Y: 0, Width: w, Height: h, Scale: scale}
		}
	}
	shot, err := req.Call(p)
	if err != nil {
		return nil, err
	}
	return shot.Data, nil
}

func (b *Browser) AXTree(ctx context.Context) (string, error) {
	res, err := proto.AccessibilityGetFullAXTree{}.Call(b.page.Context(ctx))
	if err != nil {
		return "", err
	}
	type node struct {
		Role          string `json:"role"`
		Name          string `json:"name"`
		BackendNodeID int    `json:"backendDOMNodeId"`
	}
	out := make([]node, 0, len(res.Nodes))
	for _, n := range res.Nodes {
		if n.Ignored {
			continue
		}
		nn := node{BackendNodeID: int(n.BackendDOMNodeID)}
		if n.Role != nil {
			nn.Role = n.Role.Value.String()
		}
		if n.Name != nil {
			nn.Name = n.Name.Value.String()
		}
		if nn.Role == "" && nn.Name == "" {
			continue
		}
		out = append(out, nn)
	}
	data, err := json.Marshal(out)
	return string(data), err
}

func (b *Browser) BoxModel(ctx context.Context, backendNodeID int) (float64, float64, error) {
	res, err := proto.DOMGetBoxModel{BackendNodeID: proto.DOMBackendNodeID(backendNodeID)}.Call(b.page.Context(ctx))
	if err != nil {
		return 0, 0, err
	}
	q := res.Model.Content
	var sx, sy float64
	for i := range 4 {
		sx += q[i*2]
		sy += q[i*2+1]
	}
	return sx / 4, sy / 4, nil
}

func (b *Browser) Tabs(ctx context.Context) ([]Tab, error) {
	pages, err := b.browser.Context(ctx).Pages()
	if err != nil {
		return nil, err
	}
	var out []Tab
	for _, p := range pages {
		info, err := p.Info()
		if err != nil || info.Type != "page" {
			continue
		}
		out = append(out, Tab{TargetID: string(p.TargetID), Title: info.Title, URL: info.URL})
	}
	return out, nil
}

func (b *Browser) UseTab(ctx context.Context, targetID string) error {
	page, err := b.browser.Context(ctx).PageFromTarget(proto.TargetTargetID(targetID))
	if err != nil {
		return err
	}
	b.page = page
	return nil
}

func (b *Browser) UploadFiles(ctx context.Context, selector string, paths []string) error {
	p := b.page.Context(ctx)
	el, err := p.Element(selector)
	if err != nil {
		return fmt.Errorf("upload: element not found: %s", selector)
	}
	return el.SetFiles(paths)
}

func dedicatedProfileDir(home, sessionName string) string {
	if sessionName == "" || sessionName == "default" {
		return filepath.Join(home, ".k-brain", "browser", "dedicated-profile")
	}
	return filepath.Join(home, ".k-brain", "browser", "dedicated-profile-"+sessionName)
}
