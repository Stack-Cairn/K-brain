package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/hooks"
	"github.com/Stack-Cairn/K-brain/internal/plugins"
	sysprompt "github.com/Stack-Cairn/K-brain/internal/prompts"
	"github.com/Stack-Cairn/K-brain/internal/routing"
	"github.com/Stack-Cairn/K-brain/internal/sandbox"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/session/recording"
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

type runTimeoutError struct {
	after time.Duration
	cause error
}

func (e *runTimeoutError) Error() string {
	return fmt.Sprintf("run timed out after %s: %s", e.after, e.cause)
}

func (e *runTimeoutError) Unwrap() error { return e.cause }

func runCLI(args []string) (runErr error) {
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
	outputCtx, cancelOutput := context.WithCancel(context.Background())
	defer cancelOutput()
	output := &runOutput{writer: os.Stdout, json: *format == "json", cancel: cancelOutput}
	var final string
	var stopReason ai.StopReason
	defer func() {
		runErr = errors.Join(runErr, output.finish(final, stopReason, runErr))
	}()

	if *maxTurnsFlag < 0 {
		return errors.New("--max-turns must be non-negative")
	}
	if *timeoutFlag < 0 {
		return errors.New("--timeout must be non-negative")
	}
	if *noSessionFlag && *resumeFlag != "" {
		return errors.New("--resume cannot be combined with --no-session")
	}
	ctx, stop := signal.NotifyContext(outputCtx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *timeoutFlag > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeoutFlag)
		defer cancel()
	}
	defer func() {
		var timeout *runTimeoutError
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && !errors.As(runErr, &timeout) {
			runErr = &runTimeoutError{*timeoutFlag, errors.Join(context.DeadlineExceeded, runErr)}
		}
	}()

	prompt := strings.Join(fs.Args(), " ")

	fi, err := os.Stdin.Stat()
	if err != nil {
		return fmt.Errorf("stdin: %w", err)
	}
	if fi.Mode()&os.ModeCharDevice == 0 {
		data, err := readRunInput(ctx, os.Stdin)
		if err != nil {
			return fmt.Errorf("stdin: %w", err)
		}
		if piped := strings.TrimSpace(string(data)); piped != "" {
			if prompt != "" {
				prompt += "\n\n"
			}
			prompt += piped
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
	route, err := routing.ResolveRouteContext(ctx, cfg, *modelFlag, *providerFlag, false)
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
	ag.WorkingDir = cwd()
	ag.WorktreeSubagents = cfg.WorktreeSubagents != nil && *cfg.WorktreeSubagents
	ag.Hooks = hooks.New(cfg.Hooks)
	ag.ModelName, ag.Provider = route.ModelName, route.ProviderName
	ag.Vision = route.Vision

	ag.ComputerDisabled = true
	ag.ContextLimit = route.ContextLimit

	ag.Effort = routing.DefaultEffortFor(config.LoadCatalogs(), route.ProviderName, ag.Model, cfg.DefaultEffort)
	ag.MaxTurns = *maxTurnsFlag
	if project, perr := os.Getwd(); perr == nil {
		if pm, _ := plugins.New(project); pm != nil {
			ag.PluginHook = pm.RunHook
			ag.SetPluginTools(pluginTools(pm))
			ag.Messages[0].Content += pm.PromptBlock()
		}
	}
	ag.SandboxPolicy = cfg.Sandbox.Policy(cwd())

	var store *session.Store
	var sessionID string
	var recorder *recording.Recorder
	if !*noSessionFlag {
		dir, err := config.Dir()
		if err != nil {
			return fmt.Errorf("session storage: %w", err)
		}
		store, err = session.OpenProjectHome(dir)
		if err != nil {
			return fmt.Errorf("session storage: %w", err)
		}
		defer func() { _ = store.Close() }()
		if *resumeFlag != "" {
			meta, _, err := store.Load(*resumeFlag)
			if err != nil {
				return fmt.Errorf("-resume: %w", err)
			}
			sessionID = meta.ID
		} else {
			sessionID, err = store.Create(ag.WorkingDir, route.ModelName, route.ProviderName)
			if err != nil {
				return fmt.Errorf("session create: %w", err)
			}
			if err := store.Save(sessionID, 0, ag.MessagesSnapshot(), route.ModelName, route.ProviderName); err != nil {
				return fmt.Errorf("session initialize: %w", err)
			}
		}
		recorder, err = recording.Open(store, sessionID, ag)
		if err != nil {
			return fmt.Errorf("session restore: %w", err)
		}
	}
	ctx = sandbox.WithPolicy(ctx, ag.SandboxPolicy)
	ag.SetSessionID(sessionID)
	if err := ag.StartSession(ctx); err != nil {
		return err
	}

	ag.SetCacheKey(resolveCacheKey(*cacheKeyFlag, sessionID))

	note := func(format string, a ...any) {
		if !*quietFlag {
			fmt.Fprintf(os.Stderr, format+"\n", a...)
		}
	}
	ev := agent.FanIn(recorder.Events(), output.events(note))

	ag.ResolveModel = func(model, provider string) (agent.SubModel, error) {
		return routing.SubModelForContext(ctx, cfg, model, provider)
	}
	if o, terr := routing.TaskDefaultForContext(ctx, cfg); terr == nil {
		ag.TaskDefault = o
	} else {
		note("task model: %v — subagents use the run's model", terr)
	}

	final, err = ag.Turn(ctx, prompt, ev)
	stopReason = ag.LastStopReason()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = &runTimeoutError{*timeoutFlag, errors.Join(context.DeadlineExceeded, err)}
	}
	if store != nil && sessionID != "" {
		if serr := recorder.Save(); serr != nil {
			config.LogEvent("session.save", "run FAILED id="+sessionID+": "+serr.Error())
			err = errors.Join(err, fmt.Errorf("session save: %w", serr))
		} else {
			note("session %s — resume with: kn run -resume %s \"…\" · or interactively: kn --resume %s", sessionID, sessionID, sessionID)
		}
	}

	return err
}
