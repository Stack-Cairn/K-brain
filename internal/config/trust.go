package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type trustedFolder struct {
	Trusted   bool
	DecidedAt int64
}

func ProjectID(dir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(dir)))
	return fmt.Sprintf("%x", sum[:8])
}

func trustedHome() (string, error) {
	return Dir()
}

func trustedPath() (string, error) {
	home, err := trustedHome()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(home, "trusted_folders.toml"), nil
}

func TrustedPath() (string, error) { return trustedPath() }

// trustMu serializes read-modify-write updates of trusted_folders.toml within
// this process; the backend can serve several trust requests at once.
var trustMu sync.Mutex

func Trusted(dir string) bool {
	trusted, _ := TrustDecision(dir)
	return trusted
}

// TrustDecision reports whether dir is trusted and which recorded folder (dir
// itself or its nearest recorded ancestor) decided it. source is empty when no
// record covers dir.
func TrustDecision(dir string) (trusted bool, source string) {
	folders, err := loadTrustedFolders()
	if err != nil {
		return false, ""
	}
	return decideTrust(folders, dir)
}

func loadTrustedFolders() (map[string]trustedFolder, error) {
	p, err := trustedPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return parseTrustedFolders(string(data))
}

func decideTrust(folders map[string]trustedFolder, dir string) (bool, string) {
	query := filepath.Clean(dir)
	bestDepth := -1
	trusted := false
	source := ""
	for folder, record := range folders {
		rel, relErr := filepath.Rel(folder, query)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		depth := len(filepath.VolumeName(folder))
		for _, part := range strings.FieldsFunc(filepath.Clean(folder)[len(filepath.VolumeName(folder)):], func(r rune) bool {
			return r == '/' || r == '\\'
		}) {
			if part != "" {
				depth++
			}
		}
		if depth >= bestDepth {
			bestDepth = depth
			trusted = record.Trusted
			source = folder
		}
	}
	return trusted, source
}

func Trust(dir string) error {
	if err := updateTrustedFolders(func(folders map[string]trustedFolder) {
		folders[filepath.Clean(dir)] = trustedFolder{Trusted: true, DecidedAt: time.Now().Unix()}
	}); err != nil {
		return err
	}
	LogEvent("trust.grant", dir)
	return nil
}

// Untrust revokes trust for dir. Its own record is removed; when an ancestor
// record would still grant trust, an explicit trusted = false record is kept
// for dir instead so the revocation sticks. Records of descendants are left as
// they are.
func Untrust(dir string) error {
	key := filepath.Clean(dir)
	if err := updateTrustedFolders(func(folders map[string]trustedFolder) {
		delete(folders, key)
		if inherited, _ := decideTrust(folders, key); inherited {
			folders[key] = trustedFolder{Trusted: false, DecidedAt: time.Now().Unix()}
		}
	}); err != nil {
		return err
	}
	LogEvent("trust.revoke", dir)
	return nil
}

func updateTrustedFolders(change func(map[string]trustedFolder)) error {
	trustMu.Lock()
	defer trustMu.Unlock()
	p, err := trustedPath()
	if err != nil {
		return err
	}
	folders := map[string]trustedFolder{}
	if data, readErr := os.ReadFile(p); readErr == nil {
		if parsed, parseErr := parseTrustedFolders(string(data)); parseErr == nil {
			folders = parsed
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	change(folders)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(formatTrustedFolders(folders)), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func parseTrustedFolders(data string) (map[string]trustedFolder, error) {
	folders := map[string]trustedFolder{}
	section := ""
	for lineNo, raw := range strings.Split(data, "\n") {
		line := strings.TrimSpace(trimTOMLComment(raw))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[folders.") && strings.HasSuffix(line, "]") {
			key, err := parseTOMLString(strings.TrimSuffix(strings.TrimPrefix(line, "[folders."), "]"))
			if err != nil || key == "" {
				return nil, fmt.Errorf("invalid trusted_folders.toml section at line %d", lineNo+1)
			}
			section = filepath.Clean(key)
			if _, ok := folders[section]; !ok {
				folders[section] = trustedFolder{}
			}
			continue
		}
		if section == "" {
			return nil, fmt.Errorf("setting outside folders section at line %d", lineNo+1)
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("invalid setting at line %d", lineNo+1)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "trusted":
			trusted, err := strconv.ParseBool(value)
			if err != nil {
				return nil, fmt.Errorf("invalid trusted value at line %d", lineNo+1)
			}
			record := folders[section]
			record.Trusted = trusted
			folders[section] = record
		case "decided_at":
			at, err := strconv.ParseInt(value, 10, 64)
			if err != nil || at < 0 {
				return nil, fmt.Errorf("invalid decided_at value at line %d", lineNo+1)
			}
			record := folders[section]
			record.DecidedAt = at
			folders[section] = record
		default:
			return nil, fmt.Errorf("unknown setting %q at line %d", key, lineNo+1)
		}
	}
	return folders, nil
}

func trimTOMLComment(line string) string {
	quoted := byte(0)
	escaped := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if quoted == '"' {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == quoted {
				quoted = 0
			}
			continue
		}
		if quoted == '\'' {
			if c == quoted {
				quoted = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quoted = c
			continue
		}
		if c == '#' {
			return line[:i]
		}
	}
	return line
}

func parseTOMLString(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		return value[1 : len(value)-1], nil
	}
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", errors.New("expected quoted string")
	}
	decoded, err := strconv.Unquote(value)
	if err != nil {
		return "", err
	}
	return decoded, nil
}

func formatTrustedFolders(folders map[string]trustedFolder) string {
	keys := make([]string, 0, len(folders))
	for key := range folders {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		record := folders[key]
		b.WriteString("[folders.")
		b.WriteString(strconv.Quote(key))
		b.WriteString("]\ntrusted = ")
		b.WriteString(strconv.FormatBool(record.Trusted))
		b.WriteString("\ndecided_at = ")
		b.WriteString(strconv.FormatInt(record.DecidedAt, 10))
		b.WriteString("\n\n")
	}
	return b.String()
}
