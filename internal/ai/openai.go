package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/netproxy"
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
		out[i].PromptContext = ""
		out[i].PromptSnapshots = nil
		out[i].Usage = nil
		out[i].Model = ""
		out[i].RewoundFrom = ""
		out[i].StopReason, out[i].RawStopReason = "", ""
		out[i].HostedSearch = nil
		out[i].Reasoning = ""

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
	out := make([]Message, 0, len(msgs))
	var pending []ToolCall
	answered := map[string]bool{}
	flush := func() {
		for _, call := range pending {
			if answered[call.ID] {
				continue
			}
			out = append(out, Message{
				Role:       "tool",
				Content:    "(interrupted before execution)",
				ToolCallID: call.ID,
				Name:       call.Function.Name,
			})
		}
		pending = nil
		answered = map[string]bool{}
	}
	for _, m := range msgs {
		if m.Role == "tool" {
			matched := false
			for _, call := range pending {
				if call.ID == m.ToolCallID && !answered[call.ID] {
					answered[call.ID] = true
					if m.Name == "" {
						m.Name = call.Function.Name
					}
					matched = true
					break
				}
			}
			if !matched {
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
			pending = m.ToolCalls
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
	BaseURL   string
	APIKey    string
	IsFullURL bool
	ModelsURL string
	Headers   http.Header
	HTTP      *http.Client

	MaxRetries int

	OnRetry func(RetryEvent)

	CacheKey                   string
	CacheRetention             string
	CacheSessionAffinity       bool
	CacheControlFormat         string
	SupportsLongCacheRetention bool
	SupportsPromptCacheKey     *bool
	CacheSessionAffinityFormat string
	ResponsesCacheOptions      bool
}

func (c *OpenAI) Clone() Client        { cp := *c; cp.Headers = c.Headers.Clone(); return &cp }
func (c *OpenAI) SetCacheKey(k string) { c.CacheKey = k }
func (c *OpenAI) Endpoint() string     { return c.BaseURL }

type CacheOptions struct {
	Retention             string
	SessionAffinity       bool
	ControlFormat         string
	SupportsLong          bool
	SupportsKey           *bool
	AffinityFormat        string
	ResponsesCacheOptions bool
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
	c.SupportsPromptCacheKey = nil
	if o.SupportsKey != nil {
		value := *o.SupportsKey
		c.SupportsPromptCacheKey = &value
	}
	c.CacheSessionAffinityFormat = o.AffinityFormat
	c.ResponsesCacheOptions = o.ResponsesCacheOptions
}

func (c *OpenAI) policy() retryPolicy { return newRetryPolicy(c.MaxRetries, c.OnRetry) }

func New(baseURL, apiKey string) *OpenAI {
	return NewWithOptions(baseURL, apiKey, false, "", nil)
}

func NewWithOptions(baseURL, apiKey string, full bool, modelsURL string, headers map[string]string) *OpenAI {
	h := make(http.Header)
	for k, v := range headers {
		h.Set(k, v)
	}
	return &OpenAI{
		BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, IsFullURL: full, ModelsURL: modelsURL, Headers: h,
		HTTP:           &http.Client{Transport: privacy.WrapTransport(netproxy.Transport(false)), Timeout: 10 * time.Minute},
		CacheRetention: "short",
	}
}

func (c *OpenAI) SetOnRetry(fn func(RetryEvent)) {
	c.OnRetry = fn
}

func (c *OpenAI) SetMaxRetries(n int) { c.MaxRetries = n }

// SetUseProxy selects the proxied or direct transport for this provider.
func (c *OpenAI) SetUseProxy(on bool) {
	h := *c.HTTP
	h.Transport = privacy.WrapTransport(netproxy.Transport(on))
	c.HTTP = &h
}

type Request struct {
	NativeWebSearch      bool      `json:"-"`
	NativeSearchProvider string    `json:"-"`
	Model                string    `json:"model"`
	Messages             []Message `json:"messages"`
	Tools                []Tool    `json:"tools,omitempty"`
	MaxTokens            int       `json:"max_tokens,omitempty"`
	ReasoningEffort      string    `json:"reasoning_effort,omitempty"`

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
	Reasoning        string `json:"reasoning"`
	ReasoningText    string `json:"reasoning_text"`
	Refusal          string `json:"refusal"`
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

	OutputModalities []string `json:"output_modalities,omitempty"`
	InputModalities  []string `json:"input_modalities,omitempty"`
}

func (mi ModelInfo) SupportsVision() bool {
	return slices.Contains(mi.InputModalities, "image")
}

func (c *OpenAI) Models(ctx context.Context) ([]ModelInfo, error) {
	return c.modelsWithHeaders(ctx, c.Headers)
}

func (c *OpenAI) modelsWithHeaders(ctx context.Context, headers http.Header) ([]ModelInfo, error) {
	endpoints := []string{c.modelCatalogURL()}
	if c.ModelsURL == "" && !c.IsFullURL {
		u, err := url.Parse(endpoints[0])
		if err == nil {
			root := strings.TrimSuffix(u.EscapedPath(), "/models")
			if !modelCatalogVersionSuffix.MatchString(root) {
				path := root + "/v1/models"
				u.Path, _ = url.PathUnescape(path)
				u.RawPath = path
				endpoints = append(endpoints, u.String())
			}
		}
	}
	for i, endpoint := range endpoints {
		models, err := c.modelsAtURL(ctx, endpoint, headers)
		if err == nil || i == len(endpoints)-1 || ctx.Err() != nil {
			return models, err
		}
		// Root relays may expose only /v1/models or serve an HTML landing page.
		var syntax *json.SyntaxError
		httpErr, ok := errors.AsType[*HTTPError](err)
		if !(ok && (httpErr.StatusCode == http.StatusNotFound || httpErr.StatusCode == http.StatusMethodNotAllowed)) && !errors.As(err, &syntax) {
			return nil, err
		}
	}
	return nil, nil
}

func (c *OpenAI) modelsAtURL(ctx context.Context, endpoint string, headers http.Header) ([]ModelInfo, error) {
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	for name, values := range headers {
		hr.Header[name] = append([]string(nil), values...)
	}
	if !hasHeader(headers, "Authorization") {
		hr.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
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

func hasHeader(headers http.Header, name string) bool {
	for key := range headers {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

func (c *OpenAI) modelCatalogURL() string {
	if c.ModelsURL != "" {
		return c.ModelsURL
	}
	if c.IsFullURL {
		return fullURLModelCatalog(c.BaseURL, "v1")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return strings.TrimRight(c.BaseURL, "/") + "/models"
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	for _, suffix := range []string{"/chat/completions", "/responses", "/response", "/messages", "/models"} {
		if strings.HasSuffix(strings.ToLower(path), suffix) {
			path = path[:len(path)-len(suffix)]
			break
		}
	}
	path += "/models"
	u.Path, _ = url.PathUnescape(path)
	u.RawPath = path
	return u.String()
}

var modelCatalogVersionSuffix = regexp.MustCompile(`(?i)/v\d+(?:beta)?$`)

// Full generation endpoints use LiveAgent's parent API root, not the generation URL.
func fullURLModelCatalog(endpoint, version string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
	u.ForceQuery = false
	path := strings.TrimRight(u.EscapedPath(), "/")
	lowerPath := strings.ToLower(path)
	root := ""
	for _, marker := range []string{"/v1/", "/v1beta/"} {
		if index := strings.Index(lowerPath, marker); index >= 0 {
			root = path[:index+len(marker)-1]
			break
		}
	}
	if root == "" {
		if strings.HasSuffix(lowerPath, "/chat/completions") {
			root = path[:len(path)-len("/chat/completions")]
		} else if index := strings.LastIndex(path, "/"); index > 0 {
			root = path[:index]
		}
	}
	root = modelCatalogVersionSuffix.ReplaceAllString(strings.TrimRight(root, "/"), "")
	if root == "" {
		root = "/"
	}
	if strings.HasSuffix(strings.ToLower(path), "/models") && strings.HasSuffix(strings.ToLower(root), "/"+strings.ToLower(version)) {
		return u.String()
	}
	if strings.HasSuffix(strings.ToLower(root), "/"+strings.ToLower(version)) {
		path = root + "/models"
	} else {
		path = strings.TrimRight(root, "/") + "/" + version + "/models"
	}
	u.Path, _ = url.PathUnescape(path)
	u.RawPath = path
	return u.String()
}

func clampCacheKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 64 {
		return key
	}
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:])
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
	if c.SupportsPromptCacheKey != nil && !*c.SupportsPromptCacheKey {
		req.PromptCacheKey = ""
	}
	req.PromptCacheRetention = ""
	if retention == "long" && c.SupportsLongCacheRetention {
		req.PromptCacheRetention = "24h"
	}
}

func (c *OpenAI) applyCacheHeaders(req *http.Request) {
	if c.CacheRetention == "none" {
		return
	}
	key, ok := req.Context().Value(cacheRequestKey{}).(string)
	if !ok {
		key = clampCacheKey(c.CacheKey)
	}
	if key == "" {
		return
	}
	format := c.CacheSessionAffinityFormat
	if format == "" && c.CacheSessionAffinity {
		switch CacheEndpointHost(c.BaseURL) {
		case "api.openai.com":
			format = "openai"
		case "openrouter.ai":
			format = "openrouter"
		}
	}
	set := func(name string) {
		if !hasHeader(req.Header, name) {
			req.Header.Set(name, key)
		}
	}
	switch format {
	case "openai":
		set("session_id")
		fallthrough
	case "openai-nosession":
		set("x-client-request-id")
		set("x-session-affinity")
	case "openrouter":
		set("x-session-id")
	}
}

func (c *OpenAI) Stream(ctx context.Context, req Request, onText, onThink func(string), onToolCall func(id, name, args string)) (Message, Usage, error) {
	if req.NativeWebSearch {
		return Message{}, Usage{}, fmt.Errorf("native web search is unsupported for Chat Completions; use Responses, Anthropic Messages, or Gemini")
	}
	req.Stream = true
	req.StreamOptions = &struct {
		IncludeUsage bool `json:"include_usage"`
	}{IncludeUsage: true}
	req.Messages = prepareRequestHistory(req.Messages, APIChatCompletions, c.Endpoint(), req.Model)
	if err := validateAttachments(req.Messages, false, "Chat Completions"); err != nil {
		return Message{}, Usage{}, err
	}
	ctx = c.cacheContext(ctx, req)
	c.applyCache(&req)
	body, err := c.chatBody(req)
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
	if msg.Replay != nil {
		msg.Replay.Model = req.Model
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
	var reasoning strings.Builder
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
		if d.Refusal != "" {
			return Message{}, usage, providerStreamError(nil, "Chat Completions refusal: "+d.Refusal)
		}
		if thought := firstNonEmpty(d.ReasoningContent, d.Reasoning, d.ReasoningText); thought != "" {
			reasoning.WriteString(thought)
			if onThink != nil {
				onThink(thought)
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
			if tc.Function.Name != "" && tc.Function.Name != cur.Function.Name {
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
	if finish == "content_filter" {
		return Message{}, usage, providerStreamError(nil, "Chat Completions content_filter")
	}
	if reasoning.Len() > 0 {
		raw, _ := json.Marshal(map[string]string{"text": reasoning.String()})
		msg.Replay = &ProviderReplay{API: APIChatCompletions, Endpoint: c.Endpoint(), Blocks: []json.RawMessage{raw}}
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
	req.Messages = prepareRequestHistory(req.Messages, APIChatCompletions, c.Endpoint(), req.Model)
	if err := validateAttachments(req.Messages, false, "Chat Completions"); err != nil {
		return "", Usage{}, err
	}
	ctx = c.cacheContext(ctx, req)
	c.applyCache(&req)
	body, err := c.chatBody(req)
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
				Refusal string `json:"refusal"`
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
	if out.Choices[0].FinishReason == "content_filter" || out.Choices[0].Message.Refusal != "" {
		return "", usage, providerStreamError(nil, "Chat Completions response was refused or filtered")
	}
	return out.Choices[0].Message.Content, usage, nil
}
