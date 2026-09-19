package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func sessionsCLI(args ...string) error {
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	st, err := session.OpenProjectHome(dir)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	fmt.Printf("Sessions: %s\n", st.SessionsDir())
	var metas []session.Meta
	if len(args) > 0 && args[0] == "search" {
		if len(args) < 2 {
			return fmt.Errorf("usage: kn sessions search <query>")
		}
		includeArchived := slices.Contains(args[1:], "--all")
		terms := make([]string, 0, len(args)-1)
		for _, arg := range args[1:] {
			if arg != "--all" {
				terms = append(terms, arg)
			}
		}
		if len(terms) == 0 {
			return fmt.Errorf("usage: kn sessions search <query>")
		}
		metas, err = st.Search(strings.Join(terms, " "), includeArchived)
	} else if len(args) > 0 && (args[0] == "delete" || args[0] == "archive" || args[0] == "unarchive") {
		if len(args) != 2 {
			return fmt.Errorf("usage: kn sessions %s <id>", args[0])
		}
		id := args[1]
		switch args[0] {
		case "delete":
			err = st.Delete(id)
		case "archive":
			err = st.SetArchived(id, true)
		case "unarchive":
			err = st.SetArchived(id, false)
		}
		if err != nil {
			return err
		}
		fmt.Printf("%s %s\n", args[0], id)
		return nil
	} else if len(args) > 0 {
		return fmt.Errorf("usage: kn sessions [search <query>|delete <id>|archive <id>|unarchive <id>]")
	} else {
		metas, err = st.Recent(50)
	}
	if err != nil {
		return err
	}
	if len(metas) == 0 {
		fmt.Println("no sessions yet")
		return nil
	}
	for _, mt := range metas {
		title := mt.Title
		if title == "" {
			title = "(untitled)"
		}
		project := filepath.Base(filepath.Clean(mt.CWD))
		if project == "." || project == string(filepath.Separator) || project == "" {
			project = "workspace"
		}
		fmt.Printf("%s  %-24s  %-16s  %-40s  %s  %s\n", mt.ID, trunc(project, 24), mt.Model, trunc(title, 40), ago(mt.UpdatedAt), strings.Join(mt.Tags, ","))
	}
	return nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("2006-01-02")
	}
}
