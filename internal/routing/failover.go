package routing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

const (
	DefaultStreamAttempts   = 6
	DefaultMaxSwitches      = 3
	DefaultFailureThreshold = 4
	DefaultCooldown         = 60 * time.Second
)

type RetryPolicy struct {
	Mode       string
	MaxRetries int
}

type FailoverConfig struct {
	MaxSwitches      int
	FailureThreshold int
	Cooldown         time.Duration
}

type FailoverSettings struct {
	Retry    RetryPolicy
	Failover FailoverConfig
}

func DefaultFailoverSettings() FailoverSettings {
	return FailoverSettings{
		Retry: RetryPolicy{Mode: "default", MaxRetries: DefaultStreamAttempts - 1},
		Failover: FailoverConfig{
			MaxSwitches: DefaultMaxSwitches, FailureThreshold: DefaultFailureThreshold, Cooldown: DefaultCooldown,
		},
	}
}

// ParseProviderRuntimeSettings reads the existing provider RetryPolicy map. It
// keeps failover runtime settings out of config.Provider so settings ownership
// remains with the existing provider settings contract.
func ParseProviderRuntimeSettings(provider config.Provider) (FailoverSettings, error) {
	settings := DefaultFailoverSettings()
	if provider.RetryPolicy == nil {
		return settings, nil
	}
	if raw, ok := provider.RetryPolicy["mode"]; ok {
		mode, ok := raw.(string)
		if !ok || (mode != "default" && mode != "off" && mode != "custom") {
			return settings, fmt.Errorf("retryPolicy.mode must be default, off, or custom")
		}
		settings.Retry.Mode = mode
	}
	if raw, ok := provider.RetryPolicy["maxRetries"]; ok {
		n, err := runtimeInt(raw)
		if err != nil || n < 0 {
			return settings, fmt.Errorf("retryPolicy.maxRetries must be a non-negative integer")
		}
		settings.Retry.MaxRetries = n
	}
	failover := provider.RetryPolicy
	if nested, ok := provider.RetryPolicy["failover"].(map[string]any); ok {
		failover = nested
	}
	if raw, ok := failover["maxSwitches"]; ok {
		n, err := runtimeInt(raw)
		if err != nil || n < 0 {
			return settings, fmt.Errorf("retryPolicy.failover.maxSwitches must be a non-negative integer")
		}
		settings.Failover.MaxSwitches = n
	}
	if raw, ok := failover["failureThreshold"]; ok {
		n, err := runtimeInt(raw)
		if err != nil || n < 1 {
			return settings, fmt.Errorf("retryPolicy.failover.failureThreshold must be a positive integer")
		}
		settings.Failover.FailureThreshold = n
	}
	if raw, ok := failover["cooldownSeconds"]; ok {
		n, err := runtimeInt(raw)
		if err != nil || n < 1 {
			return settings, fmt.Errorf("retryPolicy.failover.cooldownSeconds must be a positive integer")
		}
		settings.Failover.Cooldown = time.Duration(n) * time.Second
	}
	return settings, nil
}

func runtimeInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		if n != float64(int(n)) {
			return 0, errors.New("not an integer")
		}
		return int(n), nil
	case jsonNumber:
		return strconv.Atoi(string(n))
	default:
		return 0, errors.New("not an integer")
	}
}

type jsonNumber string

func providerAttempts(policy RetryPolicy) int {
	switch policy.Mode {
	case "off":
		return 1
	case "custom":
		return policy.MaxRetries + 1
	default:
		return DefaultStreamAttempts
	}
}

func applyRetryPolicy(client ai.Client, policy RetryPolicy) {
	attempts := providerAttempts(policy)
	if setter, ok := client.(interface{ SetMaxRetries(int) }); ok {
		setter.SetMaxRetries(attempts)
	}
}

type breakerEntry struct {
	failures int
	openedAt time.Time
}

var breakerState = struct {
	sync.Mutex
	entries map[string]breakerEntry
}{entries: make(map[string]breakerEntry)}

func ResetFailoverBreakers() {
	breakerState.Lock()
	breakerState.entries = make(map[string]breakerEntry)
	breakerState.Unlock()
}

func breakerKey(provider, model string) string { return provider + "\x00" + model }

func targetAvailable(key string, cooldown time.Duration, now time.Time) bool {
	breakerState.Lock()
	defer breakerState.Unlock()
	entry, ok := breakerState.entries[key]
	return !ok || entry.openedAt.IsZero() || now.Sub(entry.openedAt) >= cooldown
}

func recordTarget(key string, success, eligible bool, threshold int, now time.Time) {
	breakerState.Lock()
	defer breakerState.Unlock()
	if success {
		delete(breakerState.entries, key)
		return
	}
	if !eligible {
		return
	}
	entry := breakerState.entries[key]
	entry.failures++
	if entry.failures >= threshold {
		entry.openedAt = now
	}
	breakerState.entries[key] = entry
}

func providerVendor(name string, provider config.Provider) string {
	if provider.Metadata != nil {
		if vendor, ok := provider.Metadata["vendor"].(string); ok && strings.TrimSpace(vendor) != "" {
			return strings.ToLower(strings.TrimSpace(vendor))
		}
	}
	if strings.TrimSpace(provider.Type) != "" {
		return strings.ToLower(strings.TrimSpace(provider.Type))
	}
	return strings.ToLower(strings.TrimSpace(provider.API))
}

type failoverCandidate struct {
	name      string
	provider  config.Provider
	client    ai.Client
	settings  FailoverSettings
	maxOutput int
}

// request caps output without mutating the request reused by other candidates.
func (c failoverCandidate) request(req ai.Request) ai.Request {
	if c.maxOutput > 0 && (req.MaxTokens <= 0 || req.MaxTokens > c.maxOutput) {
		req.MaxTokens = c.maxOutput
	}
	return req
}

// FailoverClient is the backend-owned provider queue. It buffers callbacks
// until the first visible delta, so discarded candidates cannot duplicate UI
// output or transcript events.
type FailoverClient struct {
	candidates  []failoverCandidate
	primary     int
	maxSwitches int
	cacheKey    string
	onRetry     func(ai.RetryEvent)
}

func (c *FailoverClient) Models(ctx context.Context) ([]ai.ModelInfo, error) {
	if len(c.candidates) == 0 {
		return nil, errors.New("no provider candidates")
	}
	return c.candidates[c.primary].client.Models(ctx)
}

func (c *FailoverClient) Clone() ai.Client {
	out := &FailoverClient{primary: c.primary, maxSwitches: c.maxSwitches, cacheKey: c.cacheKey, onRetry: c.onRetry}
	out.candidates = make([]failoverCandidate, len(c.candidates))
	for i, candidate := range c.candidates {
		candidate.client = candidate.client.Clone()
		if c.cacheKey != "" {
			candidate.client.SetCacheKey(c.cacheKey)
		}
		if setter, ok := candidate.client.(interface{ SetOnRetry(func(ai.RetryEvent)) }); ok {
			setter.SetOnRetry(c.onRetry)
		}
		out.candidates[i] = candidate
	}
	return out
}

func (c *FailoverClient) SetCacheKey(key string) {
	c.cacheKey = key
	for _, candidate := range c.candidates {
		candidate.client.SetCacheKey(key)
	}
}

func (c *FailoverClient) Endpoint() string {
	if len(c.candidates) == 0 {
		return ""
	}
	return c.candidates[c.primary].client.Endpoint()
}

func (c *FailoverClient) SetOnRetry(fn func(ai.RetryEvent)) {
	c.onRetry = fn
	for _, candidate := range c.candidates {
		if setter, ok := candidate.client.(interface{ SetOnRetry(func(ai.RetryEvent)) }); ok {
			setter.SetOnRetry(fn)
		}
	}
}

func (c *FailoverClient) Complete(ctx context.Context, req ai.Request) (string, ai.Usage, error) {
	plan := c.plan(req.Model)
	var last error
	for i, candidate := range plan {
		ai.ObserveRuntimeDiagnostic(ctx, ai.RuntimeDiagnostic{Kind: "attempt", Provider: candidate.name, Endpoint: candidate.client.Endpoint(), Attempt: i + 1, TargetIndex: i})
		previousErr := last
		text, usage, err := candidate.client.Complete(ctx, candidate.request(req))
		if i > 0 {
			errorText := ""
			if previousErr != nil {
				errorText = previousErr.Error()
			}
			ai.ObserveRuntimeDiagnostic(ctx, ai.RuntimeDiagnostic{Kind: "failover", From: plan[i-1].name, To: candidate.name, Provider: candidate.name, Endpoint: candidate.client.Endpoint(), Attempt: i, TargetIndex: i, Error: errorText})
		}
		if err == nil {
			recordTarget(breakerKey(candidate.name, req.Model), true, false, candidate.settings.Failover.FailureThreshold, time.Now())
			return text, usage, nil
		}
		last = err
		eligible := failoverEligible(err)
		recordTarget(breakerKey(candidate.name, req.Model), false, eligible, candidate.settings.Failover.FailureThreshold, time.Now())
		if !eligible || i+1 >= c.maxAttempts(candidate.settings) || ctx.Err() != nil {
			break
		}
	}
	return "", ai.Usage{}, last
}

func (c *FailoverClient) Stream(ctx context.Context, req ai.Request, onText, onThink func(string), onTool func(string, string, string)) (ai.Message, ai.Usage, error) {
	plan := c.plan(req.Model)
	var lastMessage ai.Message
	var lastUsage ai.Usage
	var lastErr error
	for i, candidate := range plan {
		ai.ObserveRuntimeDiagnostic(ctx, ai.RuntimeDiagnostic{Kind: "attempt", Provider: candidate.name, Endpoint: candidate.client.Endpoint(), Attempt: i + 1, TargetIndex: i})
		previousErr := lastErr
		committed := false
		var bufferedText, bufferedThink []string
		var bufferedTools [][3]string
		flush := func() {
			for _, value := range bufferedText {
				if onText != nil {
					onText(value)
				}
			}
			for _, value := range bufferedThink {
				if onThink != nil {
					onThink(value)
				}
			}
			for _, value := range bufferedTools {
				if onTool != nil {
					onTool(value[0], value[1], value[2])
				}
			}
			bufferedText, bufferedThink, bufferedTools = nil, nil, nil
		}
		mark := func() {
			if !committed {
				committed = true
				flush()
			}
		}
		message, usage, err := candidate.client.Stream(ctx, candidate.request(req),
			func(delta string) {
				if committed {
					if onText != nil {
						onText(delta)
					}
					return
				}
				bufferedText = append(bufferedText, delta)
				mark()
			},
			func(delta string) {
				if committed {
					if onThink != nil {
						onThink(delta)
					}
					return
				}
				bufferedThink = append(bufferedThink, delta)
				mark()
			},
			func(id, name, args string) {
				if committed {
					if onTool != nil {
						onTool(id, name, args)
					}
					return
				}
				bufferedTools = append(bufferedTools, [3]string{id, name, args})
				mark()
			},
		)
		lastMessage, lastUsage, lastErr = message, usage, err
		if i > 0 {
			errorText := ""
			if previousErr != nil {
				errorText = previousErr.Error()
			}
			ai.ObserveRuntimeDiagnostic(ctx, ai.RuntimeDiagnostic{Kind: "failover", From: plan[i-1].name, To: candidate.name, Provider: candidate.name, Endpoint: candidate.client.Endpoint(), Attempt: i, TargetIndex: i, Error: errorText})
		}
		if err == nil {
			flush()
			recordTarget(breakerKey(candidate.name, req.Model), true, false, candidate.settings.Failover.FailureThreshold, time.Now())
			return message, usage, nil
		}
		eligible := failoverEligible(err)
		recordTarget(breakerKey(candidate.name, req.Model), false, eligible, candidate.settings.Failover.FailureThreshold, time.Now())
		if committed || !eligible || ctx.Err() != nil || i+1 >= c.maxAttempts(candidate.settings) {
			break
		}
		// Buffered callbacks are intentionally discarded before the next candidate.
	}
	return lastMessage, lastUsage, lastErr
}

func (c *FailoverClient) plan(model string) []failoverCandidate {
	if len(c.candidates) == 0 {
		return nil
	}
	now := time.Now()
	plan := make([]failoverCandidate, 0, len(c.candidates))
	for _, candidate := range c.candidates {
		if targetAvailable(breakerKey(candidate.name, model), candidate.settings.Failover.Cooldown, now) {
			plan = append(plan, candidate)
		}
	}
	if len(plan) == 0 {
		return []failoverCandidate{c.candidates[c.primary]}
	}
	return plan
}

func (c *FailoverClient) maxAttempts(settings FailoverSettings) int {
	switches := c.maxSwitches
	if switches == 0 {
		switches = settings.Failover.MaxSwitches
	}
	max := switches + 1
	if max > len(c.candidates) {
		max = len(c.candidates)
	}
	if max < 1 {
		max = 1
	}
	return max
}

func failoverEligible(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var httpErr *ai.HTTPError
	if errors.As(err, &httpErr) {
		code := httpErr.StatusCode
		if code == 400 || code == 413 || code == 422 {
			return false
		}
		return code == 401 || code == 402 || code == 403 || code == 404 || code == 408 || code == 409 || code == 425 || code == 429 || code >= 500
	}
	text := strings.ToLower(err.Error())
	for _, phrase := range []string{"context length", "context window", "prompt is too long", "invalid request", "unsupported media", "payload too large"} {
		if strings.Contains(text, phrase) {
			return false
		}
	}
	for _, phrase := range []string{"quota", "billing", "insufficient", "unauthorized", "forbidden", "api key", "model not found", "timeout", "temporar", "overloaded", "rate limit", "connection reset"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

var _ ai.Client = (*FailoverClient)(nil)
