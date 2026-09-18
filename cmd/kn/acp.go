package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	sysprompt "github.com/Stack-Cairn/K-brain/internal/prompts"
	"github.com/Stack-Cairn/K-brain/internal/routing"

	"github.com/Stack-Cairn/K-brain/internal/acp"
	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/lsp"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
	"github.com/Stack-Cairn/K-brain/internal/memory"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/skills"
	"github.com/Stack-Cairn/K-brain/internal/tools"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
	acpsdk "github.com/coder/acp-go-sdk"
)

func acpCLI(args []string) error {
	fs := flag.NewFlagSet("acp", flag.ContinueOnError)
	modelFlag := fs.String("m", "", "model name from ~/.k-brain/config.json (default: defaultModel)")
	providerFlag := fs.String("p", "", "provider to route the model through (default: model's first provider)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kn acp [-m model] [-p provider]")
		fmt.Fprintln(os.Stderr, "serve k-brain as an ACP agent over stdio (for editors like Zed)")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	route, err := routing.ResolveRoute(cfg, *modelFlag, *providerFlag, false)
	if err != nil {
		return err
	}

	vision := acpSupportsVision(cfg, route.ModelName, route.APIModel, route.ProviderName)
	acp.SetEventLog(func(format string, args ...any) { config.LogEvent("acp", fmt.Sprintf(format, args...)) })

	var store *session.Store
	if dir, derr := config.Dir(); derr == nil {
		if st, serr := session.Open(dir + "/sessions.db"); serr == nil {
			store = st
			defer func() { _ = st.Close() }()
		}
	}

	lspMgr := lsp.NewManager(lsp.FromConfigMap(cfg.LSPServers))
	tools.LSP = lspMgr
	defer func() {
		lspMgr.Close()
		tools.LSP = nil
	}()

	factory := func(ctx context.Context, wd string, servers map[string]mcp.ServerConfig) (*agent.Agent, *mcp.Manager, error) {
		ag := agent.New(route.Client.Clone(), route.APIModel, route.MaxOutput, sysprompt.Build(wd, time.Now()), agent.WithExperimental(cfg.Experimental))
		ag.ModelName, ag.Provider = route.ModelName, route.ProviderName
		ag.ComputerDisabled = true
		ag.ContextLimit = route.ContextLimit

		ag.Effort = routing.DefaultEffortFor(config.LoadCatalogs(), route.ProviderName, ag.Model, cfg.DefaultEffort)

		ag.Messages[0].Content += skills.PromptBlock(skills.Scan(skills.DefaultDirs()...))
		ag.Messages[0].Content += memory.PromptBlock(memory.Installation(), memory.Session("acp"))

		mgr := mcp.NewManager(servers)
		mgr.SetOnChange(func() { ag.SetMCPTools(mgr.Tools()) })
		mgr.Start(ctx)
		ag.SetMCPTools(mgr.Tools())
		if ib := mgr.InstructionsBlock(); ib != "" {
			ag.Messages[0].Content += ib
		}
		return ag, mgr, nil
	}

	bridge := acp.NewBridge(version, factory, store, vision, acpBaseMCP(cfg))
	conn := acpsdk.NewAgentSideConnection(bridge, os.Stdout, os.Stdin)
	bridge.SetAgentConnection(conn)

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case <-conn.Done():
	case <-sigCtx.Done():
	}

	bridge.CloseAll()
	bashrun.KillAll()
	return nil
}

func acpSupportsVision(cfg *config.Config, modelName, modelID, provName string) bool {
	if cat, ok := config.LoadCatalogs()[provName]; ok {
		if vision, found := cat.SupportsVision(modelID); found {
			return vision
		}
	}
	if mc, ok := cfg.Models[modelName]; ok {
		return mc.Vision
	}
	return false
}

func acpBaseMCP(cfg *config.Config) map[string]mcp.ServerConfig {
	disc := mcp.LoadConfigured(mcp.FromConfigMap(cfg.MCPServers))
	for src, err := range disc.Errs {
		config.LogEvent("acp", fmt.Sprintf("mcp discovery: %s: %s", src, err))
	}
	return disc.Merged
}
