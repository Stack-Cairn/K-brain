package templates

import (
	"fmt"
	"github.com/Stack-Cairn/K-brain/internal/commandline"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const maxSize = 1 << 20

type Template struct {
	Name         string
	Description  string
	ArgumentHint string
	Body         string
	Path         string
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
var parameter = regexp.MustCompile(`\$\{([1-9][0-9]*|@|ARGUMENTS):-([^}]*)\}|\$\{@:([1-9][0-9]*)(?::([0-9]+))?\}|\$([1-9][0-9]*|@|ARGUMENTS)\b|\$@`)

func Load(dirs ...string) ([]Template, []error) {
	byName := map[string]Template{}
	var problems []error
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", dir, err))
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), ".md")
			if !validName.MatchString(name) {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			f, err := os.Open(path)
			if err != nil {
				problems = append(problems, fmt.Errorf("%s: %w", path, err))
				continue
			}
			data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
			f.Close()
			if err == nil && len(data) > maxSize {
				err = fmt.Errorf("template exceeds 1 MiB")
			}
			if err != nil {
				problems = append(problems, fmt.Errorf("%s: %w", path, err))
				continue
			}
			tpl, err := Parse(name, string(data))
			if err != nil {
				problems = append(problems, fmt.Errorf("%s: %w", path, err))
				continue
			}
			tpl.Path = path
			byName[name] = tpl
		}
	}
	out := make([]Template, 0, len(byName))
	for _, tpl := range byName {
		out = append(out, tpl)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, problems
}

func Parse(name, text string) (Template, error) {
	tpl := Template{Name: name}
	text = strings.TrimPrefix(strings.ReplaceAll(text, "\r\n", "\n"), "\ufeff")
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[0] == "---" {
		end := -1
		for i := 1; i < len(lines); i++ {
			if lines[i] == "---" {
				end = i
				break
			}
		}
		if end < 0 {
			return tpl, fmt.Errorf("unclosed frontmatter")
		}
		for _, line := range lines[1:end] {
			key, val, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			val = strings.TrimSpace(val)
			if unquoted, err := strconv.Unquote(val); err == nil {
				val = unquoted
			} else if len(val) >= 2 && val[0] == '\'' && val[len(val)-1] == '\'' {
				val = strings.ReplaceAll(val[1:len(val)-1], "''", "'")
			}
			switch strings.TrimSpace(key) {
			case "description":
				tpl.Description = val
			case "argument-hint":
				tpl.ArgumentHint = val
			}
		}
		lines = lines[end+1:]
	}
	tpl.Body = strings.TrimSpace(strings.Join(lines, "\n"))
	if tpl.Body == "" {
		return tpl, fmt.Errorf("empty template")
	}
	if tpl.Description == "" {
		for _, line := range lines {
			if strings.TrimSpace(line) != "" {
				tpl.Description = strings.TrimSpace(line)
				break
			}
		}
	}
	return tpl, nil
}

func SplitArgs(text string) ([]string, error) {
	return commandline.Split(text)
}

func (t Template) Expand(text string) (string, error) {
	args, err := SplitArgs(text)
	if err != nil {
		return "", err
	}
	value := func(key string) string {
		if key == "@" || key == "ARGUMENTS" {
			return strings.Join(args, " ")
		}
		n, err := strconv.Atoi(key)
		if err != nil || n < 1 || n > len(args) {
			return ""
		}
		return args[n-1]
	}
	return parameter.ReplaceAllStringFunc(t.Body, func(token string) string {
		m := parameter.FindStringSubmatch(token)
		if m[1] != "" {
			if s := value(m[1]); s != "" {
				return s
			}
			return m[2]
		}
		if m[3] != "" {
			n, err := strconv.Atoi(m[3])
			if err != nil || n > len(args) {
				return ""
			}
			end := len(args)
			if m[4] != "" {
				count, err := strconv.Atoi(m[4])
				if err == nil && count < end-(n-1) {
					end = n - 1 + count
				}
			}
			return strings.Join(args[n-1:end], " ")
		}
		if token == "$@" {
			return value("@")
		}
		return value(m[5])
	}), nil
}
