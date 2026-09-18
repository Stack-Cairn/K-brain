package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	logFileName = "k-brain.log"

	logMaxBytes = 1 << 20
)

var logMu sync.Mutex

func LogEvent(op, detail string) {
	logMu.Lock()
	defer logMu.Unlock()
	dir, err := Dir()
	if err != nil {
		return
	}
	p := filepath.Join(dir, logFileName)
	if st, err := os.Stat(p); err == nil && st.Size() > logMaxBytes {
		_ = os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(f, "%s %-16s pid=%d %s\n", time.Now().UTC().Format(time.RFC3339), op, os.Getpid(), detail)
	_ = f.Close()
}

func logf(op, format string, args ...any) {
	LogEvent(op, fmt.Sprintf(format, args...))
}
