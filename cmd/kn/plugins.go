package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/plugins"
)

func pluginsCLI(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: kn plugins <list|install|enable|disable|remove|reload> [path|name]")
	}
	project, _ := os.Getwd()
	m, err := plugins.New(project)
	if err != nil {
		if m == nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "plugins: %v\n", err)
	}
	switch args[0] {
	case "list":
		for _, p := range m.List() {
			state := "disabled"
			if p.Enabled {
				state = "enabled"
			}
			fmt.Printf("%-24s %-9s %s\n", p.Name, state, p.Description)
		}
		if len(m.List()) == 0 {
			fmt.Println("no plugins found")
		}
		return nil
	case "reload":
		return m.Reload()
	case "install":
		if len(args) != 2 {
			return errors.New("usage: kn plugins install <directory>")
		}
		return m.Install(args[1])
	case "enable", "disable":
		if len(args) != 2 || strings.TrimSpace(args[1]) == "" {
			return errors.New("usage: kn plugins enable|disable <name>")
		}
		return m.SetEnabled(args[1], args[0] == "enable")
	case "remove":
		if len(args) != 2 || strings.TrimSpace(args[1]) == "" {
			return errors.New("usage: kn plugins remove <name>")
		}
		return m.Remove(args[1])
	default:
		return fmt.Errorf("unknown plugins subcommand %q", args[0])
	}
}
