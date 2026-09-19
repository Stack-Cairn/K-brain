package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/privacy"
)

type Message struct {
	Role       string        `json:"role"`
	Content    string        `json:"content"`
	Parts      []ContentPart `json:"-"`
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`

	Name string `json:"name,omitempty"`

	Authored bool `json:"authored,omitempty"`

	SentAt *time.Time `json:"sent_at,omitempty"`

	Usage *Usage `json:"usage,omitempty"`

	Model string `json:"model,omitempty"`

	RewoundFrom string `json:"rewound_from,omitempty"`
}

type ContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
	W int `json:"w,omitempty"`
	H int `json:"h,omitempty"`
}

func (m Message) TextContent() string {
	if m.Content != "" {
		return m.Content
	}
	for _, p := range m.Parts {
		if p.Type == "text" {
			return p.Text
		}
	}
	return ""
}

func imageDataURL(ext string, data []byte) string {
	mime := "image/" + ext
	if ext == "jpg" {
		mime = "image/jpeg"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func ImagePart(ext string, data []byte) ContentPart {
	p := ContentPart{Type: "image_url"}
	p.ImageURL = &struct {
		URL string `json:"url"`
	}{URL: imageDataURL(ext, data)}
	p.W, p.H, _ = DecodeImageSize(data)
	return p
}

func (p ContentPart) DecodeDimensions() (w, h int, ok bool) {
	if p.ImageURL == nil {
		return 0, 0, false
	}
	const prefix = ";base64,"
	i := strings.Index(p.ImageURL.URL, prefix)
	if i < 0 {
		return 0, 0, false
	}

	b64 := p.ImageURL.URL[i+len(prefix):]
	if len(b64) > 65536 {
		b64 = b64[:65536]
	}
	head, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return 0, 0, false
	}
	return DecodeImageSize(head)
}

type messageWire struct {
	Role        string     `json:"role"`
	Content     any        `json:"content"`
	ToolCalls   []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID  string     `json:"tool_call_id,omitempty"`
	Name        string     `json:"name,omitempty"`
	Authored    bool       `json:"authored,omitempty"`
	SentAt      *time.Time `json:"sent_at,omitempty"`
	Usage       *Usage     `json:"usage,omitempty"`
	Model       string     `json:"model,omitempty"`
	RewoundFrom string     `json:"rewound_from,omitempty"`
}

func (m Message) MarshalJSON() ([]byte, error) {
	w := messageWire{
		Role: m.Role, Content: m.Content, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID,
		Name: m.Name, Authored: m.Authored, SentAt: m.SentAt, Usage: m.Usage,
		Model: m.Model, RewoundFrom: m.RewoundFrom,
	}
	if len(m.Parts) > 0 {
		parts := m.Parts
		if m.Content != "" {

			parts = append([]ContentPart{{Type: "text", Text: m.Content}}, parts...)
		}
		w.Content = parts
	}
	return json.Marshal(w)
}

func (m *Message) UnmarshalJSON(data []byte) error {
	var raw struct {
		messageWire
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Role, m.ToolCalls, m.ToolCallID, m.Name = raw.Role, raw.ToolCalls, raw.ToolCallID, raw.Name
	m.Authored, m.SentAt, m.Usage, m.Model, m.RewoundFrom = raw.Authored, raw.SentAt, raw.Usage, raw.Model, raw.RewoundFrom
	if len(raw.Content) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw.Content, &s); err == nil {
		m.Content = s
		return nil
	}
	var parts []ContentPart
	if err := json.Unmarshal(raw.Content, &parts); err != nil {
		return err
	}
	for _, p := range parts {
		switch p.Type {
		case "text":
			m.Content = p.Text
		case "image_url":
			m.Parts = append(m.Parts, p)
		}
	}
	return nil
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	DurationMs int64 `json:"duration_ms,omitempty"`
	ExitCode   int   `json:"exit_code,omitempty"`
}

func stripAuthored(msgs []Message) []Message {
	out := make([]Message, len(msgs))
	copy(out, msgs)
	for i := range out {
		out[i].Authored = false
		out[i].SentAt = nil
		out[i].Usage = nil
		out[i].Model = ""
		out[i].RewoundFrom = ""

		if len(out[i].ToolCalls) > 0 {
			calls := make([]ToolCall, len(out[i].ToolCalls))
			copy(calls, out[i].ToolCalls)
			for j := range calls {
				calls[j].DurationMs = 0
				calls[j].ExitCode = 0
			}
			out[i].ToolCalls = calls
		}

		if len(out[i].Parts) > 0 {
			parts := make([]ContentPart, len(out[i].Parts))
			copy(parts, out[i].Parts)
			for j := range parts {
				parts[j].W = 0
				parts[j].H = 0
			}
			out[i].Parts = parts
		}
	}

	names := map[string]string{}
	for _, m := range out {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				names[tc.ID] = tc.Function.Name
			}
		}
	}
	for i := range out {
		if out[i].Role == "tool" && out[i].Name == "" {
			out[i].Name = names[out[i].ToolCallID]
		}
	}
	return out
}

func repairToolHistory(msgs []Message) []Message {
	answered := make(map[string]bool, len(msgs))
	callName := make(map[string]string, len(msgs))
	for i, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			answered[tc.ID] = false
			callName[tc.ID] = tc.Function.Name
			for _, r := range msgs[i+1:] {
				if r.Role == "tool" && r.ToolCallID == tc.ID {
					answered[tc.ID] = true
					break
				}
				if r.Role == "assistant" || r.Role == "user" {
					break
				}
			}
		}
	}
	out := make([]Message, 0, len(msgs))
	var pending []string
	flush := func() {
		for _, id := range pending {
			out = append(out, Message{
				Role:       "tool",
				Content:    "(interrupted before execution)",
				ToolCallID: id,
				Name:       callName[id],
			})
		}
		pending = nil
	}
	for _, m := range msgs {
		if m.Role == "tool" {
			if _, ok := answered[m.ToolCallID]; !ok {
				flush()

				out = append(out, Message{
					Role:    "user",
					Content: "[earlier tool result]\n" + m.Content,
				})
				continue
			}
			out = append(out, m)
			continue
		}
		flush()
		out = append(out, m)
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				if !answered[tc.ID] {
					pending = append(pending, tc.ID)
				}
			}
		}
	}
	flush()
	return out
}

type Tool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

func NewTool(name, desc, schema string) Tool {
	t := Tool{Type: "function"}
	t.Function.Name = name
	t.Function.Description = desc
	t.Function.Parameters = json.RawMessage(schema)
	return t
}

type Client interface {
	Models(context.Context) ([]ModelInfo, error)
	Stream(context.Context, Request, func(string), func(string), func(id, name, args string)) (Message, Usage, error)
	Complete(context.Context, Request) (string, Usage, error)

	Clone() Client

	SetCacheKey(string)

	Endpoint() string
}

type OpenAI struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client

	MaxRetries int

	OnRetry func(RetryEvent)

	CacheKey                   string
	CacheRetention             string
	CacheSessionAffinity       bool
	CacheControlFormat         string
	SupportsLongCacheRetention bool
}

func (c *OpenAI) Clone() Client        { cp := *c; return &cp }
func (c *OpenAI) SetCacheKey(k string) { c.CacheKey = k }
func (c *OpenAI) Endpoint() string     { return c.BaseURL }

type CacheOptions struct {
	Retention       string
	SessionAffinity bool
	ControlFormat   string
	SupportsLong    bool
}

func (c *OpenAI) SetCacheOptions(o CacheOptions) {
	if o.Retention == "" {
		o.Retention = "short"
	}
	if o.Retention != "none" && o.Retention != "short" && o.Retention != "long" {
		o.Retention = "short"
	}
	c.CacheRetention = o.Retention
	c.CacheSessionAffinity = o.SessionAffinity
	c.CacheControlFormat = o.ControlFormat
	c.SupportsLongCacheRetention = o.SupportsLong
}

func (c *OpenAI) policy() retryPolicy { return newRetryPolicy(c.MaxRetries, c.OnRetry) }

func New(baseURL, apiKey string) *OpenAI {
	return &OpenAI{
		BaseURL:        strings.TrimRight(baseURL, "/"),
		APIKey:         apiKey,
		HTTP:           &http.Client{Transport: privacy.WrapTransport(http.DefaultTransport), Timeout: 10 * time.Minute},
		CacheRetention: "short",
	}
}

func (c *OpenAI) SetOnRetry(fn func(RetryEvent)) {
	c.OnRetry = fn
}

func (c *OpenAI) SetMaxRetries(n int) { c.MaxRetries = n }

type Request struct {
	Model           string    `json:"model"`
	Messages        []Message `json:"messages"`
	Tools           []Tool    `json:"tools,omitempty"`
	MaxTokens       int       `json:"max_tokens,omitempty"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`

	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	Stream      bool     `json:"stream"`

	PromptCacheKey       string `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string `json:"prompt_cache_retention,omitempty"`
	StreamOptions        *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`

	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
	PromptCacheHitTokens   int `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheWriteTokens int `json:"prompt_cache_write_tokens,omitempty"`
}

func (u Usage) Cached() int {
	if u.PromptTokensDetails == nil {
		return u.PromptCacheHitTokens
	}
	if u.PromptTokensDetails.CachedTokens > 0 {
		return u.PromptTokensDetails.CachedTokens
	}
	return u.PromptCacheHitTokens
}

func (u Usage) CacheWrite() int {
	if u.PromptCacheWriteTokens > 0 {
		return u.PromptCacheWriteTokens
	}
	return 0
}

type delta struct {
	Content string `json:"content"`

	ReasoningContent string `json:"reasoning_content"`
	ToolCalls        []struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

type chunk struct {
	Choices []struct {
		Delta        delta  `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *apiError `json:"error"`
	Usage *Usage    `json:"usage"`
}

type apiError struct {
	Message string `json:"message"`
}

type HTTPError struct {
	Status string
	Body   string

	RetryAfter time.Duration
}

func (e *HTTPError) Error() string { return e.Status + ": " + e.Body }

const DefaultMaxAttempts = 8

type RetryEvent struct {
	Attempt int
	Max     int
	Delay   time.Duration
	Err     error
}

func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

type nonRetryable struct{ err error }

func (n nonRetryable) Error() string { return n.err.Error() }
func (n nonRetryable) Unwrap() error { return n.err }

func retryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if _, ok := errors.AsType[nonRetryable](err); ok {
		return false
	}
	if he, ok := errors.AsType[*HTTPError](err); ok {
		if he.RetryAfter > maxRetryAfter {
			return false
		}
		code, _ := strconv.Atoi(strings.Fields(he.Status)[0])
		return retryableStatus(code)
	}

	return true
}

func backoff(attempt int) time.Duration {
	d := min(time.Second<<(attempt-1), 20*time.Second)
	return d + time.Duration(rand.Int64N(int64(d/4)+1))
}

var sleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

var contextLimitMarkers = []string{
	"context_length_exceeded",
	"maximum context length",
	"prompt_too_long",
}

func IsContextLimit(err error) bool {
	if err == nil {
		return false
	}
	if he, ok := errors.AsType[*HTTPError](err); ok {
		if strings.HasPrefix(he.Status, "400") || strings.HasPrefix(he.Status, "413") {
			b := strings.ToLower(he.Body)
			for _, m := range contextLimitMarkers {
				if strings.Contains(b, m) {
					return true
				}
			}
		}
		return false
	}
	s := strings.ToLower(err.Error())
	for _, m := range contextLimitMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

type ModelInfo struct {
	ID                  string   `json:"id"`
	ContextLength       int      `json:"context_length,omitempty"`
	MaxCompletionTokens int      `json:"max_completion_tokens,omitempty"`
	ReasoningEfforts    []string `json:"reasoning_efforts,omitempty"`
	Pricing             *Pricing `json:"pricing,omitempty"`

	InputModalities []string `json:"input_modalities,omitempty"`
}

func (mi ModelInfo) SupportsVision() bool {
	return slices.Contains(mi.InputModalities, "image")
}

type Pricing struct {
	Prompt         string `json:"prompt"`
	Completion     string `json:"completion"`
	InputCacheRead string `json:"input_cache_read,omitempty"`
}

func (p Pricing) Rates() (in, out, cacheRead float64) {
	in, _ = strconv.ParseFloat(p.Prompt, 64)
	out, _ = strconv.ParseFloat(p.Completion, 64)
	cacheRead, _ = strconv.ParseFloat(p.InputCacheRead, 64)
	return in, out, cacheRead
}

func SessionCost(u Usage, in, out, cacheRead float64) float64 {
	cached := u.Cached()
	if cacheRead == 0 {
		cacheRead = in
	}
	return float64(u.PromptTokens-cached)*in +
		float64(cached)*cacheRead +
		float64(u.CompletionTokens)*out
}

func (c *OpenAI) Models(ctx context.Context) ([]ModelInfo, error) {
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, modelCatalogURL(c.BaseURL), nil)
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, newHTTPError(resp, string(b))
	}
	var list struct {
		Data []ModelInfo `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list.Data, nil
}

func modelCatalogURL(base string) string {
	return strings.TrimRight(base, "/") + "/models"
}

func clampCacheKey(key string) string {
	if key == "" {
		return ""
	}
	r := []rune(key)
	if len(r) > 64 {
		r = r[:64]
	}
	return string(r)
}

func (c *OpenAI) applyCache(req *Request) {
	retention := c.CacheRetention
	if retention == "" {
		retention = "short"
	}
	if retention == "none" {
		req.PromptCacheKey = ""
		req.PromptCacheRetention = ""
		return
	}
	if req.PromptCacheKey == "" {
		req.PromptCacheKey = c.CacheKey
	}
	req.PromptCacheKey = clampCacheKey(req.PromptCacheKey)
	if retention == "long" && c.SupportsLongCacheRetention {
		req.PromptCacheRetention = "24h"
	}
}

func (c *OpenAI) applyCacheHeaders(req *http.Request) {
	if !c.CacheSessionAffinity || c.CacheRetention == "none" || c.CacheKey == "" {
		return
	}
	key := clampCacheKey(c.CacheKey)
	req.Header.Set("x-session-id", key)
	req.Header.Set("x-client-request-id", key)
	req.Header.Set("x-session-affinity", key)
}

func (c *OpenAI) Stream(ctx context.Context, req Request, onText, onThink func(string), onToolCall func(id, name, args string)) (Message, Usage, error) {
	req.Stream = true
	req.StreamOptions = &struct {
		IncludeUsage bool `json:"include_usage"`
	}{IncludeUsage: true}
	req.Messages = repairToolHistory(stripAuthored(req.Messages))
	c.applyCache(&req)
	body, err := json.Marshal(req)
	if err != nil {
		return Message{}, Usage{}, err
	}
	var msg Message
	var usage Usage
	emitted := false
	wrapText, wrapThink, wrapTool := onText, onThink, onToolCall
	if onText != nil {
		wrapText = func(s string) { emitted = true; onText(s) }
	}
	if onThink != nil {
		wrapThink = func(s string) { emitted = true; onThink(s) }
	}
	if onToolCall != nil {
		wrapTool = func(id, name, args string) { emitted = true; onToolCall(id, name, args) }
	}
	err = c.policy().run(ctx, func() (err error) {
		msg, usage, err = c.streamOnce(ctx, body, wrapText, wrapThink, wrapTool)
		return err
	}, func() bool { return emitted })
	if err != nil {
		return Message{}, Usage{}, err
	}
	return msg, usage, nil
}

func (c *OpenAI) streamOnce(ctx context.Context, body []byte, onText, onThink func(string), onToolCall func(id, name, args string)) (Message, Usage, error) {
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Message{}, Usage{}, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Authorization", "Bearer "+c.APIKey)
	c.applyCacheHeaders(hr)
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return Message{}, Usage{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Message{}, Usage{}, newHTTPError(resp, string(b))
	}

	msg := Message{Role: "assistant"}
	var usage Usage
	var calls []ToolCall
	callPositions := make(map[int]int)
	finish := ""
	sawData := false
	preview := ""
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			if preview == "" && strings.TrimSpace(line) != "" {
				preview = strings.TrimSpace(line)
				if len(preview) > 160 {
					preview = preview[:160] + "…"
				}
			}
			continue
		}
		sawData = true
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var ch chunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			continue
		}
		if ch.Error != nil {

			return Message{}, usage, nonRetryable{fmt.Errorf("api error: %s", ch.Error.Message)}
		}
		if ch.Usage != nil {
			usage = *ch.Usage
		}
		if len(ch.Choices) == 0 {
			continue
		}
		if fr := ch.Choices[0].FinishReason; fr != "" {
			finish = fr
		}
		d := ch.Choices[0].Delta
		if d.ReasoningContent != "" {
			if onThink != nil {
				onThink(d.ReasoningContent)
			}
		}
		if d.Content != "" {
			msg.Content += d.Content
			if onText != nil {
				onText(d.Content)
			}
		}
		for _, tc := range d.ToolCalls {
			pos, ok := callPositions[tc.Index]
			if !ok {
				pos = len(calls)
				callPositions[tc.Index] = pos
				calls = append(calls, ToolCall{Type: "function"})
			}
			cur := &calls[pos]
			if tc.ID != "" {
				cur.ID = tc.ID
			}
			if tc.Function.Name != "" {
				cur.Function.Name += tc.Function.Name
			}
			cur.Function.Arguments += tc.Function.Arguments

			if onToolCall != nil && cur.ID != "" {
				onToolCall(cur.ID, cur.Function.Name, cur.Function.Arguments)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Message{}, usage, err
	}
	if !sawData {
		if preview == "" {
			preview = "empty response"
		}
		return Message{}, usage, nonRetryable{fmt.Errorf("invalid streaming response from %s: expected SSE data, got %q", c.BaseURL, preview)}
	}

	if finish == "length" && len(calls) > 0 {
		calls = nil
		msg.Content += "\n[response truncated by max_tokens; tool calls discarded]"
	}

	kept := make([]ToolCall, 0, len(calls))
	for _, tc := range calls {
		if tc.Function.Arguments != "" && !validToolCallArgs(tc.Function.Arguments) {
			msg.Content += fmt.Sprintf("\n[tool call %q discarded: arguments are invalid JSON]", tc.Function.Name)
			continue
		}
		kept = append(kept, tc)
	}
	calls = kept
	msg.ToolCalls = calls
	return msg, usage, nil
}

func validToolCallArgs(s string) bool {
	var obj map[string]any
	return json.Unmarshal([]byte(s), &obj) == nil
}

func (c *OpenAI) Complete(ctx context.Context, req Request) (string, Usage, error) {
	req.Stream = false
	req.Messages = stripAuthored(req.Messages)
	c.applyCache(&req)
	body, err := json.Marshal(req)
	if err != nil {
		return "", Usage{}, err
	}
	var text string
	var usage Usage
	err = c.policy().run(ctx, func() (err error) {
		text, usage, err = c.completeOnce(ctx, body)
		return err
	}, nil)
	if err != nil {
		return "", Usage{}, err
	}
	return text, usage, nil
}

func (c *OpenAI) completeOnce(ctx context.Context, body []byte) (string, Usage, error) {
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", Usage{}, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Authorization", "Bearer "+c.APIKey)
	c.applyCacheHeaders(hr)
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return "", Usage{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", Usage{}, newHTTPError(resp, string(b))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *Usage `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", Usage{}, err
	}
	if len(out.Choices) == 0 {
		return "", Usage{}, errors.New("no choices in completion response")
	}
	var usage Usage
	if out.Usage != nil {
		usage = *out.Usage
	}
	return out.Choices[0].Message.Content, usage, nil
}
