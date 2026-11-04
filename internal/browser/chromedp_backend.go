package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

type chromedpBackend struct {
	mode      Mode
	allocCtx  context.Context
	allocStop context.CancelFunc
	ownerStop context.CancelFunc
	targetCtx context.Context
	targetOff context.CancelFunc
	endpoint  string
	targetID  target.ID
	browser   *chromedp.Browser
	connOff   context.CancelFunc
	invalid   error
	closed    bool
	obtained  Obtained
}

func openChromedp(ctx context.Context, mode Mode, sessionName string) (*chromedpBackend, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b := &chromedpBackend{mode: mode}
	if mode == ModeLive {
		ws, err := DiscoverLiveWS(ctx)
		if err != nil {
			return nil, err
		}
		b.endpoint, b.obtained = ws, ObtainedLive
		if err = b.connect(ctx); err != nil {
			return nil, err
		}
		if err := b.selectLiveTarget(ctx, sessionName); err != nil {
			if b.targetID != "" {
				b.invalid = err
				return b, err
			}
			_ = b.Close()
			return nil, err
		}
		err = b.Prepare(ctx)
		return b, err
	}
	if mode != ModeDedicated && mode != ModeHeadless {
		return nil, fmt.Errorf("unknown browser mode %q", mode)
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", mode == ModeHeadless),
		chromedp.Flag("remote-debugging-port", "0"),
	)
	if home, err := os.UserHomeDir(); err == nil {
		opts = append(opts, chromedp.UserDataDir(managedProfileDir(home, mode, sessionName)))
	}
	if bin := os.Getenv("ROD_BROWSER_BIN"); bin != "" {
		opts = append(opts, chromedp.ExecPath(bin))
	}
	b.allocCtx, b.allocStop = chromedp.NewExecAllocator(context.WithoutCancel(ctx), opts...)
	ownerCtx, ownerStop := chromedp.NewContext(b.allocCtx)
	b.ownerStop, b.obtained = ownerStop, ObtainedLaunched
	c := chromedp.FromContext(ownerCtx)
	// Allocate with an independent lifetime. A transient Run context passed to
	// ExecAllocator would also become the browser connection's lifetime.
	stopStartup := context.AfterFunc(ctx, ownerStop)
	browser, err := c.Allocator.Allocate(ownerCtx, chromedp.WithDialTimeout(chromedpDialBudget(ctx)))
	stopStartup()
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = b.Close()
		return nil, fmt.Errorf("chromedp connect: %w", err)
	}
	c.Browser, b.browser = browser, browser
	b.targetID, err = target.CreateTarget("about:blank").Do(cdp.WithExecutor(ctx, browser))
	if err != nil {
		if b.targetID != "" {
			b.invalid = err
			return b, err
		}
		_ = b.Close()
		return nil, err
	}
	return b, b.attach(ctx, b.targetID)
}

func (b *chromedpBackend) selectLiveTarget(ctx context.Context, sessionName string) error {
	browserCtx := cdp.WithExecutor(ctx, b.browser)
	if sessionName == "" || sessionName == "default" {
		infos, err := target.GetTargets().Do(browserCtx)
		if err != nil {
			return err
		}
		var blanks []target.ID
		for _, info := range infos {
			if info.Type != "page" {
				continue
			}
			if !internalURL(info.URL) {
				if liveTargets.claim(b.endpoint, string(info.TargetID), b) == nil {
					b.targetID = info.TargetID
					return nil
				}
			}
			if info.URL == "about:blank" || strings.HasPrefix(info.URL, "chrome://newtab") || strings.HasPrefix(info.URL, "edge://newtab") {
				blanks = append(blanks, info.TargetID)
			}
		}
		for _, id := range blanks {
			if liveTargets.claim(b.endpoint, string(id), b) == nil {
				b.targetID = id
				return nil
			}
		}
	}
	id, err := target.CreateTarget("about:blank").Do(browserCtx)
	if id != "" {
		if claimErr := liveTargets.claim(b.endpoint, string(id), b); claimErr != nil {
			return claimErr
		}
		b.targetID = id
	}
	return err
}

// connect owns only the websocket. NewBrowser's context survives a call, while
// its dial timeout and startup cancellation obey that call's remaining budget.
func chromedpDialBudget(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return max(time.Nanosecond, time.Until(deadline))
	}
	return 10 * time.Second
}

func (b *chromedpBackend) connect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	life, off := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, off)
	browser, err := chromedp.NewBrowser(life, b.endpoint, chromedp.WithDialTimeout(chromedpDialBudget(ctx)))
	stop()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		off()
		return fmt.Errorf("chromedp connect: %w", err)
	}
	b.browser, b.connOff = browser, off
	return nil
}

func (b *chromedpBackend) Invalidate(err error) {
	var switchErr *tabSwitchError
	if errors.As(err, &switchErr) && !isConnErr(err) {
		return
	}
	if errors.Is(b.invalid, ErrTargetClosed) {
		return
	}
	if isTargetClosedErr(err) {
		b.invalid = fmt.Errorf("%w: %s: %w", ErrTargetClosed, b.targetID, err)
	} else if isConnErr(err) || isSessionErr(err) {
		b.invalid = err
	}
}

func (b *chromedpBackend) disconnected() bool {
	if b.browser == nil {
		return true
	}
	select {
	case <-b.browser.LostConnection:
		return true
	default:
		return false
	}
}

func (b *chromedpBackend) Prepare(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.closed {
		return errors.New("chromedp backend is closed")
	}
	if errors.Is(b.invalid, ErrTargetClosed) {
		return b.invalid
	}
	lost := b.disconnected()
	if b.invalid == nil && !lost && b.targetCtx != nil && b.targetCtx.Err() == nil {
		err := b.verify(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !isConnErr(err) && !isSessionErr(err) && !isTargetClosedErr(err) {
			return err
		}
		b.Invalidate(err)
		if errors.Is(b.invalid, ErrTargetClosed) {
			return b.invalid
		}
		lost = b.disconnected()
	}
	if lost || isConnErr(b.invalid) {
		if b.endpoint == "" {
			// ExecAllocator kills its owned process on websocket loss. Reopening
			// would create a different browser, so fail without relaunching it.
			return fmt.Errorf("chromedp original browser disconnected: %w", b.invalid)
		}
		if b.targetOff != nil {
			b.targetOff()
			b.targetOff = nil
		}
		if b.connOff != nil {
			b.connOff()
		}
		if err := b.connect(ctx); err != nil {
			return err
		}
	}
	if b.targetID == "" {
		return errors.New("chromedp has no original target")
	}
	if _, err := target.GetTargetInfo().WithTargetID(b.targetID).Do(cdp.WithExecutor(ctx, b.browser)); err != nil {
		b.Invalidate(err)
		if errors.Is(b.invalid, ErrTargetClosed) {
			return b.invalid
		}
		return err
	}
	b.detach(ctx)
	return b.attach(ctx, b.targetID)
}

// A normal NewContext(..., WithTargetID(...)) cancellation closes that target.
// Give this context an already connected Browser via the public ContextOption,
// without inheriting a browser/RemoteAllocator parent. NewContext records it as
// the first context, whose cancellation performs no target cleanup. Cancel the
// outer lifetime directly; the returned cancel-and-wait assumes allocation and
// must not be used for this borrowed browser. Detachment is explicit below.
func (b *chromedpBackend) attach(ctx context.Context, id target.ID) error {
	life, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	off := func() { cancel(nil) }
	targetCtx, _ := chromedp.NewContext(life, chromedp.WithTargetID(id), func(c *chromedp.Context) {
		c.Browser = b.browser
	})
	watchChromedpTarget(targetCtx, id, cancel)
	b.targetCtx, b.targetOff = targetCtx, off
	// Keep the selected identity even when attachment or validation times out.
	b.targetID = id
	// The first Run starts Target.run using its context. Give it the target's
	// lifetime; a per-call child would stop its event loop as soon as Run returns.
	stopStartup := context.AfterFunc(ctx, off)
	err := chromedp.Run(targetCtx)
	stopStartup()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil {
		err = b.verify(ctx)
	}
	if err != nil {
		b.invalid = err
		b.Invalidate(err)
		return b.invalid
	}
	b.invalid = nil
	return nil
}

func (b *chromedpBackend) verify(ctx context.Context) error {
	return b.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.GetFrameTree().Do(ctx)
		return err
	}))
}

func (b *chromedpBackend) detach(ctx context.Context) {
	if b.targetCtx != nil && !b.disconnected() {
		if c := chromedp.FromContext(b.targetCtx); c != nil && c.Target != nil {
			_ = target.DetachFromTarget().WithSessionID(c.Target.SessionID).Do(cdp.WithExecutor(ctx, b.browser))
		}
	}
	if b.targetOff != nil {
		b.targetOff()
		b.targetOff = nil
	}
}

func (b *chromedpBackend) run(ctx context.Context, actions ...chromedp.Action) error {
	c, cancel := context.WithCancel(b.targetCtx)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	if err := ctx.Err(); err != nil {
		return err
	}
	if d, ok := ctx.Deadline(); ok {
		var off context.CancelFunc
		c, off = context.WithDeadline(c, d)
		defer off()
	}
	return b.targetError(ctx, chromedp.Run(c, actions...))
}

func (b *chromedpBackend) Info(ctx context.Context) (PageInfo, error) {
	var rawJSON string
	err := b.run(ctx, chromedp.Evaluate(`JSON.stringify({url:location.href,title:document.title,w:innerWidth,h:innerHeight,sx:scrollX,sy:scrollY,pw:document.documentElement.scrollWidth,ph:document.documentElement.scrollHeight})`, &rawJSON))
	if err != nil {
		return PageInfo{}, err
	}
	var raw struct {
		URL, Title     string
		W, H           int
		SX, SY, PW, PH float64
	}

	if err := json.Unmarshal([]byte(rawJSON), &raw); err != nil {
		return PageInfo{}, err
	}
	return PageInfo{URL: raw.URL, Title: raw.Title, Width: raw.W, Height: raw.H, ScrollX: raw.SX, ScrollY: raw.SY, PageWidth: raw.PW, PageHeight: raw.PH}, nil
}

func (b *chromedpBackend) Navigate(ctx context.Context, url string) error {
	if err := b.run(ctx, chromedp.Navigate(url)); err != nil {
		return err
	}
	return b.WaitLoad(ctx)
}

func (b *chromedpBackend) Back(ctx context.Context) error {
	var entries []*page.NavigationEntry
	var current int
	err := b.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		cur, ents, err := page.GetNavigationHistory().Do(ctx)
		if err != nil {
			return err
		}
		current, entries = int(cur), ents
		return nil
	}))
	if err != nil {
		return err
	}
	if current <= 0 {
		return nil
	}
	prev := entries[current-1]
	return b.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return page.NavigateToHistoryEntry(prev.ID).Do(ctx)
	}))
}

func (b *chromedpBackend) Eval(ctx context.Context, expression string) (string, error) {
	var res *runtime.RemoteObject
	var exp *runtime.ExceptionDetails
	err := b.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		res, exp, err = runtime.Evaluate(expression).
			WithReturnByValue(true).
			WithAwaitPromise(true).
			Do(ctx)
		return err
	}))
	if err != nil {

		if strings.Contains(err.Error(), "Illegal return statement") {
			return b.Eval(ctx, "(function(){"+expression+"})()")
		}
		return "", err
	}
	if exp != nil {
		desc := exp.Text
		if exp.Exception != nil && exp.Exception.Description != "" {
			desc = exp.Exception.Description
		}
		return "", fmt.Errorf("JavaScript evaluation failed: %s; expression: %.160s", desc, expression)
	}
	if res == nil || res.Type == "undefined" {
		return "null", nil
	}
	return string(res.Value), nil
}

func (b *chromedpBackend) ClickAt(ctx context.Context, x, y float64) error {
	press := input.DispatchMouseEvent(input.MousePressed, x, y).
		WithButton(input.Left).WithClickCount(1)
	release := input.DispatchMouseEvent(input.MouseReleased, x, y).
		WithButton(input.Left).WithClickCount(1)
	return b.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		if err := press.Do(ctx); err != nil {
			return err
		}
		return release.Do(ctx)
	}))
}

func (b *chromedpBackend) TypeText(ctx context.Context, text string) error {
	return b.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return input.InsertText(text).Do(ctx)
	}))
}

var cdKeys = map[string]struct {
	code string
	key  int64
	text string
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

func (b *chromedpBackend) PressKey(ctx context.Context, key string) error {
	def, ok := cdKeys[key]
	if !ok && len(key) == 1 {
		def = struct {
			code string
			key  int64
			text string
		}{key, int64(key[0]), key}
		ok = true
	}
	if !ok {
		return fmt.Errorf("unknown key %q", key)
	}
	return b.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		down := &input.DispatchKeyEventParams{
			Type:                  input.KeyDown,
			Key:                   key,
			Code:                  def.code,
			WindowsVirtualKeyCode: def.key,
			NativeVirtualKeyCode:  def.key,
		}
		if err := down.Do(ctx); err != nil {
			return err
		}
		if def.text != "" {
			ch := &input.DispatchKeyEventParams{Type: input.KeyChar, Text: def.text, Key: key, Code: def.code}
			if err := ch.Do(ctx); err != nil {
				return err
			}
		}
		up := &input.DispatchKeyEventParams{Type: input.KeyUp, Key: key, Code: def.code, WindowsVirtualKeyCode: def.key, NativeVirtualKeyCode: def.key}
		return up.Do(ctx)
	}))
}

func (b *chromedpBackend) Fill(ctx context.Context, selector, text string) error {
	_, err := b.Eval(ctx, fillExpression(selector, text))
	return err
}

func (b *chromedpBackend) Scroll(ctx context.Context, dy float64) error {
	info, err := b.Info(ctx)
	if err != nil {
		return err
	}
	x, y := float64(info.Width)/2, float64(info.Height)/2
	return b.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		p := &input.DispatchMouseEventParams{Type: input.MouseWheel, X: x, Y: y, DeltaX: 0, DeltaY: dy}
		return p.Do(ctx)
	}))
}

func (b *chromedpBackend) WaitLoad(ctx context.Context) error {
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

func (b *chromedpBackend) WaitElement(ctx context.Context, selector string, visible bool) (bool, error) {
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

func (b *chromedpBackend) Screenshot(ctx context.Context, maxDim int) ([]byte, error) {
	var data []byte
	err := b.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		params := page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatJpeg).WithQuality(80)
		if maxDim > 0 {
			_, _, _, cssLayout, _, _, err := page.GetLayoutMetrics().Do(ctx)
			if err == nil && cssLayout != nil {
				w, h := float64(cssLayout.ClientWidth), float64(cssLayout.ClientHeight)
				if max(w, h) > float64(maxDim) {
					scale := float64(maxDim) / max(w, h)
					params = params.WithClip(&page.Viewport{X: 0, Y: 0, Width: w, Height: h, Scale: scale})
				}
			}
		}
		var err error
		data, err = params.Do(ctx)
		return err
	}))
	return data, err
}

func (b *chromedpBackend) AXTree(ctx context.Context) (string, error) {
	var nodes []*accessibility.Node
	err := b.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		nodes, err = accessibility.GetFullAXTree().Do(ctx)
		return err
	}))
	if err != nil {
		return "", err
	}
	type node struct {
		Role          string `json:"role"`
		Name          string `json:"name"`
		BackendNodeID int    `json:"backendDOMNodeId"`
	}
	out := make([]node, 0, len(nodes))
	for _, n := range nodes {
		if n.Ignored {
			continue
		}
		nn := node{BackendNodeID: int(n.BackendDOMNodeID)}
		if n.Role != nil {
			nn.Role = fmt.Sprint(n.Role.Value)
		}
		if n.Name != nil {
			nn.Name = fmt.Sprint(n.Name.Value)
		}
		if nn.Role == "" && nn.Name == "" {
			continue
		}
		out = append(out, nn)
	}
	data, err := json.Marshal(out)
	return string(data), err
}

func (b *chromedpBackend) BoxModel(ctx context.Context, backendNodeID int) (float64, float64, error) {
	var model *dom.BoxModel
	err := b.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		model, err = dom.GetBoxModel().WithBackendNodeID(cdp.BackendNodeID(backendNodeID)).Do(ctx)
		return err
	}))
	if err != nil {
		return 0, 0, err
	}
	q := model.Content
	var sx, sy float64
	for i := range 4 {
		sx += q[i*2]
		sy += q[i*2+1]
	}
	return sx / 4, sy / 4, nil
}

func (b *chromedpBackend) Tabs(ctx context.Context) ([]Tab, error) {
	var infos []*target.Info
	err := b.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		infos, err = target.GetTargets().Do(ctx)
		return err
	}))
	if err != nil {
		return nil, err
	}
	var out []Tab
	for _, ti := range infos {
		if ti.Type != "page" {
			continue
		}
		out = append(out, Tab{TargetID: string(ti.TargetID), Title: ti.Title, URL: ti.URL})
	}
	return out, nil
}

func (b *chromedpBackend) UseTab(ctx context.Context, targetID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.closed || b.disconnected() {
		return errors.New("chromedp browser is not connected")
	}
	id := target.ID(targetID)
	if id == "" {
		return errors.New("chromedp target ID is empty")
	}
	if id == b.targetID {
		return b.Prepare(ctx)
	}
	if _, err := target.GetTargetInfo().WithTargetID(id).Do(cdp.WithExecutor(ctx, b.browser)); err != nil {
		return &tabSwitchError{targetID: targetID, cause: err}
	}
	if err := liveTargets.claim(b.endpoint, targetID, b); err != nil {
		return err
	}
	candidate := &chromedpBackend{browser: b.browser}
	if err := candidate.attach(ctx, id); err != nil {
		candidate.detach(ctx)
		liveTargets.release(b.endpoint, targetID, b)
		return &tabSwitchError{targetID: targetID, cause: err}
	}
	oldID := b.targetID
	b.detach(ctx)
	b.targetID, b.targetCtx, b.targetOff, b.invalid = id, candidate.targetCtx, candidate.targetOff, nil
	liveTargets.release(b.endpoint, string(oldID), b)
	return nil
}

func (b *chromedpBackend) UploadFiles(ctx context.Context, selector string, paths []string) error {
	var nodeID cdp.NodeID
	err := b.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		doc, err := dom.GetDocument().WithDepth(-1).Do(ctx)
		if err != nil {
			return err
		}
		nodeID, err = dom.QuerySelector(doc.NodeID, selector).Do(ctx)
		return err
	}))
	if err != nil {
		return err
	}
	if nodeID == 0 {
		return fmt.Errorf("upload: element not found: %s", selector)
	}
	return b.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return dom.SetFileInputFiles(paths).WithNodeID(nodeID).Do(ctx)
	}))
}

func (b *chromedpBackend) Close() error {
	defer liveTargets.releaseAll(b)
	if b.closed {
		return nil
	}
	b.closed = true
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	b.detach(ctx)
	if b.connOff != nil {
		b.connOff()
	}
	if b.ownerStop != nil {
		b.ownerStop()
	}
	if b.allocStop != nil {
		b.allocStop()
	}

	if b.mode != ModeLive {
		time.Sleep(150 * time.Millisecond)
	}
	return nil
}

func (b *chromedpBackend) Mode() Mode { return b.mode }

func (b *chromedpBackend) Obtained() Obtained { return b.obtained }

func (b *chromedpBackend) HandleDialog(accept bool, promptText string) error {
	return b.run(context.Background(), chromedp.ActionFunc(func(ctx context.Context) error {
		return page.HandleJavaScriptDialog(accept).WithPromptText(promptText).Do(ctx)
	}))
}
