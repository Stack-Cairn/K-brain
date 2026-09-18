package skills

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Skill struct {
	Name        string
	Description string
	Path        string

	DisableModelInvocation bool

	Warning string
}

type ScanProblem struct {
	Path string
	Err  string
}

func DefaultDirs() []string {
	var dirs []string
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(wd, ".agents", "skills"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".k-brain", "skills"))
		dirs = append(dirs, filepath.Join(home, ".agents", "skills"))
	}
	return dirs
}

func ForeignDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".codex", "skills"),
		filepath.Join(home, ".claude", "skills"),
	}
}

func Scan(dirs ...string) []Skill {
	sk, _ := ScanDetailed(dirs...)
	return sk
}

func ScanDetailed(dirs ...string) ([]Skill, []ScanProblem) {
	var out []Skill
	var problems []ScanProblem
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(d, e.Name(), "SKILL.md")
			if _, err := os.Stat(p); err != nil {
				continue
			}
			s, err := parse(p)
			if err != nil {
				problems = append(problems, ScanProblem{Path: p, Err: err.Error()})
				continue
			}
			if s.Name == "" {
				s.Name = e.Name()
			}
			if w := validate(s); w != "" {
				s.Warning = w
			}
			out = append(out, s)
		}
	}
	return out, problems
}

func parse(path string) (Skill, error) {
	f, err := os.Open(path)
	if err != nil {
		return Skill{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return Skill{}, fmt.Errorf("%s: no frontmatter", path)
	}
	s := Skill{Path: path}
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			break
		}
		key, v, ok := cutKey(line)
		if !ok {
			continue
		}
		if isBlockScalarIndicator(v) {

			v = readBlockScalar(sc, v, indentOf(line))
		} else {
			v = unquote(v)
		}
		switch key {
		case "name":
			s.Name = v
		case "description":
			s.Description = v
		case "disable-model-invocation":
			s.DisableModelInvocation = strings.TrimSpace(v) == "true"
		}
	}
	return s, sc.Err()
}

func cutKey(line string) (key, value string, ok bool) {
	if line == "" || line[0] == ' ' || line[0] == '\t' {
		return "", "", false
	}
	k, v, found := strings.Cut(line, ":")
	if !found {
		return "", "", false
	}
	return k, v, true
}

func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

func isBlockScalarIndicator(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || (v[0] != '>' && v[0] != '|') {
		return false
	}
	for _, r := range v[1:] {
		if r != '-' && r != '+' && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func readBlockScalar(sc *bufio.Scanner, header string, keyIndent int) string {
	indicator := strings.TrimSpace(header)
	literal := indicator[0] == '|'
	chomp := byte(0)
	for _, r := range indicator[1:] {
		if r == '-' || r == '+' {
			chomp = byte(r)
		}
	}

	var lines []string

	bodyIndent := -1
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {

			break
		}
		ind := indentOf(line)
		if trimmed != "" {
			if ind <= keyIndent {
				break
			}
			if bodyIndent == -1 {
				bodyIndent = ind
			}
		}
		lines = append(lines, line)
	}

	_ = keyIndent

	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			lines[i] = ""
			continue
		}
		if bodyIndent > 0 && len(line) >= bodyIndent {
			lines[i] = line[bodyIndent:]
		}
	}

	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}

	trailing := 0
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
		trailing++
	}

	var v string
	if literal {
		v = strings.Join(lines, "\n")
	} else {
		v = strings.Join(lines, " ")
	}
	if chomp == '+' && len(lines) > 0 {
		v += "\n" + strings.Repeat("\n", trailing)
	}
	return v
}

func unquote(v string) string {
	v = strings.TrimSpace(v)
	for _, q := range []string{`"`, `'`} {
		if strings.HasPrefix(v, q) && strings.HasSuffix(v, q) && len(v) >= 2 {
			return v[1 : len(v)-1]
		}
	}
	return v
}

const (
	specMaxName = 64
	specMaxDesc = 1024
)

var specNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

func ValidName(name string) bool {
	return specNameRe.MatchString(name)
}

func validate(s Skill) string {
	var problems []string
	if len(s.Name) > specMaxName {
		problems = append(problems, fmt.Sprintf("name exceeds %d characters (%d)", specMaxName, len(s.Name)))
	}
	if !specNameRe.MatchString(s.Name) {
		problems = append(problems, "name must be lowercase a-z, 0-9, hyphens only")
	}
	if strings.HasPrefix(s.Name, "-") || strings.HasSuffix(s.Name, "-") {
		problems = append(problems, "name must not start or end with a hyphen")
	}
	if strings.Contains(s.Name, "--") {
		problems = append(problems, "name must not contain consecutive hyphens")
	}
	if len(s.Description) > specMaxDesc {
		problems = append(problems, fmt.Sprintf("description exceeds %d characters (%d)", specMaxDesc, len(s.Description)))
	}
	return strings.Join(problems, "; ")
}

func PromptBlock(sk []Skill) string {
	var visible []Skill
	for _, s := range sk {
		if !s.DisableModelInvocation {
			visible = append(visible, s)
		}
	}
	if len(visible) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n<available_skills>\nThese skills hold task-specific instructions. When one is relevant, read its SKILL.md with the read tool and follow it. Relative paths in a skill resolve against the skill's directory (the parent of its SKILL.md).\n")
	for _, s := range visible {
		b.WriteString("  <skill>\n")
		fmt.Fprintf(&b, "    <name>%s</name>\n", xmlEscape(s.Name))
		fmt.Fprintf(&b, "    <description>%s</description>\n", xmlEscape(s.Description))
		fmt.Fprintf(&b, "    <location>%s</location>\n", xmlEscape(s.Path))
		b.WriteString("  </skill>\n")
	}
	b.WriteString("</available_skills>")
	return b.String()
}

func xmlEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	).Replace(s)
}
