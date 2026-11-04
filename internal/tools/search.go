package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// cliSearchCaps bounds the CLI's grep, glob and list so one call cannot flood the
// context or stall the turn on a huge tree.
var cliSearchCaps = searchCaps{
	timeout:     20 * time.Second,
	lineChars:   1000,
	contentRows: 200,
	fileRows:    500,
	fileBytes:   10 << 20,
	listChars:   10_000,
}

// WithoutCLIOnlyTools returns ts without the CLI's grep, glob, list and web_fetch. The
// LiveAgent catalog brings its own root-confined Grep, Glob and List, so the backend
// drops these; web_fetch is not in that catalog, whose policies the app lets users set.
func WithoutCLIOnlyTools(ts []Tool) []Tool {
	out := make([]Tool, 0, len(ts))
	for _, t := range ts {
		switch t.Def.Function.Name {
		case "grep", "glob", "list", "web_fetch":
			continue
		}
		out = append(out, t)
	}
	return out
}

func grepTool() Tool {
	return cliSearchTool("Grep", ai.NewTool("grep",
		"Search file contents with a regular expression (RE2 syntax), skipping .gitignore'd paths, .git and binary files. Read-only; prefer it over grep/rg in bash. output_mode: content (default; path:line: text), files_with_matches (one row per file with its match count), or count (totals only). Output is capped: 200 rows in content mode and 500 in files_with_matches by default (head_limit/offset page through more), lines are clipped at 1000 characters, files over 10 MB are skipped, and the search stops after 20 s with partial results.",
		`{"type":"object","properties":{"pattern":{"type":"string","description":"Regular expression to search for"},"path":{"type":"string","description":"File or directory to search (default: the working directory)"},"glob":{"type":"string","description":"Only search files whose path matches this glob, e.g. *.go or src/**/*.{ts,tsx}; a pattern without / matches at any depth. Separate alternatives with |"},"output_mode":{"anyOf":[{"type":"string","const":"content"},{"type":"string","const":"files_with_matches"},{"type":"string","const":"count"}],"description":"content (default), files_with_matches or count"},"ignore_case":{"type":"boolean","description":"Case-insensitive match (default false)"},"context":{"type":"number","minimum":0,"description":"Lines of context around each match in content mode (default 0)"},"multiline":{"type":"boolean","description":"Let the pattern span lines; . also matches newlines"},"head_limit":{"type":"number","minimum":1,"description":"Maximum rows to return (default 200 for content, 500 for files_with_matches)"},"offset":{"type":"number","minimum":0,"description":"Rows to skip, for paging (default 0)"}},"required":["pattern"],"additionalProperties":false}`))
}

func globTool() Tool {
	return cliSearchTool("Glob", ai.NewTool("glob",
		"Find files whose path, relative to the search directory, matches a glob pattern; results are sorted by path and skip .gitignore'd paths and .git. Read-only; prefer it over find/ls in bash. Supports *, ?, **, [abc] and {a,b}; * stays within one directory, so use **/*.go to search at any depth. Returns at most 200 paths by default (max_results/offset page through more).",
		`{"type":"object","properties":{"pattern":{"type":"string","description":"Glob relative to path, e.g. **/*.go or cmd/*/main.go; use / as the separator"},"path":{"type":"string","description":"Directory to search under (default: the working directory)"},"max_results":{"type":"number","minimum":1,"description":"Maximum paths to return (default 200)"},"offset":{"type":"number","minimum":0,"description":"Paths to skip, for paging (default 0)"}},"required":["pattern"],"additionalProperties":false}`))
}

func listTool() Tool {
	return cliSearchTool("List", ai.NewTool("list",
		"List a directory tree as [DIR]/[FILE] rows, skipping .gitignore'd paths and .git. Read-only; prefer it over ls/find in bash. Output stays within about 10,000 characters; entries that do not fit are summarized by extension, and offset continues from where the listing stopped.",
		`{"type":"object","properties":{"path":{"type":"string","description":"Directory to list (default: the working directory)"},"depth":{"type":"number","minimum":1,"description":"Levels to descend (default 2; 1 lists only direct children)"},"max_results":{"type":"number","minimum":1,"description":"Maximum entries to return (default 200)"},"offset":{"type":"number","minimum":0,"description":"Entries to skip, for paging (default 0)"}},"additionalProperties":false}`))
}

// cliSearchTool runs the LiveAgent search core for a CLI tool. Like read, it reads
// any path the user's account can and does not ask the permission gate; under AI
// review the reviewer gates it as the read-only tool it is.
func cliSearchTool(kind string, def ai.Tool) Tool {
	return Tool{Def: def, Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		a, err := liveagentArguments(def, raw)
		if err != nil {
			return "", err
		}
		target := laString(a, "path")
		if target == "" {
			target = WorkingDir(ctx)
		}
		if target == "" {
			if target, err = os.Getwd(); err != nil {
				return "", err
			}
		}
		root, err := resolveFilePath(ctx, target)
		if err != nil {
			return "", err
		}
		if kind == "Grep" {
			a["file_pattern"] = cliFilePattern(laString(a, "glob"))
			delete(a, "glob")
			if laString(a, "output_mode") == "files_with_matches" {
				a["output_mode"] = "files"
			}
			if _, set := a["ignore_case"]; !set {
				a["ignore_case"] = false
			}
		}
		return searchTree(ctx, kind, root, a, searchOptions{caps: cliSearchCaps})
	}}
}

// cliFilePattern turns grep's glob filter into the core's file_pattern: a pattern
// without a slash matches by name at any depth, the way rg --glob does.
func cliFilePattern(glob string) string {
	var parts []string
	for _, part := range strings.Split(glob, "|") {
		part = strings.TrimSpace(strings.ReplaceAll(part, `\`, "/"))
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			part = "**/" + part
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "|")
}
