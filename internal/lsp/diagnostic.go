package lsp

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const (
	SeverityError   = 1
	SeverityWarning = 2
	SeverityInfo    = 3
	SeverityHint    = 4
)

type Diagnostic struct {
	Line     int
	Col      int
	Severity int
	Message  string
}

const (
	maxPerFile      = 20
	maxSiblingFiles = 5
)

const maxMsgLen = 300

func format(d Diagnostic) string {
	sev := "ERROR"
	switch d.Severity {
	case SeverityWarning:
		sev = "WARN"
	case SeverityInfo:
		sev = "INFO"
	case SeverityHint:
		sev = "HINT"
	}
	msg := d.Message
	if len(msg) > maxMsgLen {
		msg = msg[:maxMsgLen] + "…"
	}
	return fmt.Sprintf("%s [%d:%d] %s", sev, d.Line, d.Col, msg)
}

func block(file string, diags []Diagnostic) string {
	var kept []Diagnostic
	for _, d := range diags {
		if d.Severity <= SeverityWarning {
			kept = append(kept, d)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	over := 0
	if len(kept) > maxPerFile {
		over = len(kept) - maxPerFile
		kept = kept[:maxPerFile]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n<diagnostics file=%q>\n", file)
	for _, d := range kept {
		b.WriteString(format(d))
		b.WriteByte('\n')
	}
	if over > 0 {
		fmt.Fprintf(&b, "... and %d more\n", over)
	}
	b.WriteString("</diagnostics>")
	return b.String()
}

func Report(edited string, editedDiags []Diagnostic, siblings map[string][]Diagnostic) string {
	var sb strings.Builder
	sb.WriteString(block(edited, editedDiags))
	var names []string
	for p, diags := range siblings {
		if p == edited {
			continue
		}
		for _, d := range diags {
			if d.Severity == SeverityError {
				names = append(names, p)
				break
			}
		}
	}
	if len(names) == 0 {
		return sb.String()
	}
	sort.Strings(names)
	shown := min(len(names), maxSiblingFiles)
	for _, p := range names[:shown] {
		sb.WriteString(block(p, siblings[p]))
	}
	plural := "file"
	if len(names) > 1 {
		plural = "files"
	}
	if len(names) > shown {
		fmt.Fprintf(&sb, "\n(this edit introduced errors in %d other %s, %d shown; fix them too)", len(names), plural, shown)
	} else {
		fmt.Fprintf(&sb, "\n(this edit introduced errors in %s; fix them too)", plural)
	}
	return sb.String()
}

func siblingErrors(edited string, all map[string][]Diagnostic) map[string][]Diagnostic {
	dir := filepath.Dir(edited)
	out := map[string][]Diagnostic{}
	for p, ds := range all {
		if p == edited || filepath.Dir(p) != dir {
			continue
		}
		kept := make([]Diagnostic, len(ds))
		copy(kept, ds)
		for _, d := range ds {
			if d.Severity == SeverityError {
				out[p] = kept
				break
			}
		}
	}
	return out
}
