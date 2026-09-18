package workflow

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/dop251/goja"
)

var effortValues = map[string]bool{
	"off": true, "minimal": true, "low": true,
	"medium": true, "high": true, "xhigh": true,
}

func validEffort(e string) bool { return effortValues[e] }

const effortValuesDesc = "off|minimal|low|medium|high|xhigh"

type MetaPhase struct {
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

type Meta struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	WhenToUse   string      `json:"whenToUse,omitempty"`
	Phases      []MetaPhase `json:"phases,omitempty"`
	Model       string      `json:"model,omitempty"`
	Effort      string      `json:"effort,omitempty"`
}

var determinismBlocklist = regexp.MustCompile(
	`\bDate\s*\.\s*now\b` +
		`|\bDate\s*\[\s*['"]now['"]\s*\]\s*\(\s*\)` +
		`|\bMath\s*\.\s*random\b` +
		`|\bMath\s*\[\s*['"]random['"]\s*\]\s*\(\s*\)` +
		`|\bnew\s+Date\s*\(\s*\)`)

var metaPrefix = regexp.MustCompile(`^export\s+const\s+meta\s*=`)

func Parse(script string) (Meta, string, error) {
	text := strings.TrimLeft(script, " \t\r\n")
	lead := len(script) - len(text)

	if determinismBlocklist.MatchString(text) {
		return Meta{}, "", errors.New("workflow scripts must be deterministic: Date.now() / Math.random() / argless new Date() are unavailable (they break resume); pass timestamps via args and vary randomness by index")
	}

	m := metaPrefix.FindString(text)
	if m == "" {
		return Meta{}, "", errors.New("`export const meta = { name, description, phases? }` must be the first statement in the script")
	}

	i := len(m)
	for i < len(text) && isSpace(text[i]) {
		i++
	}
	if i >= len(text) || text[i] != '{' {
		return Meta{}, "", errors.New("meta must be assigned a literal object: `export const meta = { ... }`")
	}

	objEnd := matchBrace(text, i)
	if objEnd == -1 {
		return Meta{}, "", errors.New("meta object literal is not closed (unbalanced braces)")
	}
	objText := text[i : objEnd+1]

	if strings.Contains(objText, "`") {
		return Meta{}, "", errors.New("meta must be a PURE LITERAL — no variables, function calls, spreads, or template interpolation (template literal in meta)")
	}

	vm := goja.New()
	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	if _, err := vm.RunString(determinismPrelude); err != nil {
		return Meta{}, "", fmt.Errorf("meta determinism prelude: %w", err)
	}
	v, err := vm.RunString("(" + objText + ")")
	if err != nil {
		return Meta{}, "", fmt.Errorf("meta must be a PURE LITERAL — no variables, function calls, spreads, or template interpolation (%w)", err)
	}

	var meta Meta
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("meta must be a PURE LITERAL object (getter threw: %v)", r)
			}
		}()
		err = vm.ExportTo(v, &meta)
	}()
	if err != nil {
		return Meta{}, "", fmt.Errorf("meta must be a PURE LITERAL object (%w)", err)
	}
	if err := validateMeta(&meta); err != nil {
		return Meta{}, "", err
	}

	after := objEnd + 1
	for after < len(text) && (text[after] == ';' || isSpace(text[after])) && text[after] != '\n' {
		after++
	}
	body := script[:lead] + text[after:]
	return meta, body, nil
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

func matchBrace(s string, open int) int {
	depth := 0
	var inStr byte
	for i := open; i < len(s); i++ {
		c := s[i]
		if inStr != 0 {
			switch c {
			case '\\':
				i++
			case inStr:
				inStr = 0
			}
			continue
		}
		switch {
		case c == '"' || c == '\'' || c == '`':
			inStr = c
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			nl := strings.IndexByte(s[i:], '\n')
			if nl == -1 {
				return -1
			}
			i += nl
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end == -1 {
				return -1
			}
			i += 2 + end + 1
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func validateMeta(m *Meta) error {
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("meta.name is required and must be a non-empty string")
	}
	if strings.TrimSpace(m.Description) == "" {
		return errors.New("meta.description is required and must be a non-empty string")
	}
	for _, p := range m.Phases {
		if p.Title == "" {
			return errors.New("each meta.phases entry must be an object with a string `title`")
		}
		if p.Effort != "" && !validEffort(p.Effort) {
			return fmt.Errorf("meta.phases[].effort must be one of %s when present (got %q)", effortValuesDesc, p.Effort)
		}
	}
	if m.Effort != "" && !validEffort(m.Effort) {
		return fmt.Errorf("meta.effort must be one of %s when present (got %q)", effortValuesDesc, m.Effort)
	}
	return nil
}
