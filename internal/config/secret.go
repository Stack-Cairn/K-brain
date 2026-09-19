package config

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

const SecretCmdTimeout = 5 * time.Second

func ResolveSecret(v string) (string, error) {
	return ResolveSecretContext(context.Background(), v)
}

func ResolveSecretContext(ctx context.Context, v string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	switch {
	case strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") && isEnvRefBody(v[2:len(v)-1]):
		name := v[2 : len(v)-1]
		if key, def, found := strings.Cut(name, ":-"); found {
			if !isEnvName(key) {
				return "", fmt.Errorf("secret reference ${%s}: invalid variable name %q", name, key)
			}
			if val := os.Getenv(key); val != "" {
				return val, nil
			}
			return ExpandTemplate(def)
		}
		if key, def, found := strings.Cut(name, "-"); found {
			if !isEnvName(key) {
				return "", fmt.Errorf("secret reference ${%s}: invalid variable name %q", name, key)
			}
			if _, ok := os.LookupEnv(key); ok {
				return os.Getenv(key), nil
			}
			return ExpandTemplate(def)
		}
		if val := os.Getenv(name); val != "" {
			return val, nil
		}
		return "", fmt.Errorf("secret reference ${%s}: environment variable unset or empty", name)
	case strings.HasPrefix(v, "$") && len(v) > 1 && !strings.ContainsAny(v[1:], " \t") && isEnvName(v[1:]):
		name := v[1:]
		if val := os.Getenv(name); val != "" {
			return val, nil
		}
		return "", fmt.Errorf("secret reference $%s: environment variable unset or empty", name)
	case strings.HasPrefix(v, "!"):
		return resolveSecretCommand(ctx, v[1:])
	}
	return v, nil
}

func IsWholeRef(v string) bool {
	switch {
	case strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}"):
		return len(v) > 3 && isEnvRefBody(v[2:len(v)-1])
	case strings.HasPrefix(v, "$") && len(v) > 1:
		return !strings.ContainsAny(v[1:], " \t") && isEnvName(v[1:])
	}
	return false
}

func isEnvRefBody(s string) bool {
	if key, _, found := strings.Cut(s, ":-"); found {
		return isEnvName(key)
	}
	if key, _, found := strings.Cut(s, "-"); found {
		return isEnvName(key)
	}
	return isEnvName(s)
}

func ExpandTemplate(v string) (string, error) {
	idx := strings.IndexByte(v, '$')
	if idx < 0 {
		return v, nil
	}
	var b strings.Builder
	b.Grow(len(v))
	b.WriteString(v[:idx])
	for i := idx; i < len(v); {
		c := v[i]
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 >= len(v) {
			b.WriteByte('$')
			break
		}
		switch n := v[i+1]; {
		case n == '$':
			b.WriteByte('$')
			i += 2
		case n == '{':
			end := strings.IndexByte(v[i+2:], '}')
			if end < 0 {
				return "", fmt.Errorf("template %q: unterminated ${", v)
			}
			val, err := templateVar(v[i+2:i+2+end], v)
			if err != nil {
				return "", err
			}
			b.WriteString(val)
			i += 2 + end + 1
		case n == '_' || n >= 'a' && n <= 'z' || n >= 'A' && n <= 'Z':
			j := i + 1
			for j < len(v) && (v[j] == '_' || v[j] >= 'a' && v[j] <= 'z' || v[j] >= 'A' && v[j] <= 'Z' || v[j] >= '0' && v[j] <= '9') {
				j++
			}
			val, err := templateVar(v[i+1:j], v)
			if err != nil {
				return "", err
			}
			b.WriteString(val)
			i = j
		default:
			b.WriteByte('$')
			i++
		}
	}
	return b.String(), nil
}

func templateVar(name, orig string) (string, error) {
	if key, def, found := strings.Cut(name, ":-"); found {
		if !isEnvName(key) {
			return "", fmt.Errorf("template %q: invalid variable name %q", orig, key)
		}
		if val := os.Getenv(key); val != "" {
			return val, nil
		}
		return ExpandTemplate(def)
	}
	if key, def, found := strings.Cut(name, "-"); found {
		if !isEnvName(key) {
			return "", fmt.Errorf("template %q: invalid variable name %q", orig, key)
		}
		if _, ok := os.LookupEnv(key); ok {
			return os.Getenv(key), nil
		}
		return ExpandTemplate(def)
	}
	if !isEnvName(name) {
		return "", fmt.Errorf("template %q: invalid variable name %q", orig, name)
	}
	if val := os.Getenv(name); val != "" {
		return val, nil
	}
	return "", fmt.Errorf("template %q: environment variable $%s unset or empty (no default)", orig, name)
}

func isEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

func ResolveEnvMap(env map[string]string) (map[string]string, error) {
	return ResolveEnvMapContext(context.Background(), env)
}

func ResolveEnvMapContext(ctx context.Context, env map[string]string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(env) == 0 {
		return env, nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var rv string
		var err error
		if strings.HasPrefix(v, "!") || IsWholeRef(v) {
			rv, err = ResolveSecretContext(ctx, v)
		} else {
			rv, err = ExpandTemplate(v)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			logf("mcp.env", "dropping env %s: %v", k, err)
			continue
		}
		out[k] = rv
	}
	return out, nil
}

func ResolveHeader(v string) (string, error) {
	return ResolveHeaderContext(context.Background(), v)
}

func ResolveHeaderContext(ctx context.Context, v string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.HasPrefix(v, "!") || IsWholeRef(v) {
		return ResolveSecretContext(ctx, v)
	}
	return ExpandTemplate(v)
}
