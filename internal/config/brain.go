package config

import (
	"os"
	"path/filepath"
	"strings"
)

type PromptFile struct {
	Scope string
	Path  string
	Text  string
}

func BrainPath() string {
	dir, err := Dir()
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, "brain.md")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			return ""
		}
	}
	return path
}

func BrainInstructions() string {
	path := BrainPath()
	if path == "" {
		return ""
	}
	return readPromptFile(path)
}

func readPromptFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var lines []string
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func projectPromptCandidates(wd string) []string {
	wd, err := filepath.Abs(wd)
	if err != nil {
		return nil
	}
	info, err := os.Stat(wd)
	if err == nil && !info.IsDir() {
		wd = filepath.Dir(wd)
	}
	var dirs []string
	for {
		dirs = append(dirs, wd)
		parent := filepath.Dir(wd)
		if parent == wd {
			break
		}
		wd = parent
	}
	var paths []string
	userBrain := ""
	if dir, err := Dir(); err == nil {
		userBrain = filepath.Clean(filepath.Join(dir, "brain.md"))
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		dir := dirs[i]
		for _, candidate := range []string{
			filepath.Join(dir, "AGENTS.md"),
			filepath.Join(dir, ".k-brain", "brain.md"),
		} {
			if filepath.Clean(candidate) == userBrain {
				continue
			}
			if _, err := os.Stat(candidate); err == nil {
				paths = append(paths, candidate)
			}
		}
	}
	return paths
}

func ProjectPromptFiles(wd string) []PromptFile {
	var out []PromptFile
	seen := map[string]bool{}
	for _, path := range projectPromptCandidates(wd) {
		path, err := filepath.Abs(path)
		if err != nil || seen[path] {
			continue
		}
		seen[path] = true
		text := readPromptFile(path)
		if text == "" {
			continue
		}
		scope := "project"
		if filepath.Base(path) == "AGENTS.md" {
			scope = "project AGENTS.md"
		}
		out = append(out, PromptFile{Scope: scope, Path: path, Text: text})
	}
	return out
}
