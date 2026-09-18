package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/config"
	sysprompt "github.com/Stack-Cairn/K-brain/internal/prompts"
	"github.com/Stack-Cairn/K-brain/internal/routing"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func resolveCacheKey(explicit, sessionID string) string {
	if explicit != "" {
		return explicit
	}
	if sessionID != "" {
		return sessionID
	}
	return "run-" + randHex(8)
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "run"
	}
	return hex.EncodeToString(b)
}

func runCLI(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	format := fs.String("format", "text", "output format: text (stream the reply) or json (newline-delimited event stream)")
	modelFlag := fs.String("m", "", "model name from ~/.k-brain/config.json (default: defaultModel)")
	providerFlag := fs.String("p", "", "provider to route the model through (default: model's first provider)")
	resumeFlag := fs.String("resume", "", "continue this session id (see `kn sessions`) instead of starting fresh")
	systemFlag := fs.String("system", "", "override the system prompt for this run")
	systemFileFlag := fs.String("system-file", "", "read the system prompt from this file (wins over -system)")
	maxTurnsFlag := fs.Int("max-turns", 0, "cap the tool-call loop at N rounds (0 = uncapped); on the cap, the model makes one final no-tools answer instead of erroring")
	timeoutFlag := fs.Duration("timeout", 0, "wall-clock cap on the whole run (e.g. 30s, 5m); 0 = no timeout")
	quietFlag := fs.Bool("quiet", false, "suppress the stderr tool/session notes (clean stdout for -format json piping)")
	noSessionFlag := fs.Bool("no-session", false, "run without persisting a session (one-off jobs don't clutter k-brain sessions)")
	cacheKeyFlag := fs.String("cache-key", "", "prompt_cache_key for provider prefix caching; defaults to the session id, else a per-run key. Pass a STABLE value (e.g. repo/reviewer) to reuse the cached system prefix across runs.")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kn run [--format text|json] [-m model] [-p provider] [-resume id] [-system text | -system-file path] [-max-turns N] [-timeout dur] [-quiet] [-no-session] \"prompt\"")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch *format {
	case "text", "json":
	default:
		return fmt.Errorf("unknown --format %q (want text|json)", *format)
	}

	prompt := strings.Join(fs.Args(), " ")

	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
		if data, err := io.ReadAll(os.Stdin); err == nil {
			if piped := strings.TrimSpace(string(data)); piped != "" {
				if prompt != "" {
					prompt += "\n\n"
				}
				prompt += piped
			}
		}
	}
	if prompt == "" {
		fs.Usage()
		return errors.New("no prompt given (pass one as an argument or pipe it on stdin)")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	route, err := routing.ResolveRoute(cfg, *modelFlag, *providerFlag, false)
	if err != nil {
		return err
	}

	sys := sysprompt.Build(cwd(), time.Now())
	if *systemFlag != "" {
		sys = *systemFlag
	}
	if *systemFileFlag != "" {
		data, err := os.ReadFile(*systemFileFlag)
		if err != nil {
			return fmt.Errorf("-system-file: %w", err)
		}
		sys = string(data)
	}

	ag := agent.New(route.Client, route.APIModel, route.MaxOutput, sys, agent.WithExperimental(cfg.Experimental))
	ag.ModelName, ag.Provider = route.ModelName, route.ProviderName

	ag.ComputerDisabled = true
	ag.ContextLimit = route.ContextLimit

	ag.Effort = routing.DefaultEffortFor(config.LoadCatalogs(), route.ProviderName, ag.Model, cfg.DefaultEffort)
	ag.MaxTurns = *maxTurnsFlag

	var store *session.Store
	var sessionID string
	if !*noSessionFlag {
		if dir, derr := config.Dir(); derr == nil {
			if st, serr := session.Open(dir + "/sessions.db"); serr == nil {
				store = st
				defer func() { _ = st.Close() }()
			}
		}
	}
	if store != nil {
		if *resumeFlag != "" {
			meta, msgs, lerr := store.Load(*resumeFlag)
			if lerr != nil {
				return fmt.Errorf("-resume: %w", lerr)
			}
			sessionID = meta.ID
			ag.Messages = append(ag.Messages[:1], msgs[1:]...)
		} else if cwd, cerr := os.Getwd(); cerr == nil {
			if id, ierr := store.Create(cwd, route.ModelName, route.ProviderName); ierr == nil {
				sessionID = id
			}
		}
	}

	ag.SetCacheKey(resolveCacheKey(*cacheKeyFlag, sessionID))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *timeoutFlag > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeoutFlag)
		defer cancel()
	}

	ev := agent.Events{}
	var emit func(any)
	note := func(format string, a ...any) {
		if !*quietFlag {
			fmt.Fprintf(os.Stderr, format+"\n", a...)
		}
	}
	if *format == "json" {
		enc := json.NewEncoder(os.Stdout)
		emit = func(v any) {
			if err := enc.Encode(v); err != nil {
				fmt.Fprintln(os.Stderr, "kn: json encode:", err)
			}
		}
		ev.OnText = func(d string) { emit(map[string]string{"type": "text", "delta": d}) }
		ev.OnThink = func(d string) {
			emit(map[string]string{"type": "reasoning", "delta": d})
		}
		ev.OnToolStart = func(_, name, args string) {
			emit(map[string]string{"type": "tool_start", "name": name, "args": args})
		}
		ev.OnToolEnd = func(_, name, result string) {
			emit(map[string]string{"type": "tool_end", "name": name, "result": result})
		}
	} else {
		ev.OnText = func(d string) { fmt.Fprint(os.Stdout, d) }
		ev.OnToolStart = func(_, name, args string) { note("⚒ %s", name) }
	}

	ag.ResolveModel = func(model, provider string) (agent.SubModel, error) {
		return routing.SubModelFor(cfg, model, provider)
	}
	if o, terr := routing.TaskDefaultFor(cfg, route.ProviderName); terr == nil {
		ag.TaskDefault = o
	} else {
		note("task model: %v — subagents use the run's model", terr)
	}

	final, err := ag.Turn(ctx, prompt, ev)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("run timed out after %s", *timeoutFlag)
	}
	if emit != nil {
		if err != nil {
			emit(map[string]string{"type": "error", "error": err.Error()})
		} else {
			emit(map[string]string{"type": "done", "text": final})
		}
	} else {
		fmt.Fprintln(os.Stdout)
	}

	if store != nil && sessionID != "" {
		if serr := store.Save(sessionID, 0, ag.MessagesSnapshot(), route.ModelName, route.ProviderName); serr != nil {
			config.LogEvent("session.save", "run FAILED id="+sessionID+": "+serr.Error())
		}
		note("session %s — resume with: kn run -resume %s \"…\" · or interactively: kn --resume %s", sessionID, sessionID, sessionID)
	}
	return err
}
