package agent

import (
	"errors"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

var ErrBusy = errors.New("agent is busy; wait for the current turn to finish")

type ModelConfig struct {
	Client       ai.Client
	ID           string
	Name         string
	Provider     string
	MaxTokens    int
	ContextLimit int
	Vision       bool
	Temperature  *float64
	TopP         *float64
}

func (a *Agent) SetModel(model ModelConfig) error {
	if !a.turnMu.TryLock() {
		return ErrBusy
	}
	defer a.turnMu.Unlock()
	if model.Client == nil || strings.TrimSpace(model.ID) == "" {
		return errors.New("model client and id are required")
	}
	if !a.startMu.TryLock() {
		return ErrBusy
	}
	defer a.startMu.Unlock()
	a.clientMu.Lock()
	defer a.clientMu.Unlock()
	model.Client.SetCacheKey(a.cacheKey)
	a.usageMu.Lock()
	a.Client, a.Model, a.ModelName, a.Provider = model.Client, model.ID, model.Name, model.Provider
	a.MaxTokens, a.ContextLimit = model.MaxTokens, model.ContextLimit
	a.Vision = model.Vision
	a.Temperature, a.TopP = model.Temperature, model.TopP
	a.lastPrompt = 0
	a.usageMu.Unlock()
	return nil
}
