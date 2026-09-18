package workflow

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf16"
)

func homeDir() (string, error) {
	dir := os.Getenv("K_BRAIN_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".k-brain")
	}
	dir = filepath.Join(dir, "workflows")

	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o700); err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

var runCounter atomic.Int64

func GenerateRunID() string {
	n := runCounter.Add(1) % 0xffff
	var b [2]byte
	if _, err := rand.Read(b[:]); err == nil {
		return fmt.Sprintf("run-%x-%x-%s", time.Now().UnixMilli(), n, hex.EncodeToString(b[:]))
	}
	return fmt.Sprintf("run-%x-%x", time.Now().UnixMilli(), n)
}

func PersistScript(name, runID, script string) string {
	dir, err := homeDir()
	if err != nil {
		return ""
	}
	safe := sanitizeName(name)
	file := filepath.Join(dir, "scripts", safe+"-"+runID+".js")
	if err := os.WriteFile(file, []byte(script), 0o600); err != nil {
		return ""
	}
	return file
}

func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
		if b.Len() >= 50 {
			break
		}
	}
	if b.Len() == 0 {
		return "workflow"
	}
	return b.String()
}

func validRunID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.'
		if !ok {
			return false
		}
	}

	return !strings.Contains(id, "..")
}

type JournalEntry struct {
	Index  int    `json:"index"`
	Hash   string `json:"hash"`
	Result any    `json:"result"`
}

type PersistedRun struct {
	RunID      string         `json:"runId"`
	Name       string         `json:"name"`
	ScriptPath string         `json:"scriptPath,omitempty"`
	Status     string         `json:"status"`
	Args       any            `json:"args,omitempty"`
	Journal    []JournalEntry `json:"journal"`
	Result     any            `json:"result,omitempty"`
	Error      string         `json:"error,omitempty"`
	StartedAt  int64          `json:"startedAt"`
	FinishedAt int64          `json:"finishedAt,omitempty"`
}

func SaveRun(state *PersistedRun) {
	dir, err := homeDir()
	if err != nil {
		return
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "runs", state.RunID+".json"), data, 0o600)
}

func LoadRun(runID string) *PersistedRun {
	dir, err := homeDir()
	if err != nil {
		return nil
	}

	data, err := os.ReadFile(filepath.Join(dir, "runs", runID+".json"))
	if err != nil {
		return nil
	}
	var run PersistedRun
	if json.Unmarshal(data, &run) != nil {
		return nil
	}
	return &run
}

func JournalMap(run *PersistedRun) map[int]JournalEntry {
	m := map[int]JournalEntry{}
	if run == nil {
		return m
	}
	for _, e := range run.Journal {
		m[e.Index] = e
	}
	return m
}

func HashString(s string) string {
	h := int32(5381)
	for _, u := range utf16.Encode([]rune(s)) {
		h = (h << 5) + h + int32(u)
	}
	return strconvUint32Base36(uint32(h))
}

func strconvUint32Base36(n uint32) string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%36]
		n /= 36
	}
	return string(buf[i:])
}
