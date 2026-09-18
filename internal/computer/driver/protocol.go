package driver

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/computer"
)

type Params map[string]json.RawMessage

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  Params          `json:"params"`
}

type Fault struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (f *Fault) Error() string { return f.Message }

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *Fault          `json:"error,omitempty"`
}

type Snapshot struct {
	computer.AppState
	Revision string `json:"revision"`
}

type Backend interface {
	Call(context.Context, string, Params, any) error
}

type savedState struct {
	revision   string
	generation int
}

type Server struct {
	backend Backend
	token   string
	states  map[string]savedState
}

func New(backend Backend, token string) *Server {
	return &Server{backend: backend, token: token, states: make(map[string]savedState)}
}

func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	if s.token == "" {
		return errors.New("K_BRAIN_COMPUTER_TOKEN must be set")
	}
	if _, err := fmt.Fprintln(out, computer.ProtocolVersion); err != nil {
		return err
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		var req Request
		var result any
		var err error
		if e := json.Unmarshal(scanner.Bytes(), &req); e != nil {
			err = &Fault{-32700, "invalid JSON"}
		} else {
			callCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
			result, err = s.dispatch(callCtx, req)
			cancel()
		}
		resp := response{JSONRPC: "2.0", ID: req.ID, Result: result}
		if err != nil {
			resp.Result = nil
			if !errors.As(err, &resp.Error) {
				resp.Error = &Fault{6, err.Error()}
			}
		}
		if err := encoder.Encode(resp); err != nil {
			return err
		}
		if req.Method == "shutdown" && err == nil {
			return nil
		}
	}
	return scanner.Err()
}

func text(p Params, key string) string {
	var value string
	_ = json.Unmarshal(p[key], &value)
	return value
}

func put(p Params, key string, value any) { p[key], _ = json.Marshal(value) }

func (s *Server) dispatch(ctx context.Context, r Request) (any, error) {
	if r.JSONRPC != "2.0" || r.Method == "" {
		return nil, &Fault{-32600, "invalid JSON-RPC request"}
	}
	if s.token == "" || subtle.ConstantTimeCompare([]byte(text(r.Params, "token")), []byte(s.token)) != 1 {
		return nil, &Fault{8, "missing or invalid token"}
	}
	if r.Params == nil {
		r.Params = Params{}
	}
	switch r.Method {
	case "handshake":
		return map[string]any{"version": computer.ProtocolVersion, "pid": os.Getpid()}, nil
	case "shutdown":
		return map[string]bool{"ok": true}, nil
	case "apps":
		var apps []computer.RunningApp
		err := s.backend.Call(ctx, r.Method, r.Params, &apps)
		return apps, err
	case "permissions.status", "permissions.request":
		var status computer.TCCStatus
		err := s.backend.Call(ctx, r.Method, r.Params, &status)
		return status, err
	case "screenshot":
		if strings.TrimSpace(text(r.Params, "app")) == "" {
			return nil, &Fault{-32602, "app is required"}
		}
		var shot computer.Screenshot
		err := s.backend.Call(ctx, r.Method, r.Params, &shot)
		return shot, err
	case "state", "ax":
		return s.snapshot(ctx, r.Method, r.Params)
	case "click", "type", "press", "scroll", "set", "select", "menu":
		return s.mutate(ctx, r)
	default:
		return nil, &Fault{-32601, "unknown method " + r.Method}
	}
}

func (s *Server) snapshot(ctx context.Context, method string, p Params) (*computer.AppState, error) {
	app := text(p, "app")
	if strings.TrimSpace(app) == "" {
		return nil, &Fault{-32602, "app is required"}
	}
	var state Snapshot
	if err := s.backend.Call(ctx, method, p, &state); err != nil {
		return nil, err
	}
	if state.Revision == "" {
		return nil, errors.New("backend returned no state revision")
	}
	key := strings.ToLower(app)
	prev := s.states[key]
	if prev.generation == 0 || prev.revision != state.Revision {
		prev.generation++
	}
	prev.revision = state.Revision
	s.states[key] = prev
	state.Generation = prev.generation
	return &state.AppState, nil
}

func (s *Server) mutate(ctx context.Context, r Request) (any, error) {
	app := text(r.Params, "app")
	prev, ok := s.states[strings.ToLower(app)]
	var gen int
	if !ok || json.Unmarshal(r.Params["gen"], &gen) != nil || gen != prev.generation {
		return nil, &Fault{4, "state changed or missing — re-read state(app)"}
	}
	put(r.Params, "expectedRevision", prev.revision)
	var ack struct {
		OK bool `json:"ok"`
	}
	if err := s.backend.Call(ctx, r.Method, r.Params, &ack); err != nil {
		return nil, err
	}
	if !ack.OK {
		return nil, errors.New("backend did not confirm action")
	}
	s.states[strings.ToLower(app)] = savedState{generation: prev.generation + 1}
	state, err := s.snapshot(ctx, "state", r.Params)
	if err != nil {
		return map[string]string{"action": "completed", "stateUnavailable": err.Error(), "hint": "read state again; do not repeat the action"}, nil
	}
	saved := s.states[strings.ToLower(app)]
	saved.generation = prev.generation + 1
	s.states[strings.ToLower(app)] = saved
	state.Generation = saved.generation
	return state, nil
}
