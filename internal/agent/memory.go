package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func (a *Agent) SetSessionID(id string) {
	a.sessionID.Store(&id)
	a.SetCacheKey(id)
}

func (a *Agent) SetCacheKey(key string) {
	a.cacheKey = key
	if a.Client != nil {
		a.Client.SetCacheKey(key)
	}
}

func (a *Agent) SessionIDValue() string {
	if p := a.sessionID.Load(); p != nil {
		return *p
	}
	return ""
}

func memoryTools(a *Agent) []tools.Tool {
	scopes := func() (installation, session memory.Scope) {
		return memory.Installation(), memory.Session(a.SessionIDValue())
	}
	resolve := func(name string) (memory.Scope, error) {
		inst, sess := scopes()
		switch name {
		case "", "installation", "install", "global":
			return inst, nil
		case "session":
			if sess.Path == "" {
				return sess, errors.New("no session yet — use scope \"installation\"")
			}
			return sess, nil
		default:
			return memory.Scope{}, fmt.Errorf("scope must be \"installation\" or \"session\", got %q", name)
		}
	}
	scopeProp := `"scope":{"type":"string","enum":["installation","session"],"description":"installation (default) = every session, remembers facts about the user; session = notes for this conversation only"}`
	remember := tools.Tool{
		Def: ai.NewTool("remember",
			"Save a durable fact as one bullet in a markdown memory file that is injected back into your context every turn. Use it when the user shares a lasting preference or fact about themselves (scope=installation), or to leave a note for this conversation (scope=session). Phrase it as a standalone statement. The files are plain markdown the user can edit directly — keep entries short.",
			`{"type":"object","properties":{"text":{"type":"string","description":"The fact, phrased to stand on its own"},`+scopeProp+`},"required":["text"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Text  string `json:"text"`
				Scope string `json:"scope"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			s, err := resolve(in.Scope)
			if err != nil {
				return "", err
			}
			if err := s.Remember(in.Text); err != nil {
				return "", err
			}
			return fmt.Sprintf("Remembered (%s memory).", s.Name), nil
		},
	}
	forget := tools.Tool{
		Def: ai.NewTool("forget",
			"Mark a saved memory entry done by its number (entries are numbered in the memory block of your context). Use it when a fact is stale or the user asks you to stop remembering something.",
			`{"type":"object","properties":{"n":{"type":"number","description":"The entry number"},`+scopeProp+`},"required":["n"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				N     int    `json:"n"`
				Scope string `json:"scope"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			s, err := resolve(in.Scope)
			if err != nil {
				return "", err
			}
			if err := s.Forget(in.N); err != nil {
				return "", err
			}
			return fmt.Sprintf("Forgot entry %d (%s memory).", in.N, s.Name), nil
		},
	}
	return []tools.Tool{remember, forget}
}
