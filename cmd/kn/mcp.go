package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/mcp"
)

func mcpCLI(args []string, version string) error {
	if len(args) == 0 {
		return errors.New("usage: kn mcp <list|add|remove|serve|test>")
	}
	if args[0] == "serve" {
		return mcp.Serve(context.Background(), version)
	}
	if args[0] == "test" {
		if len(args) < 2 {
			return errors.New("usage: kn mcp test <name>")
		}
		return mcpTestCLI(args[1])
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	switch args[0] {
	case "list":
		disc := mcp.LoadConfigured(mcp.FromConfigMap(cfg.MCPServers))
		names := make([]string, 0, len(disc.Merged)+len(disc.Blocked))
		for name := range disc.Merged {
			names = append(names, name)
		}
		for name := range disc.Blocked {
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) == 0 {
			fmt.Println("no MCP servers configured")
		}
		for _, name := range names {
			status := "enabled"
			var c mcp.ServerConfig
			if b, ok := disc.Blocked[name]; ok {
				c, status = b, "blocked"
			} else {
				c = disc.Merged[name]
				if c.Disabled() {
					status = "disabled"
				}
			}
			fmt.Printf("%-20s %-9s %-30s %s\n", name, status, mcpTarget(c), disc.Sources[name]+" config")
		}
		for src, e := range disc.Errs {
			fmt.Fprintf(os.Stderr, "mcp: %s: %s\n", src, e)
		}
		return nil

	case "add":
		if len(args) < 2 {
			return errors.New("usage: kn mcp add <name> -- <cmd...> | kn mcp add <name> --url <url>")
		}
		name := args[1]
		entry := config.MCPServer{}
		rest := args[2:]
		switch {
		case len(rest) >= 2 && rest[0] == "--url":
			entry.URL = rest[1]
		case len(rest) >= 2 && rest[0] == "--":
			entry.Command = rest[1:]
		default:
			return errors.New("usage: kn mcp add <name> -- <cmd...> | kn mcp add <name> --url <url>")
		}
		sc := mcp.FromConfigMap(map[string]config.MCPServer{name: entry})[name]
		if msg := sc.Valid(); msg != "" {
			return fmt.Errorf("invalid server: %s", msg)
		}
		if cfg.MCPServers == nil {
			cfg.MCPServers = map[string]config.MCPServer{}
		}
		cfg.MCPServers[name] = entry
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("added mcp server %q — starts on next kn launch\n", name)
		return nil

	case "remove":
		if len(args) < 2 {
			return errors.New("usage: kn mcp remove <name>")
		}
		name := args[1]
		if _, ok := cfg.MCPServers[name]; !ok {

			return fmt.Errorf("no mcp server named %q", name)
		}
		delete(cfg.MCPServers, name)
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("removed mcp server %q\n", name)
		return nil
	}
	return fmt.Errorf("unknown mcp subcommand %q (list|add|remove|serve|test)", args[0])
}

func mcpTestCLI(name string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	disc := mcp.LoadConfigured(mcp.FromConfigMap(cfg.MCPServers))
	sc, ok := disc.Merged[name]
	if !ok {
		return fmt.Errorf("no mcp server named %q (try: kn mcp list)", name)
	}
	fmt.Printf("testing mcp server %q (%s)…\n", name, mcpTarget(sc))
	res := mcp.Probe(context.Background(), name, sc)
	switch res.Status {
	case mcp.StatusReady:
		fmt.Printf("✓ connected in %s — %d tools\n", res.Elapsed.Round(time.Millisecond), res.Tools)
		if len(res.ToolNames) > 0 {
			fmt.Println("  tools:", strings.Join(res.ToolNames, ", "))
		}
		return nil
	case mcp.StatusDisabled:
		fmt.Println("○ disabled — enable it in ~/.k-brain/config.json")
		return fmt.Errorf("server %q is disabled", name)
	default:
		fmt.Printf("✗ failed after %s: %s\n", res.Elapsed.Round(time.Millisecond), res.Err)
		if res.Note != "" {
			fmt.Println("  note:", res.Note)
		}
		if res.Source != "" {
			fmt.Println("  config:", res.Source)
		}
		return fmt.Errorf("server %q failed", name)
	}
}

func mcpTarget(c mcp.ServerConfig) string {
	if c.Remote() {
		return c.URL
	}
	return strings.Join(c.Command, " ")
}

func mcpImportCLI(args []string) error {
	dryRun := false
	for _, a := range args {
		if a == "--dry-run" {
			dryRun = true
		} else {
			return errors.New("usage: kn mcp import [--dry-run]")
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	wd, _ := os.Getwd()
	disc := mcp.LoadMergedFiltered(wd, mcp.FromConfigMap(cfg.MCPServers), mcp.ImportPolicyFrom(cfg.MCPImport))
	add := map[string]config.MCPServer{}
	for name, sc := range disc.Merged {
		if _, owned := cfg.MCPServers[name]; owned {
			continue
		}
		add[name] = config.MCPServer{
			Command: sc.Command, Env: sc.Env, Cwd: sc.Cwd,
			URL: sc.URL, Headers: sc.Headers, Enabled: sc.Enabled,
			Note: sc.Note, StartupTimeout: sc.StartupTimeout, ToolTimeout: sc.ToolTimeout,
		}
	}
	if len(add) == 0 {
		fmt.Println("nothing to import — all servers are already in k-brain's config (or blocked by mcpImport)")
		return nil
	}
	if dryRun {
		body, err := json.MarshalIndent(add, "", "  ")
		if err != nil {
			return err
		}
		fmt.Printf("would add %d server(s) to ~/.k-brain/config.json under \"mcp\":\n%s\n", len(add), body)
		return nil
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = map[string]config.MCPServer{}
	}
	names := make([]string, 0, len(add))
	for name, entry := range add {
		cfg.MCPServers[name] = entry
		names = append(names, name)
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	sort.Strings(names)
	fmt.Printf("imported %d mcp server(s) into ~/.k-brain/config.json: %s\n", len(names), strings.Join(names, ", "))
	return nil
}
