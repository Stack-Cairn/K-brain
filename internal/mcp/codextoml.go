package mcp

import (
	"fmt"
	"maps"
	"os"
	"strconv"
	"strings"
)

func ParseCodex(data []byte) (map[string]ServerConfig, error) {
	tables, err := parseTOMLTables(string(data))
	if err != nil {
		return nil, err
	}

	hasKeys := map[string]bool{}
	for table, kv := range tables {
		if len(kv) > 0 {
			hasKeys[table] = true
		}
	}
	out := map[string]ServerConfig{}
	for table, kv := range tables {
		name, found := strings.CutPrefix(table, "mcp_servers.")
		if !found {
			continue
		}

		if idx := strings.LastIndex(name, "."); idx > 0 {
			sub := name[idx+1:]
			if (sub == "env" || sub == "environment" || sub == "headers" || sub == "http_headers" || sub == "env_http_headers") && hasKeys["mcp_servers."+name[:idx]] {
				continue
			}
		}

		if idx := strings.Index(name, "."); idx > 0 && hasKeys["mcp_servers."+name[:idx]] {
			continue
		}
		c := ServerConfig{}
		var args []string

		litHeaders := map[string]string{}
		envHeaders := map[string]string{}
		mergeInto := func(dst map[string]string, m map[string]any) map[string]string {
			if dst == nil {
				dst = map[string]string{}
			}
			for k, v := range m {
				if s, ok := v.(string); ok {
					dst[k] = s
				}
			}
			return dst
		}

		c.Env = mergeInto(c.Env, tables[table+".env"])
		c.Env = mergeInto(c.Env, tables[table+".environment"])

		litHeaders = mergeInto(litHeaders, tables[table+".headers"])
		litHeaders = mergeInto(litHeaders, tables[table+".http_headers"])

		if m := tables[table+".env_http_headers"]; len(m) > 0 {
			for k, v := range m {
				if s, ok := v.(string); ok && s != "" {
					envHeaders[k] = "$" + s
				}
			}
		}
		for k, v := range kv {
			switch k {
			case "command":
				switch v := v.(type) {
				case string:
					c.Command = []string{v}
				case []string:
					c.Command = v
				default:
					return nil, fmt.Errorf("codex config %s: command must be a string or array", table)
				}
			case "args":
				a, ok := v.([]string)
				if !ok {
					return nil, fmt.Errorf("codex config %s: args must be an array of strings", table)
				}
				args = a
			case "env", "environment", "headers", "http_headers", "env_http_headers":
				m, ok := v.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("codex config %s: %s must be an inline table", table, k)
				}
				switch k {
				case "headers", "http_headers":
					litHeaders = mergeInto(litHeaders, m)
				case "env_http_headers":

					for hk, hv := range m {
						if s, ok := hv.(string); ok && s != "" {
							envHeaders[hk] = "$" + s
						}
					}
				default:
					c.Env = mergeInto(c.Env, m)
				}
			case "bearer_token_env_var":

				s, ok := v.(string)
				if !ok {
					return nil, fmt.Errorf("codex config %s: bearer_token_env_var must be a string", table)
				}
				if s != "" {
					envHeaders["Authorization"] = "Bearer $" + s
				}
			case "url":
				s, ok := v.(string)
				if !ok {
					return nil, fmt.Errorf("codex config %s: url must be a string", table)
				}
				c.URL = s
			case "cwd":
				s, ok := v.(string)
				if !ok {
					return nil, fmt.Errorf("codex config %s: cwd must be a string", table)
				}
				c.Cwd = s
			case "enabled":
				b, ok := v.(bool)
				if !ok {
					return nil, fmt.Errorf("codex config %s: enabled must be a boolean", table)
				}
				c.Enabled = &b
			case "startup_timeout_sec", "startup_timeout_ms":
				n, ok := toInt(v)
				if !ok {
					return nil, fmt.Errorf("codex config %s: %s must be an integer", table, k)
				}
				if strings.HasSuffix(k, "_ms") {
					n = (n + 999) / 1000
				}
				c.StartupTimeout = n
			case "tool_timeout_sec":
				n, ok := toInt(v)
				if !ok {
					return nil, fmt.Errorf("codex config %s: tool_timeout_sec must be an integer", table)
				}
				c.ToolTimeout = n
			}

		}
		c.Command = append(c.Command, args...)

		c.Headers = envHeaders
		maps.Copy(c.Headers, litHeaders)

		out[name] = c
	}
	return out, nil
}

func LoadCodex(path string) (map[string]ServerConfig, error) {
	if path == "" {
		return nil, os.ErrNotExist
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseCodex(data)
}

func toInt(v any) (int, bool) {
	switch v := v.(type) {
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	}
	return 0, false
}

func parseTOMLTables(doc string) (map[string]map[string]any, error) {
	tables := map[string]map[string]any{"": {}}
	current := ""
	for lineno, raw := range strings.Split(doc, "\n") {
		line := strings.TrimSpace(stripTOMLComment(raw))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") || strings.HasPrefix(line, "[[") {
				return nil, fmt.Errorf("codex config: line %d: unsupported table header %q", lineno+1, raw)
			}
			current = strings.TrimSpace(line[1 : len(line)-1])
			current = unquoteTOMLTableName(current)
			if _, ok := tables[current]; !ok {
				tables[current] = map[string]any{}
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("codex config: line %d: expected key = value, got %q", lineno+1, raw)
		}
		key := strings.Trim(strings.TrimSpace(k), `"'`)
		val, err := parseTOMLValue(strings.TrimSpace(v))
		if err != nil {
			return nil, fmt.Errorf("codex config: line %d: %w", lineno+1, err)
		}
		tables[current][key] = val
	}
	return tables, nil
}

func stripTOMLComment(s string) string {
	var quote byte
	escaped := false
	for i := range len(s) {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case quote == '"' && c == '\\':
			escaped = true
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
		case quote == c:
			quote = 0
		case quote == 0 && c == '#':
			return s[:i]
		}
	}
	return s
}

func unquoteTOMLTableName(s string) string {
	var parts []string
	var cur strings.Builder
	var quote byte
	for i := range len(s) {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '"' || c == 0x27:
			quote = c
		case c == '.':
			parts = append(parts, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	parts = append(parts, strings.TrimSpace(cur.String()))
	return strings.Join(parts, ".")
}

func parseTOMLValue(s string) (any, error) {
	switch {
	case s == "true":
		return true, nil
	case s == "false":
		return false, nil
	case strings.HasPrefix(s, `"`) || strings.HasPrefix(s, "'"):
		return parseTOMLString(s)
	case strings.HasPrefix(s, "["):
		return parseTOMLArray(s)
	case strings.HasPrefix(s, "{"):
		return parseTOMLInlineTable(s)
	default:
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, nil
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, nil
		}
		return nil, fmt.Errorf("unsupported value %q", s)
	}
}

func parseTOMLString(s string) (string, error) {
	if len(s) < 2 {
		return "", fmt.Errorf("unterminated string %q", s)
	}
	if s[0] == '\'' {
		if !strings.HasSuffix(s, "'") {
			return "", fmt.Errorf("unterminated string %q", s)
		}
		return s[1 : len(s)-1], nil
	}
	var b strings.Builder
	i := 1
	for i < len(s) {
		switch s[i] {
		case '"':
			if i != len(s)-1 {
				return "", fmt.Errorf("trailing data after string %q", s)
			}
			return b.String(), nil
		case '\\':
			if i+1 >= len(s) {
				return "", fmt.Errorf("unterminated escape in %q", s)
			}
			switch s[i+1] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			default:
				return "", fmt.Errorf("unsupported escape \\%c in %q", s[i+1], s)
			}
			i += 2
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return "", fmt.Errorf("unterminated string %q", s)
}

func parseTOMLArray(s string) ([]string, error) {
	if !strings.HasSuffix(s, "]") {
		return nil, fmt.Errorf("unterminated array %q", s)
	}
	body := strings.TrimSpace(s[1 : len(s)-1])
	if body == "" {
		return nil, nil
	}
	var out []string
	for _, part := range splitTOMLList(body) {
		v, err := parseTOMLValue(part)
		if err != nil {
			return nil, err
		}
		str, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("array elements must be strings in %q", s)
		}
		out = append(out, str)
	}
	return out, nil
}

func parseTOMLInlineTable(s string) (map[string]any, error) {
	if !strings.HasSuffix(s, "}") {
		return nil, fmt.Errorf("unterminated inline table %q", s)
	}
	body := strings.TrimSpace(s[1 : len(s)-1])
	out := map[string]any{}
	if body == "" {
		return out, nil
	}
	for _, part := range splitTOMLList(body) {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("expected key = value in inline table %q", s)
		}
		val, err := parseTOMLValue(strings.TrimSpace(v))
		if err != nil {
			return nil, err
		}
		out[strings.Trim(strings.TrimSpace(k), `"'`)] = val
	}
	return out, nil
}

func splitTOMLList(s string) []string {
	var parts []string
	depth := 0
	var quote byte
	escaped := false
	start := 0
	for i := range len(s) {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case quote == '"' && c == '\\':
			escaped = true
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		case c == ',' && depth == 0:
			parts = append(parts, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	if tail := strings.TrimSpace(s[start:]); tail != "" {
		parts = append(parts, tail)
	}
	return parts
}
