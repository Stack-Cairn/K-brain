package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	sysprompt "github.com/Stack-Cairn/K-brain/internal/prompts"
	"github.com/Stack-Cairn/K-brain/internal/tui"
	"github.com/Stack-Cairn/K-brain/internal/update"
)

var version = "dev"

func cwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func normalizeBareResume(args []string) {
	for i := range args {
		a := args[i]
		if a != "-r" && a != "--resume" {
			continue
		}

		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			continue
		}
		args[i] = "--browse"
	}
}

func main() {
	modelFlag := flag.String("m", "", "model name from ~/.k-brain/config.json (default: defaultModel)")
	providerFlag := flag.String("p", "", "provider to route the model through (default: model's first provider)")
	versionFlag := flag.Bool("version", false, "print version")
	resumeFlag := flag.String("resume", "", "resume a previous session by id (or unique prefix); bare opens the picker")
	flag.StringVar(resumeFlag, "r", "", "shorthand for --resume")
	var continueMode, browseMode bool
	flag.BoolVar(&continueMode, "c", false, "continue the most recent session in the current directory")
	flag.BoolVar(&continueMode, "continue", false, "same as -c")
	flag.BoolVar(&browseMode, "browse", false, "open the session picker at startup (same as bare --resume)")
	benchFlag := flag.Bool("bench", false, "do full startup init (config, routing, key, agent) then exit; for `task benchmark`")
	cautiousFlag := flag.Bool("cautious", false, "ask before running commands / writing files")
	normalizeBareResume(os.Args[1:])
	flag.Parse()

	if *versionFlag {
		fmt.Println("k-brain", version)
		return
	}

	if flag.NArg() > 0 && flag.Arg(0) == "mcp" {
		if err := mcpCLI(flag.Args()[1:], version); err != nil {
			fmt.Fprintln(os.Stderr, "kn:", err)
			os.Exit(1)
		}
		return
	}

	if flag.NArg() > 0 && flag.Arg(0) == "skills" {
		if err := skillsCLI(flag.Args()[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "kn:", err)
			os.Exit(1)
		}
		return
	}

	if flag.NArg() > 0 && flag.Arg(0) == "plugins" {
		if err := pluginsCLI(flag.Args()[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "kn plugins:", err)
			os.Exit(1)
		}
		return
	}

	if flag.NArg() > 0 && flag.Arg(0) == "acp" {
		if err := acpCLI(flag.Args()[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "kn acp:", err)
			os.Exit(1)
		}
		return
	}

	if flag.NArg() > 0 && flag.Arg(0) == "run" {
		if err := runCLI(flag.Args()[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "kn:", err)
			os.Exit(1)
		}
		return
	}

	if flag.NArg() > 0 && flag.Arg(0) == "sessions" {
		if err := sessionsCLI(flag.Args()[1:]...); err != nil {
			fmt.Fprintln(os.Stderr, "kn:", err)
			os.Exit(1)
		}
		return
	}

	if flag.NArg() > 0 && flag.Arg(0) == "browser" {
		if err := browserCLI(flag.Args()[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "kn:", err)
			os.Exit(1)
		}
		return
	}

	if flag.NArg() > 0 && flag.Arg(0) == "update" {
		if err := updateCLI(); err != nil {
			fmt.Fprintln(os.Stderr, "kn:", err)
			os.Exit(1)
		}
		return
	}

	if flag.NArg() > 0 && flag.Arg(0) == "auth" {
		fmt.Fprintln(os.Stderr, "kn: auth was removed; configure baseUrl and apiKey in ~/.k-brain/config.json")
		os.Exit(1)
	}

	firstRun := !config.Exists() && !config.SetupDone()

	initialPrompt := ""
	if flag.NArg() > 0 && flag.Arg(0) == "up" {
		initialPrompt = strings.Join(flag.Args()[1:], " ")
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kn:", err)
		os.Exit(1)
	}

	if *benchFlag {
		prov, mdl, id, err := cfg.Resolve(*modelFlag, *providerFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "kn:", err)
			os.Exit(1)
		}
		_ = prov.Key()
		_ = agent.New(ai.New(prov.BaseURL, "bench"), id, mdl.MaxOut, sysprompt.Build(cwd(), time.Now()))
		return
	}

	go update.Check(version)
	tui.Version = version
	sessionID, err := tui.Run(cfg, *modelFlag, *providerFlag, sysprompt.Build(cwd(), time.Now()), *resumeFlag, *cautiousFlag, firstRun, initialPrompt, continueMode, browseMode)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kn:", err)
		os.Exit(1)
	}
	if sessionID != "" {
		fmt.Printf("session %s — resume with: kn --resume %s\n", sessionID, sessionID)
	}
}
