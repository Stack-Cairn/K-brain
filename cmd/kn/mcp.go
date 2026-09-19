package main

import (
	"context"
	"errors"
	"fmt"
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
	switch args[0] {
	case "serve":
		return mcp.Serve(context.Background(), version)
	case "test":
		if len(args) < 2 {
			return errors.New("usage: kn mcp test <name>")
		}
		return mcpTestCLI(args[1])
	case "list", "add", "remove":
	default:
		return fmt.Errorf("unknown mcp subcommand %q (list|add|remove|serve|test)", args[0])
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	switch args[0] {
	case "list":
		servers := mcp.FromConfigMap(cfg.MCPServers)
		names := make([]string, 0, len(servers))
		for name := range servers {
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) == 0 {
			fmt.Println("no MCP servers configured")
		}
		for _, name := range names {
			c := servers[name]
			status := "enabled"
			if c.Disabled() {
				status = "disabled"
			}
			fmt.Printf("%-20s %-9s %-30s %s\n", name, status, mcpTarget(c), c.Source+" config")
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
	sc, ok := mcp.FromConfigMap(cfg.MCPServers)[name]
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
