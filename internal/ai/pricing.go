package ai

import (
	"math"
	"strconv"
	"strings"
)

type Pricing struct {
	Prompt          string `json:"prompt"`
	Completion      string `json:"completion"`
	InputCacheRead  string `json:"input_cache_read,omitempty"`
	InputCacheWrite string `json:"input_cache_write,omitempty"`
}

type TokenRates struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

type Cost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}

func (p Pricing) Rates() *TokenRates {
	parse := func(raw string) (float64, bool) {
		rate, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		return rate, err == nil && validPrice(rate)
	}
	in, ok := parse(p.Prompt)
	if !ok {
		return nil
	}
	out, ok := parse(p.Completion)
	if !ok {
		return nil
	}
	rates := TokenRates{Input: in, Output: out, CacheRead: in, CacheWrite: in}
	for _, field := range []struct {
		raw string
		dst *float64
	}{{p.InputCacheRead, &rates.CacheRead}, {p.InputCacheWrite, &rates.CacheWrite}} {
		if field.raw == "" {
			continue
		}
		rate, ok := parse(field.raw)
		if !ok {
			return nil
		}
		*field.dst = rate
	}
	return &rates
}

func validPrice(v float64) bool {
	return v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}

func (r TokenRates) Valid() bool {
	return validPrice(r.Input) && validPrice(r.Output) && validPrice(r.CacheRead) && validPrice(r.CacheWrite)
}

func CalculateCost(u Usage, rates TokenRates) (Cost, bool) {
	if !rates.Valid() || u.PromptTokens < 0 || u.CompletionTokens < 0 || u.PromptCacheHitTokens < 0 || u.PromptCacheWriteTokens < 0 {
		return Cost{}, false
	}
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens < 0 {
		return Cost{}, false
	}
	read, write := u.Cached(), u.CacheWrite()
	if read > u.PromptTokens || write > u.PromptTokens-read {
		return Cost{}, false
	}
	cost := Cost{
		Input:      float64(u.PromptTokens-read-write) * rates.Input,
		Output:     float64(u.CompletionTokens) * rates.Output,
		CacheRead:  float64(read) * rates.CacheRead,
		CacheWrite: float64(write) * rates.CacheWrite,
	}
	cost.Total = cost.Input + cost.Output + cost.CacheRead + cost.CacheWrite
	if !validPrice(cost.Total) {
		return Cost{}, false
	}
	return cost, true
}
