package permissions

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

const (
	fileVersion = 1
	// userRulesFile holds user-scope rules in the K-brain data directory.
	userRulesFile = "permission-rules.json"
	// legacyRulesFile is the TUI's old list of "tool:rule" strings.
	legacyRulesFile = "permissions.json"
)

// StoredRule is one rule as kept on disk.
type StoredRule struct {
	ID            string    `json:"id"`
	Rule          string    `json:"rule"`
	Effect        Effect    `json:"effect"`
	CreatedAt     time.Time `json:"created_at"`
	SourceSession string    `json:"source_session,omitempty"`
}

// RuleSet is the rules of one scope.
type RuleSet struct {
	Scope Scope
	Rules []StoredRule
}

// File is the on-disk format of every scope.
type File struct {
	Version        int          `json:"version"`
	ProjectRoot    string       `json:"project_root,omitempty"`
	ImportedLegacy bool         `json:"imported_legacy,omitempty"`
	Rules          []StoredRule `json:"rules"`
}

// fileMu serializes writes within this process and keeps reads out of them: on
// Windows a file another handle has open cannot be replaced, so an overlapping read
// would make the write fail. Writes replace the file atomically, so a writer in
// another process can lose an update but never leaves a torn file.
var fileMu sync.RWMutex

// UserRulesPath is the user-scope rules file.
func UserRulesPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, userRulesFile), nil
}

// ProjectRulesPath is the project-scope rules file for an absolute project root. It
// lives in the K-brain data directory so that remembering a rule never writes into
// the user's repository.
func ProjectRulesPath(root string) (string, error) {
	key, err := projectKey(root)
	if err != nil {
		return "", err
	}
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(dir, "permissions", "projects", hex.EncodeToString(sum[:8])+".json"), nil
}

func projectKey(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("project root %q must be an absolute path", root)
	}
	key := filepath.ToSlash(filepath.Clean(root))
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key, nil
}

// LoadFile reads a rules file. A missing file is an empty rule set; a corrupt one is
// an error, never silently an empty set.
func LoadFile(path string) (File, error) {
	fileMu.RLock()
	defer fileMu.RUnlock()
	return loadFile(path)
}

func loadFile(path string) (File, error) {
	data, err := readShared(path)
	if errors.Is(err, fs.ErrNotExist) {
		return File{Version: fileVersion}, nil
	}
	if err != nil {
		return File{}, err
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return File{}, fmt.Errorf("read permission rules %s: %w", path, err)
	}
	if f.Version > fileVersion {
		return File{}, fmt.Errorf("permission rules %s: unsupported version %d", path, f.Version)
	}
	f.Version = fileVersion
	return f, nil
}

func saveFile(path string, f File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f.Version = fileVersion
	if f.Rules == nil {
		f.Rules = []StoredRule{}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".permission-rules-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return renameRetry(tmp.Name(), path)
}

// renameRetry replaces path. On Windows the rename fails for a moment while another
// process holds either file without delete sharing (antivirus and the indexer open
// freshly written files), so it retries for about four seconds.
func renameRetry(from, to string) error {
	err := os.Rename(from, to)
	for attempt := 1; err != nil && runtime.GOOS == "windows" && attempt <= 40; attempt++ {
		time.Sleep(time.Duration(min(attempt, 10)) * 10 * time.Millisecond)
		err = os.Rename(from, to)
	}
	return err
}

func newRuleID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "r_" + hex.EncodeToString(b[:])
}

// AddRule stores a rule, normalized, unless the same rule with the same effect is
// already there; it returns the stored entry either way. projectRoot is recorded in
// project files so that they can be listed by their project.
func AddRule(path, projectRoot string, rule StoredRule) (StoredRule, error) {
	r, err := ParseRule(rule.Rule)
	if err != nil {
		return StoredRule{}, err
	}
	if rule.Effect != Allow && rule.Effect != Deny {
		return StoredRule{}, fmt.Errorf("permission rule effect %q must be allow or deny", rule.Effect)
	}
	rule.Rule = r.String()
	fileMu.Lock()
	defer fileMu.Unlock()
	f, err := loadFile(path)
	if err != nil {
		return StoredRule{}, err
	}
	for _, existing := range f.Rules {
		if existing.Rule == rule.Rule && existing.Effect == rule.Effect {
			return existing, nil
		}
	}
	if rule.ID == "" {
		rule.ID = newRuleID()
	}
	if rule.CreatedAt.IsZero() {
		rule.CreatedAt = time.Now().UTC()
	}
	if projectRoot != "" {
		f.ProjectRoot = projectRoot
	}
	f.Rules = append(f.Rules, rule)
	if err := saveFile(path, f); err != nil {
		return StoredRule{}, err
	}
	return rule, nil
}

// RemoveRule deletes a rule by ID and reports whether it existed.
func RemoveRule(path, id string) (bool, error) {
	fileMu.Lock()
	defer fileMu.Unlock()
	f, err := loadFile(path)
	if err != nil {
		return false, err
	}
	kept := f.Rules[:0]
	removed := false
	for _, r := range f.Rules {
		if r.ID == id {
			removed = true
			continue
		}
		kept = append(kept, r)
	}
	if !removed {
		return false, nil
	}
	f.Rules = kept
	return true, saveFile(path, f)
}

// LoadRuleSet reads one scope's rules file.
func LoadRuleSet(path string, scope Scope) (RuleSet, error) {
	f, err := LoadFile(path)
	if err != nil {
		return RuleSet{Scope: scope}, err
	}
	return RuleSet{Scope: scope, Rules: f.Rules}, nil
}

// LoadUserRules reads the user-scope rules. The first time, when no rules file exists
// yet, it imports the TUI's legacy permissions.json and leaves that file as it is.
func LoadUserRules() (RuleSet, error) {
	path, err := UserRulesPath()
	if err != nil {
		return RuleSet{Scope: ScopeUser}, err
	}
	fileMu.Lock()
	defer fileMu.Unlock()
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if imported, ok := importLegacy(filepath.Join(filepath.Dir(path), legacyRulesFile)); ok {
			if err := saveFile(path, imported); err != nil {
				return RuleSet{Scope: ScopeUser, Rules: imported.Rules}, err
			}
			return RuleSet{Scope: ScopeUser, Rules: imported.Rules}, nil
		}
	}
	f, err := loadFile(path)
	if err != nil {
		return RuleSet{Scope: ScopeUser}, err
	}
	return RuleSet{Scope: ScopeUser, Rules: f.Rules}, nil
}

// legacyShellPattern keeps an old bash rule's reach. The TUI compared the rule with
// CommandRule(command), so `git checkout` covered every `git checkout ...` while a
// bare `kubectl` covered only `kubectl` itself, because CommandRule of any longer
// kubectl command keeps two words.
func legacyShellPattern(target string) string {
	words := strings.Fields(target)
	padded := append(append([]string(nil), words...), "\x00", "\x00", "\x00")
	if len(CommandPrefix(padded)) == len(words) {
		return target + ":*"
	}
	return target
}

// importLegacy converts the TUI's "tool:rule" entries. Old bash rules were command
// prefixes, so they become prefix rules; other tools stored the exact target.
func importLegacy(path string) (File, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, false
	}
	var list []string
	if json.Unmarshal(data, &list) != nil {
		return File{}, false
	}
	f := File{Version: fileVersion, ImportedLegacy: true}
	now := time.Now().UTC()
	seen := map[string]bool{}
	for _, entry := range list {
		tool, target, ok := strings.Cut(entry, ":")
		if !ok {
			continue
		}
		tool, target = strings.TrimSpace(tool), strings.TrimSpace(target)
		text := tool
		switch {
		case kindOfTool(tool) == kindShell:
			if target == "" {
				continue
			}
			text = tool + "(" + legacyShellPattern(target) + ")"
		case target != "":
			text = tool + "(" + target + ")"
		}
		r, err := ParseRule(text)
		if err != nil || seen[r.String()] {
			continue
		}
		seen[r.String()] = true
		f.Rules = append(f.Rules, StoredRule{ID: newRuleID(), Rule: r.String(), Effect: Allow, CreatedAt: now})
	}
	return f, true
}
