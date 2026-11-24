package ai

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/datapath"
	"github.com/Stack-Cairn/K-brain/internal/netproxy"
	"github.com/gofrs/flock"
)

var geminiThinkingLevelModel = regexp.MustCompile(`gemini-(?:[3-9]|\d{2,})(?:[.-]|$)|gemini-(?:pro|flash(?:-lite)?)-latest|robotics-er-2`)

// Gemini implements Google's native generateContent API while exposing the
// same provider-neutral Client interface as the other adapters.
type Gemini struct {
	BaseURL    string
	APIKey     string
	IsFullURL  bool
	ModelsURL  string
	Headers    http.Header
	HTTP       *http.Client
	MaxRetries int
	StreamIdle time.Duration
	OnRetry    func(RetryEvent)

	mu         sync.Mutex
	usedIDs    map[string]bool
	signatures map[string]string
}

func NewGemini(baseURL, apiKey string) *Gemini {
	return NewGeminiWithOptions(baseURL, apiKey, false, "", nil)
}

func NewGeminiWithOptions(baseURL, apiKey string, isFullURL bool, modelsURL string, headers map[string]string) *Gemini {
	h := make(http.Header)
	for key, value := range headers {
		h.Set(key, value)
	}
	return &Gemini{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, IsFullURL: isFullURL, ModelsURL: modelsURL, Headers: h, HTTP: &http.Client{Transport: netproxy.Transport(false), Timeout: 10 * time.Minute}, usedIDs: make(map[string]bool), signatures: make(map[string]string)}
}

func (c *Gemini) Clone() Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := Gemini{
		BaseURL: c.BaseURL, APIKey: c.APIKey, IsFullURL: c.IsFullURL, ModelsURL: c.ModelsURL, Headers: c.Headers.Clone(),
		HTTP:       c.HTTP,
		MaxRetries: c.MaxRetries,
		StreamIdle: c.StreamIdle,
		OnRetry:    c.OnRetry,
	}
	cp.usedIDs = make(map[string]bool, len(c.usedIDs))
	for id, used := range c.usedIDs {
		cp.usedIDs[id] = used
	}
	cp.signatures = make(map[string]string, len(c.signatures))
	for id, signature := range c.signatures {
		cp.signatures[id] = signature
	}
	return &cp
}
func (c *Gemini) SetCacheKey(string)             {}
func (c *Gemini) Endpoint() string               { return c.BaseURL }
func (c *Gemini) SetMaxRetries(n int)            { c.MaxRetries = n }
func (c *Gemini) SetOnRetry(fn func(RetryEvent)) { c.OnRetry = fn }

// SetUseProxy selects the proxied or direct transport for this provider.
func (c *Gemini) SetUseProxy(on bool) {
	h := *c.HTTP
	h.Transport = netproxy.Transport(on)
	c.HTTP = &h
}

func (c *Gemini) Models(ctx context.Context) ([]ModelInfo, error) {
	endpoint := c.ModelsURL
	if endpoint == "" {
		if c.IsFullURL {
			endpoint = fullURLModelCatalog(c.BaseURL, "v1beta")
		} else if isVertexPublisherBaseURL(c.BaseURL) {
			if !hasHeader(c.Headers, "authorization") {
				return nil, fmt.Errorf("Vertex AI Express Mode does not expose a model list endpoint; add Gemini model IDs manually")
			}
			u, err := url.Parse(c.BaseURL)
			if err != nil {
				return nil, err
			}
			endpoint = u.Scheme + "://" + u.Host + "/v1beta1/publishers/google/models"
		} else if strings.HasSuffix(strings.ToLower(strings.TrimRight(c.BaseURL, "/")), "/models") {
			endpoint = strings.TrimRight(c.BaseURL, "/")
		} else {
			endpoint = strings.TrimRight(c.BaseURL, "/") + strings.Split(geminiPath(c.BaseURL, "", false), "/models/")[0] + "/models"
		}
	}
	type geminiModelEntry struct {
		Name             string `json:"name"`
		InputTokenLimit  int    `json:"inputTokenLimit"`
		OutputTokenLimit int    `json:"outputTokenLimit"`
	}
	var out []ModelInfo
	pageToken := ""
	for page := 0; page < 20; page++ {
		pageURL := endpoint
		if pageToken != "" {
			parsed, err := url.Parse(endpoint)
			if err != nil {
				return nil, err
			}
			q := parsed.Query()
			q.Set("pageToken", pageToken)
			parsed.RawQuery = q.Encode()
			pageURL = parsed.String()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return nil, err
		}
		for name, values := range c.Headers {
			req.Header[name] = append([]string(nil), values...)
		}
		if !hasHeader(c.Headers, "x-goog-api-key") && !hasHeader(c.Headers, "authorization") {
			req.Header.Set("x-goog-api-key", c.APIKey)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return nil, newHTTPError(resp, string(b))
		}
		var payload struct {
			Models          []geminiModelEntry `json:"models"`
			PublisherModels []geminiModelEntry `json:"publisherModels"`
			NextPageToken   string             `json:"nextPageToken"`
		}
		err = json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		entries := payload.Models
		if len(entries) == 0 {
			entries = payload.PublisherModels
		}
		for _, model := range entries {
			id := strings.TrimPrefix(model.Name, "models/")
			if idx := strings.LastIndex(id, "/models/"); idx >= 0 {
				id = id[idx+len("/models/"):]
			}
			out = append(out, ModelInfo{ID: id, ContextLength: model.InputTokenLimit, MaxCompletionTokens: model.OutputTokenLimit})
		}
		if len(payload.PublisherModels) == 0 || payload.NextPageToken == "" {
			break
		}
		pageToken = payload.NextPageToken
	}
	return out, nil
}

func (c *Gemini) Complete(ctx context.Context, req Request) (string, Usage, error) {
	resp, err := c.do(ctx, req, false)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()
	var result geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", Usage{}, err
	}
	usage := result.usage()
	var msg Message
	msg.Role = "assistant"
	if _, err := result.emit(&msg, nil, nil); err != nil {
		return "", usage, err
	}
	if len(result.Candidates) > 0 && result.Candidates[0].FinishReason == "MAX_TOKENS" {
		return msg.Content, usage, &OutputLimitError{Reason: "MAX_TOKENS"}
	}
	return msg.Content, usage, nil
}

func (c *Gemini) Stream(ctx context.Context, req Request, onText, onThink func(string), onToolCall func(id, name, args string)) (Message, Usage, error) {
	return retryStalled(ctx, newRetryPolicy(c.MaxRetries, c.OnRetry), streamCallbacks{onText, onThink, onToolCall}, func(cb streamCallbacks) (Message, Usage, error) {
		return c.streamAttempt(ctx, req, cb.text, cb.think, cb.tool)
	})
}

// newGeminiChunkReader yields JSON chunks from either an SSE stream (`data: {...}`)
// or Vertex AI / Gemini's native JSON-array stream (`[{...}, {...}]`).
func newGeminiChunkReader(r io.Reader) func() (string, string, bool, error) {
	br := bufio.NewReader(r)
	for {
		b, err := br.Peek(1)
		if err != nil || (b[0] != ' ' && b[0] != '\t' && b[0] != '\r' && b[0] != '\n') {
			break
		}
		_, _ = br.ReadByte()
	}
	if b, err := br.Peek(1); err == nil && b[0] == '[' {
		lr := &io.LimitedReader{R: br, N: maxSSEEventSize + 1}
		dec := json.NewDecoder(lr)
		tok, tokErr := dec.Token()
		return func() (string, string, bool, error) {
			if tokErr != nil {
				return "", "", false, tokErr
			}
			if d, ok := tok.(json.Delim); !ok || d != '[' {
				return "", "", false, nonRetryable{fmt.Errorf("invalid Gemini streaming response: expected JSON array")}
			}
			lr.N = maxSSEEventSize + 1
			if !dec.More() {
				if _, err := dec.Token(); err != nil {
					return "", "", false, err
				}
				return "", "", false, io.ErrUnexpectedEOF
			}
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				if lr.N <= 0 {
					return "", "", false, nonRetryable{fmt.Errorf("Gemini JSON stream chunk exceeds %d bytes", maxSSEEventSize)}
				}
				return "", "", false, err
			}
			if len(raw) > maxSSEEventSize {
				return "", "", false, nonRetryable{fmt.Errorf("Gemini JSON stream chunk exceeds %d bytes", maxSSEEventSize)}
			}
			return string(raw), "", true, nil
		}
	}
	sse := newSSEReader(br)
	return func() (string, string, bool, error) {
		event, ok := sse.next()
		if !ok {
			return "", "", false, sse.endError("Gemini")
		}
		return event.Data, event.Type, true, nil
	}
}

// streamAttempt is one Gemini streaming request; Stream retries it when it
// stalls before any output.
func (c *Gemini) streamAttempt(ctx context.Context, req Request, onText, onThink func(string), onToolCall func(id, name, args string)) (Message, Usage, error) {
	sctx, idleGuard := newIdleStream(ctx, idleOr(c.StreamIdle), "Gemini")
	defer idleGuard.close()
	resp, err := c.do(sctx, req, true)
	if err != nil {
		return Message{}, Usage{}, idleGuard.stalled(err)
	}
	defer resp.Body.Close()
	nextChunk := newGeminiChunkReader(idleGuard.wrap(resp.Body))
	var msg Message
	msg.Role = "assistant"
	var thought thoughtText
	onThink = thought.wrap(onThink)
	search := newSearchStream(ctx, "gemini")
	var usage Usage
	var calls []ToolCall
	for {
		data, eventType, ok, chunkErr := nextChunk()
		if !ok {
			return Message{}, usage, idleGuard.stalled(chunkErr)
		}
		search.accept(data, eventType)
		var result geminiResponse
		if err := decodeStreamEvent(data, &result); err != nil {
			return Message{}, usage, err
		}
		if result.Error != nil {
			return Message{}, usage, providerStreamError(result.Error, "Gemini request failed")
		}
		usage = result.usage()
		partCalls, err := result.emit(&msg, onText, onThink)
		if err != nil {
			return Message{}, usage, err
		}
		for _, emitted := range partCalls {
			call := emitted.call
			call.ID, err = c.canonicalCallID(call.ID)
			if err != nil {
				return Message{}, usage, err
			}
			if err := c.rememberSignature(ctx, req.Model, call.ID, emitted.thoughtSignature); err != nil {
				return Message{}, usage, fmt.Errorf("persist Gemini tool signature: %w", err)
			}
			calls = append(calls, call)
			if onToolCall != nil && !req.hostedSearchCall(call.Function.Name) {
				onToolCall(call.ID, call.Function.Name, call.Function.Arguments)
			}
		}
		if len(result.Candidates) > 0 && result.Candidates[0].FinishReason != "" {
			msg.Reasoning = thought.String()
			search.finish(false)
			msg.HostedSearch = append([]HostedSearch(nil), search.blocks...)
			if req.NativeWebSearch {
				recoverNativeSearchCalls(req, &msg, onText)
				calls = filterNativeSearchCalls(req, calls)
			}
			return finishMessage(msg, calls, normalizeGeminiFinishReason(result.Candidates[0].FinishReason), onText), usage, nil
		}
	}
}

type geminiResponse struct {
	Candidates     []geminiCandidate `json:"candidates"`
	UsageMetadata  geminiUsage       `json:"usageMetadata"`
	Error          json.RawMessage   `json:"error"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
}
type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
}
type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}
type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}
type geminiFunctionCall struct {
	ID               string         `json:"id,omitempty"`
	Name             string         `json:"name"`
	Args             map[string]any `json:"args"`
	ThoughtSignature string         `json:"thoughtSignature,omitempty"`
}
type geminiFunctionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}
type geminiUsage struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
}

func (u geminiUsage) normalized() Usage {
	return Usage{PromptTokens: u.PromptTokenCount, CompletionTokens: u.CandidatesTokenCount + u.ThoughtsTokenCount, PromptCacheHitTokens: u.CachedContentTokenCount}
}
func (r geminiResponse) usage() Usage { return r.UsageMetadata.normalized() }

type geminiEmittedCall struct {
	call             ToolCall
	thoughtSignature string
}

func (r geminiResponse) emit(msg *Message, onText, onThink func(string)) ([]geminiEmittedCall, error) {
	if len(r.Error) > 0 && string(r.Error) != "null" {
		return nil, providerStreamError(r.Error, "Gemini request failed")
	}
	if reason := r.PromptFeedback.BlockReason; reason != "" && reason != "BLOCK_REASON_UNSPECIFIED" {
		return nil, providerStreamError(nil, "Gemini prompt blocked: "+reason)
	}
	if len(r.Candidates) == 0 {
		return nil, nil
	}
	if reason := r.Candidates[0].FinishReason; reason != "" && reason != "STOP" && reason != "MAX_TOKENS" {
		return nil, providerStreamError(nil, "Gemini generation stopped: "+reason)
	}
	var calls []geminiEmittedCall
	for _, p := range r.Candidates[0].Content.Parts {
		if p.Text != "" {
			if p.Thought {
				if onThink != nil {
					onThink(p.Text)
				}
			} else {
				msg.Content += p.Text
				if onText != nil {
					onText(p.Text)
				}
			}
		}
		if p.FunctionCall != nil {
			args, err := json.Marshal(p.FunctionCall.Args)
			if err != nil {
				return nil, err
			}
			call := ToolCall{ID: p.FunctionCall.ID, Type: "function"}
			call.Function.Name, call.Function.Arguments = p.FunctionCall.Name, string(args)
			calls = append(calls, geminiEmittedCall{call: call, thoughtSignature: firstNonEmpty(p.FunctionCall.ThoughtSignature, p.ThoughtSignature)})
		}
	}
	return calls, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (c *Gemini) canonicalCallID(nativeID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if nativeID != "" && !c.usedIDs[nativeID] {
		c.usedIDs[nativeID] = true
		return nativeID, nil
	}
	for {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", fmt.Errorf("generate Gemini tool call ID: %w", err)
		}
		id := "gemini-call-" + hex.EncodeToString(random[:])
		if !c.usedIDs[id] {
			c.usedIDs[id] = true
			return id, nil
		}
	}
}

// Signatures are provider-private: canonical transcripts contain only call IDs.

func (c *Gemini) signaturePath(model string) (string, error) {
	dir, err := datapath.UserDir()
	if err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte(strings.TrimRight(c.BaseURL, "/") + "\x00" + model))
	return filepath.Join(dir, "cache", "gemini-signatures", hex.EncodeToString(key[:])+".json"), nil
}

func readGeminiSignatures(path string) (map[string]string, error) {
	records := make(map[string]string)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return records, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	if records == nil {
		return nil, fmt.Errorf("invalid Gemini signature cache")
	}
	return records, nil
}

func (c *Gemini) rememberSignature(ctx context.Context, model, id, signature string) error {
	if id == "" || signature == "" {
		return nil
	}
	path, err := c.signaturePath(model)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock := flock.New(path+".lock", flock.SetPermissions(0o600))
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return ctx.Err()
	}
	defer lock.Unlock()
	records, err := readGeminiSignatures(path)
	if err != nil {
		return err
	}
	if old := records[id]; old != "" && old != signature {
		return fmt.Errorf("conflicting Gemini signature for call %q", id)
	}
	records[id] = signature
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".signature-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	c.mu.Lock()
	c.signatures[model+"\x00"+id] = signature
	c.mu.Unlock()
	return nil
}

func (c *Gemini) loadSignatures(model string) error {
	path, err := c.signaturePath(model)
	if err != nil {
		return err
	}
	records, err := readGeminiSignatures(path)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, signature := range records {
		c.signatures[model+"\x00"+id] = signature
	}
	return nil
}

func (c *Gemini) signature(model, id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.signatures[model+"\x00"+id]
}

func (c *Gemini) do(ctx context.Context, req Request, stream bool) (*http.Response, error) {
	req.Messages = prepareRequestHistory(req.Messages, APIGemini, c.Endpoint(), req.Model)
	if err := validateGeminiAttachments(req.Messages); err != nil {
		return nil, err
	}
	c.reserveHistoryIDs(req.Messages)
	if err := c.loadSignatures(req.Model); err != nil {
		return nil, fmt.Errorf("load Gemini tool signatures: %w", err)
	}
	body, err := json.Marshal(c.geminiPayloadWithState(req))
	if err != nil {
		return nil, err
	}
	path := geminiPath(c.BaseURL, req.Model, stream)
	if c.IsFullURL {
		path = ""
	}
	var response *http.Response
	err = newRetryPolicy(c.MaxRetries, c.OnRetry).run(ctx, func() error {
		u := strings.TrimRight(c.BaseURL, "/") + path
		parsed, parseErr := url.Parse(u)
		if parseErr != nil {
			return nonRetryable{parseErr}
		}
		q := parsed.Query()
		if stream && c.IsFullURL && strings.Contains(parsed.Path, ":streamGenerateContent") && q.Get("alt") == "" {
			q.Set("alt", "sse")
		}
		if !hasHeader(c.Headers, "x-goog-api-key") {
			q.Set("key", c.APIKey)
		}
		parsed.RawQuery = q.Encode()
		reqHTTP, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(body))
		if reqErr != nil {
			return nonRetryable{reqErr}
		}
		reqHTTP.Header.Set("Content-Type", "application/json")
		for name, values := range c.Headers {
			reqHTTP.Header[name] = append([]string(nil), values...)
		}
		if stream {
			reqHTTP.Header.Set("Accept", "text/event-stream")
		}
		response, reqErr = c.HTTP.Do(reqHTTP)
		if reqErr != nil {
			return reqErr
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			defer response.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
			return newHTTPError(response, string(b))
		}
		return nil
	}, nil)
	return response, err
}

func isOfficialGeminiEndpoint(baseURL string) bool {
	u, err := url.Parse(baseURL)
	return err == nil && u.Hostname() == "generativelanguage.googleapis.com"
}

func isVertexPublisherBaseURL(baseURL string) bool {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	isVertexHost := host == "aiplatform.googleapis.com" || strings.HasSuffix(host, "-aiplatform.googleapis.com")
	return isVertexHost && strings.HasSuffix(strings.ToLower(u.Path), "/publishers/google")
}

func geminiPath(baseURL, model string, stream bool) string {
	trimmed := strings.TrimRight(baseURL, "/")
	prefix := "/v1beta"
	modelsPrefix := "/models/"
	if strings.HasSuffix(strings.ToLower(trimmed), "/models") {
		prefix = ""
		modelsPrefix = "/"
	} else if modelCatalogVersionSuffix.MatchString(trimmed) || isVertexPublisherBaseURL(trimmed) {
		prefix = ""
	}
	path := prefix + modelsPrefix + url.PathEscape(model) + ":generateContent"
	if stream {
		path = prefix + modelsPrefix + url.PathEscape(model) + ":streamGenerateContent?alt=sse"
	}
	return path
}

func normalizeGeminiFinishReason(reason string) string {
	switch strings.ToUpper(reason) {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return "other"
	case "MALFORMED_FUNCTION_CALL":
		return "tool_calls"
	default:
		return strings.ToLower(reason)
	}
}

func (c *Gemini) reserveHistoryIDs(messages []Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if call.ID != "" {
				c.usedIDs[call.ID] = true
			}
		}
	}
}

func geminiPayload(req Request) map[string]any {
	return NewGemini("", "").geminiPayloadWithState(req)
}

func (c *Gemini) geminiPayloadWithState(req Request) map[string]any {
	contents := make([]map[string]any, 0, len(req.Messages))
	var system []any
	for _, m := range req.Messages {
		parts := make([]any, 0)
		if m.Content != "" {
			parts = append(parts, map[string]any{"text": m.Content})
		}
		for _, p := range m.Parts {
			if p.Type == "text" {
				parts = append(parts, map[string]any{"text": p.Text})
				continue
			}
			if p.Type != "image_url" {
				return nil
			}
			mimeType, data, isData, err := AttachmentData(p)
			if err != nil || !isData || !strings.HasPrefix(mimeType, "image/") {
				return nil
			}
			parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": mimeType, "data": data}})
		}
		for _, tc := range m.ToolCalls {
			var args any
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
			functionCall := map[string]any{"name": tc.Function.Name, "args": args}
			if tc.ID != "" {
				functionCall["id"] = tc.ID
			}
			part := map[string]any{"functionCall": functionCall}
			if signature := c.signature(req.Model, tc.ID); signature != "" {
				part["thoughtSignature"] = signature
			}
			parts = append(parts, part)
		}
		if m.Role == "system" || m.Role == "developer" {
			system = append(system, parts...)
			continue
		}
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		if m.Role == "tool" {
			functionResponse := map[string]any{"name": m.Name, "response": map[string]any{"content": m.TextContent()}}
			if m.ToolCallID != "" {
				functionResponse["id"] = m.ToolCallID
			}
			var images []any
			for _, part := range parts {
				if block, ok := part.(map[string]any); ok && block["inlineData"] != nil {
					images = append(images, part)
				}
			}
			parts = append([]any{map[string]any{"functionResponse": functionResponse}}, images...)
			role = "user"
		}
		if len(parts) == 0 {
			continue
		}
		if len(contents) > 0 && contents[len(contents)-1]["role"] == role {
			last := contents[len(contents)-1]
			last["parts"] = append(last["parts"].([]any), parts...)
		} else {
			contents = append(contents, map[string]any{"role": role, "parts": parts})
		}
	}
	payload := map[string]any{"contents": contents}
	if len(system) > 0 {
		payload["systemInstruction"] = map[string]any{"parts": system}
	}
	if req.MaxTokens > 0 || req.Temperature != nil || req.TopP != nil || req.ReasoningEffort != "" {
		cfg := map[string]any{}
		if req.MaxTokens > 0 {
			cfg["maxOutputTokens"] = req.MaxTokens
		}
		if req.Temperature != nil {
			cfg["temperature"] = *req.Temperature
		}
		if req.TopP != nil {
			cfg["topP"] = *req.TopP
		}
		if req.ReasoningEffort != "" {
			thinking := map[string]any{"includeThoughts": !reasoningDisabled(req.ReasoningEffort)}
			model := strings.ToLower(req.Model)
			if c.IsFullURL {
				if _, after, ok := strings.Cut(strings.ToLower(c.BaseURL), "/models/"); ok {
					if end := strings.IndexAny(after, ":?/"); end >= 0 {
						after = after[:end]
					}
					if after != "" {
						model = after
					}
				}
			}
			// Gemini 3/4+ and latest aliases require thinkingLevel; keep thinkingBudget for Gemini 2.x and opaque aliases.
			if geminiThinkingLevelModel.MatchString(model) {
				level := req.ReasoningEffort
				supportsMinimal := !strings.Contains(model, "gemini-4") &&
					(strings.Contains(model, "gemini-3-flash") ||
						strings.Contains(model, "gemini-3.1-flash") ||
						strings.Contains(model, "gemini-3.5-flash") ||
						strings.Contains(model, "gemini-3.6-flash") ||
						strings.Contains(model, "flash-lite") ||
						strings.Contains(model, "robotics-er-2"))
				supportsXHigh := strings.Contains(model, "gemini-4-neon") ||
					strings.Contains(model, "gemini-4-hydrogen")
				if reasoningDisabled(level) || level == "minimal" {
					if supportsMinimal {
						level = "minimal"
					} else {
						level = "low"
					}
				}
				if level == "medium" && (strings.Contains(model, "gemini-3-pro") || strings.Contains(model, "-image")) {
					level = "high"
				}
				if level == "xhigh" || level == "max" {
					if supportsXHigh {
						level = "xhigh"
					} else {
						level = "high"
					}
				}
				thinking["thinkingLevel"] = strings.ToUpper(level)
			} else {
				budget := -1
				switch req.ReasoningEffort {
				case "off", "none":
					budget = 0
				case "minimal", "low":
					budget = 1024
				case "medium":
					budget = 4096
				case "high":
					budget = 8192
				}
				if strings.Contains(model, "2.5-pro") && budget == 0 {
					budget = 128
				}
				if req.MaxTokens > 0 && budget > req.MaxTokens {
					budget = req.MaxTokens
				}
				thinking["thinkingBudget"] = budget
			}
			cfg["thinkingConfig"] = thinking
		}
		payload["generationConfig"] = cfg
	}
	if len(req.Tools) > 0 {
		decls := make([]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			var params any
			_ = json.Unmarshal(t.Function.Parameters, &params)
			decls = append(decls, map[string]any{"name": t.Function.Name, "description": t.Function.Description, "parametersJsonSchema": params})
		}
		payload["tools"] = []any{map[string]any{"functionDeclarations": decls}}
	}
	if req.NativeWebSearch {
		if len(req.Tools) > 0 && !isOfficialGeminiEndpoint(c.BaseURL) {
			return payload
		}
		tools, _ := payload["tools"].([]any)
		tools = append(tools, map[string]any{"google_search": map[string]any{}})
		payload["tools"] = tools
	}
	return payload
}
