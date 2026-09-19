package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/privacy"
)

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
		out[i].StopReason, out[i].RawStopReason = "", ""

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
					Parts:   m.Parts,
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
	messages, err := chatMessages(req.Messages)
	if err != nil {
		return Message{}, Usage{}, err
	}
	req.Messages = messages
	c.applyCache(&req)
	body, err := json.Marshal(req)
	if err != nil {
		return Message{}, Usage{}, err
	}
	var msg Message
	var usage Usage
	emitted := false
	wrapText := func(text string) {
		emitted = true
		if onText != nil {
			onText(text)
		}
	}
	wrapThink := func(text string) {
		emitted = true
		if onThink != nil {
			onThink(text)
		}
	}
	wrapTool := func(id, name, args string) {
		emitted = true
		if onToolCall != nil && id != "" {
			onToolCall(id, name, args)
		}
	}
	err = c.policy().run(ctx, func() (err error) {
		msg, usage, err = c.streamOnce(ctx, body, wrapText, wrapThink, wrapTool, func() { emitted = true })
		return err
	}, func() bool { return emitted || usage.PromptTokens != 0 || usage.CompletionTokens != 0 })
	if err != nil {
		return Message{}, usage, err
	}
	return msg, usage, nil
}

func (c *OpenAI) streamOnce(ctx context.Context, body []byte, onText, onThink func(string), onToolCall func(id, name, args string), onUsage func()) (Message, Usage, error) {
	resp, err := c.postJSONOnce(ctx, "/chat/completions", body, true, nil)
	if err != nil {
		return Message{}, Usage{}, err
	}
	defer resp.Body.Close()

	msg := Message{Role: "assistant"}
	var usage Usage
	var calls []ToolCall
	callPositions := make(map[int]int)
	finish := ""
	completed := false
	sse := newSSEReader(resp.Body)
	for {
		event, ok := sse.next()
		if !ok {
			break
		}
		data := strings.TrimSpace(event.Data)
		if data == "[DONE]" {
			completed = true
			break
		}
		var ch chunk
		if err := decodeStreamEvent(data, &ch); err != nil {
			return Message{}, usage, err
		}
		if ch.Usage != nil {
			usage = *ch.Usage
			onUsage()
		}
		if ch.Error != nil {

			return Message{}, usage, nonRetryable{fmt.Errorf("api error: %s", ch.Error.Message)}
		}
		if event.Type == "error" {
			return Message{}, usage, providerStreamError(json.RawMessage(data), "Chat Completions request failed")
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

			if onToolCall != nil {
				onToolCall(cur.ID, cur.Function.Name, cur.Function.Arguments)
			}
		}
	}
	if sse.err != nil {
		return Message{}, usage, sse.err
	}
	if !completed && finish == "" {
		return Message{}, usage, sse.endError("Chat Completions")
	}

	if finish == "length" {
		return finishMessage(msg, calls, finish, onText), usage, nil
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
	return finishMessage(msg, calls, finish, onText), usage, nil
}

func validToolCallArgs(s string) bool {
	var obj map[string]any
	return json.Unmarshal([]byte(s), &obj) == nil && obj != nil
}

func (c *OpenAI) Complete(ctx context.Context, req Request) (string, Usage, error) {
	req.Stream = false
	messages, err := chatMessages(repairToolHistory(stripAuthored(req.Messages)))
	if err != nil {
		return "", Usage{}, err
	}
	req.Messages = messages
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
	return text, usage, err
}

func (c *OpenAI) completeOnce(ctx context.Context, body []byte) (string, Usage, error) {
	resp, err := c.postJSONOnce(ctx, "/chat/completions", body, false, nil)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *Usage          `json:"usage"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", Usage{}, err
	}
	var usage Usage
	if out.Usage != nil {
		usage = *out.Usage
	}
	if len(out.Error) > 0 && string(out.Error) != "null" {
		return "", usage, providerStreamError(out.Error, "Chat Completions request failed")
	}
	if len(out.Choices) == 0 {
		return "", usage, nonRetryable{errors.New("no choices in completion response")}
	}
	if out.Choices[0].FinishReason == "length" {
		return out.Choices[0].Message.Content, usage, &OutputLimitError{Reason: "length"}
	}
	return out.Choices[0].Message.Content, usage, nil
}
