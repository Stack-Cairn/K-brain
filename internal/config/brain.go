package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/datapath"
)

type PromptFile struct {
	Scope string
	Path  string
	Text  string
	// User marks the user's own standing rules (for example ~/.claude/CLAUDE.md), as
	// opposed to rules that belong to the project tree.
	User bool
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
	return promptText(string(data))
}

// promptText keeps the non-empty lines of a prompt file; lines starting with # are comments.
func promptText(data string) string {
	var lines []string
	for line := range strings.Lines(data) {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// hasProjectMarker reports whether dir is a project root, which ends the ancestor walk.
func hasProjectMarker(dir string) bool {
	for _, marker := range []string{".git", ".liveagent"} {
		if info, err := os.Stat(filepath.Join(dir, marker)); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// projectWalkDirs returns the directories from the project root down to wd. The walk
// climbs until a project marker; without one it reaches the filesystem root.
func projectWalkDirs(wd string) []string {
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
		// Include the project root itself, then stop before walking into its parent.
		if hasProjectMarker(wd) {
			break
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			break
		}
		wd = parent
	}
	slices.Reverse(dirs)
	return dirs
}

// ProjectPromptFiles returns the project-tree instruction files, root first.
func ProjectPromptFiles(wd string) []PromptFile {
	var out []PromptFile
	for _, file := range InstructionFiles(wd) {
		if !file.User {
			out = append(out, file)
		}
	}
	return out
}

// InstructionFiles returns every instruction file that applies to wd, in prompt order:
// user-level compatibility files first, then each directory from the project root down
// to wd. Within a directory the order is AGENTS.md, CLAUDE.md, .claude/CLAUDE.md,
// CLAUDE.local.md, .claude/CLAUDE.local.md, .cursorrules, .cursor/rules and finally the
// native .liveagent/brain.md, so later (deeper, more local) files win on conflicts.
//
// A file reachable through several names (symlinks, hard links, case-insensitive
// filesystems) and a file whose text repeats an earlier one are included once.
func InstructionFiles(wd string) []PromptFile {
	compat := RulesCompatSettings()
	c := &promptCollector{seenText: map[string]bool{}, skipPaths: map[string]bool{}}
	if dir, err := Dir(); err == nil {
		// The user brain is injected on its own; never repeat it as a project file.
		c.skipPaths[filepath.Clean(filepath.Join(dir, "brain.md"))] = true
	}
	if compat.ClaudeEnabled() {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			c.add(filepath.Join(home, ".claude", "CLAUDE.md"), "user CLAUDE.md", true, true)
		}
	}
	for _, dir := range projectWalkDirs(wd) {
		datapath.Report(datapath.MigrateProjectDir(dir))
		c.add(filepath.Join(dir, "AGENTS.md"), "project AGENTS.md", false, false)
		if compat.ClaudeEnabled() {
			c.add(filepath.Join(dir, "CLAUDE.md"), "project CLAUDE.md", false, true)
			c.add(filepath.Join(dir, ".claude", "CLAUDE.md"), "project .claude/CLAUDE.md", false, true)
			c.add(filepath.Join(dir, "CLAUDE.local.md"), "project CLAUDE.local.md", false, true)
			c.add(filepath.Join(dir, ".claude", "CLAUDE.local.md"), "project .claude/CLAUDE.local.md", false, true)
		}
		if compat.CursorEnabled() {
			c.add(filepath.Join(dir, ".cursorrules"), "project .cursorrules", false, true)
			c.addCursorRules(filepath.Join(dir, ".cursor", "rules"))
		}
		c.add(filepath.Join(ProjectDir(dir), "brain.md"), "project", false, false)
	}
	return c.out
}

// RulesCompatSettings reads the rulesCompat switches straight from config.json. It never
// writes defaults, so prompt assembly stays free of side effects; any read or parse
// problem falls back to the defaults (everything on).
func RulesCompatSettings() RulesCompatConfig {
	p, err := path()
	if err != nil {
		return RulesCompatConfig{}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return RulesCompatConfig{}
	}
	stripped, err := stripJSONC(data)
	if err != nil {
		return RulesCompatConfig{}
	}
	var wire struct {
		RulesCompat RulesCompatConfig `json:"rulesCompat"`
	}
	if json.Unmarshal(stripped, &wire) != nil {
		return RulesCompatConfig{}
	}
	return wire.RulesCompat
}

const (
	// maxCompatPromptFileBytes caps one compatibility rule file; longer files are cut at
	// a line boundary with a pointer to the full file.
	maxCompatPromptFileBytes = 64 << 10
	// maxCompatPromptTotalBytes caps all inlined compatibility rules together. Files past
	// the budget are listed by path instead of inlined.
	maxCompatPromptTotalBytes = 256 << 10
	// maxCursorRuleFiles bounds how many files one .cursor/rules tree contributes.
	maxCursorRuleFiles = 64
)

type promptCollector struct {
	out         []PromptFile
	seen        []os.FileInfo
	seenText    map[string]bool
	skipPaths   map[string]bool
	compatBytes int
}

// claim reports whether path is a regular file not yet collected under another name.
func (c *promptCollector) claim(path string) bool {
	path = filepath.Clean(path)
	if c.skipPaths[path] {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	for _, prev := range c.seen {
		if os.SameFile(prev, info) {
			return false
		}
	}
	c.seen = append(c.seen, info)
	return true
}

// add collects one instruction file. Native K-brain files (AGENTS.md, brain.md) load as
// before; compatibility files are size-capped and count toward the shared budget.
func (c *promptCollector) add(path, scope string, user, compat bool) {
	if !c.claim(path) {
		return
	}
	if !compat {
		c.emit(PromptFile{Scope: scope, Path: path, Text: readPromptFile(path), User: user})
		return
	}
	data, truncated, err := readCapped(path, maxCompatPromptFileBytes)
	if err != nil {
		return
	}
	c.addCompatText(path, scope, user, promptText(data), truncated)
}

func (c *promptCollector) addCompatText(path, scope string, user bool, text string, truncated bool) {
	if text == "" || c.seenText[text] || c.importsOnly(path, text) {
		return
	}
	if c.compatBytes+len(text) > maxCompatPromptTotalBytes {
		c.out = append(c.out, PromptFile{Scope: scope, Path: path, User: user,
			Text: fmt.Sprintf("(Not inlined: compatibility rules exceed the %d KiB budget. Read %s when it is relevant.)", maxCompatPromptTotalBytes>>10, path)})
		return
	}
	c.compatBytes += len(text)
	c.seenText[text] = true
	if truncated {
		text += fmt.Sprintf("\n(Truncated at %d KiB. Read %s for the rest.)", maxCompatPromptFileBytes>>10, path)
	}
	c.out = append(c.out, PromptFile{Scope: scope, Path: path, Text: text, User: user})
}

func (c *promptCollector) emit(file PromptFile) {
	if file.Text == "" || c.seenText[file.Text] {
		return
	}
	c.seenText[file.Text] = true
	c.out = append(c.out, file)
}

// importsOnly reports whether text consists solely of Claude Code "@path" imports that
// point at files already collected — the common "CLAUDE.md contains @AGENTS.md" shim.
func (c *promptCollector) importsOnly(path, text string) bool {
	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		ref, ok := strings.CutPrefix(line, "@")
		if !ok || ref == "" || strings.ContainsAny(ref, " \t") {
			return false
		}
		if rest, ok := strings.CutPrefix(ref, "~/"); ok {
			home, err := os.UserHomeDir()
			if err != nil {
				return false
			}
			ref = filepath.Join(home, rest)
		} else if !filepath.IsAbs(ref) {
			ref = filepath.Join(filepath.Dir(path), ref)
		}
		info, err := os.Stat(ref)
		if err != nil {
			return false
		}
		if !slices.ContainsFunc(c.seen, func(prev os.FileInfo) bool { return os.SameFile(prev, info) }) {
			return false
		}
	}
	return true
}

// readCapped reads at most limit bytes, cutting an oversized file at its last complete line.
func readCapped(path string, limit int) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return "", false, err
	}
	if len(data) <= limit {
		return string(data), false, nil
	}
	data = data[:limit]
	if i := bytes.LastIndexByte(data, '\n'); i > 0 {
		data = data[:i]
	}
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	return string(data), true, nil
}

// addCursorRules inlines always-apply Cursor rules and lists the rest by path so the
// model can read a glob- or description-scoped rule when it becomes relevant.
//
// A rule is inlined when its frontmatter sets alwaysApply: true, or when it is a plain
// .md file with no frontmatter at all. Every other rule goes into a single index entry.
func (c *promptCollector) addCursorRules(dir string) {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return
	}
	var files []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".md", ".mdc":
			files = append(files, p)
		}
		if len(files) >= maxCursorRuleFiles {
			return filepath.SkipAll
		}
		return nil
	})
	var index []string
	for _, file := range files {
		if !c.claim(file) {
			continue
		}
		data, truncated, err := readCapped(file, maxCompatPromptFileBytes)
		if err != nil {
			continue
		}
		rule, body, hasFrontmatter := parseCursorRule(data)
		if rule.alwaysApply || (!hasFrontmatter && strings.EqualFold(filepath.Ext(file), ".md")) {
			c.addCompatText(file, "project Cursor rule", false, promptText(body), truncated)
			continue
		}
		line := "- " + file
		if rule.description != "" {
			line += " — " + rule.description
		}
		switch {
		case len(rule.globs) > 0:
			line += " (applies to files matching: " + strings.Join(rule.globs, ", ") + ")"
		case rule.description == "":
			line += " (manual: apply only when the user asks for it)"
		}
		index = append(index, line)
	}
	if len(index) > 0 {
		c.out = append(c.out, PromptFile{
			Scope: "project Cursor rules index",
			Path:  dir,
			Text: "These Cursor rules are not always on. Read a rule with the read tool when its description fits the task " +
				"or before editing files that match its globs:\n" + strings.Join(index, "\n"),
		})
	}
}

type cursorRule struct {
	description string
	globs       []string
	alwaysApply bool
}

// parseCursorRule splits a Cursor rule into its frontmatter fields and body. The
// frontmatter reader understands the three keys Cursor writes (description, globs,
// alwaysApply), with globs as a comma list, a flow sequence or a block sequence.
func parseCursorRule(data string) (cursorRule, string, bool) {
	var rule cursorRule
	data = strings.TrimPrefix(data, string(rune(0xFEFF)))
	first, rest, ok := strings.Cut(data, "\n")
	if !ok || strings.TrimSpace(first) != "---" {
		return rule, data, false
	}
	var header []string
	body, closed := "", false
	for rest != "" {
		var line string
		line, rest, _ = strings.Cut(rest, "\n")
		if strings.TrimSpace(line) == "---" {
			body, closed = rest, true
			break
		}
		header = append(header, strings.TrimRight(line, "\r"))
	}
	if !closed {
		// Unterminated frontmatter: treat the whole file as body.
		return cursorRule{}, data, false
	}
	listKey := ""
	for _, line := range header {
		trimmed := strings.TrimSpace(line)
		if item, ok := strings.CutPrefix(trimmed, "- "); ok {
			if listKey == "globs" {
				if v := unquoteYAML(item); v != "" {
					rule.globs = append(rule.globs, v)
				}
			}
			continue
		}
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		key, value, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		listKey = key
		switch key {
		case "description":
			rule.description = unquoteYAML(value)
		case "alwaysApply":
			rule.alwaysApply = strings.EqualFold(unquoteYAML(value), "true")
		case "globs":
			value = strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
			for glob := range strings.SplitSeq(value, ",") {
				if glob = unquoteYAML(glob); glob != "" {
					rule.globs = append(rule.globs, glob)
				}
			}
		}
	}
	return rule, body, true
}

func unquoteYAML(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		v = v[1 : len(v)-1]
	}
	return strings.TrimSpace(v)
}
