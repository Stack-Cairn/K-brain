package tui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func (m *model) exportCommand(arg string) {
	path := strings.TrimSpace(arg)
	if path == "" {
		path = "k-brain-transcript-" + m.sessionID + ".md"
	}
	if m.agent == nil || len(m.agent.Messages) == 0 {
		m.append(dimStyle.Render("(nothing to export yet)"))
		return
	}
	var err error
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jsonl":
		err = exportJSONL(path, m.sessionID, m.sessTitle, m.agent.Model, m.agent.Provider, m.agent.Messages)
	case ".html", ".htm":
		err = exportHTML(path, m.sessTitle, m.agent.Messages)
	default:
		err = exportTranscript(path, m.agent.Messages)
	}
	if err != nil {
		m.append(errStyle.Render("export failed: " + err.Error()))
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	m.append(dimStyle.Render("⤓ transcript exported → " + abs))
}

func (m *model) importCommand(arg string) {
	path := strings.TrimSpace(arg)
	if path == "" {
		m.append(errStyle.Render("usage: /import <jsonl path>"))
		return
	}
	if m.busy {
		m.append(dimStyle.Render("(cannot import while a turn is running)"))
		return
	}
	msgs, err := importJSONL(path)
	if err != nil {
		m.append(errStyle.Render("import failed: " + err.Error()))
		return
	}
	if len(msgs) > 0 && msgs[0].Role == "system" && len(m.agent.Messages) > 0 && m.agent.Messages[0].Role == "system" {
		msgs = msgs[1:]
	}
	if len(msgs) == 0 {
		m.append(dimStyle.Render("(nothing to import)"))
		return
	}
	m.agent.Messages = append(m.agent.Messages, msgs...)
	m.rebuildTranscript()
	m.persist()
	m.append(dimStyle.Render(fmt.Sprintf("↥ imported %d messages", len(msgs))))
}

type exportRecord struct {
	Type     string      `json:"type"`
	Version  int         `json:"version,omitempty"`
	ID       string      `json:"id,omitempty"`
	Title    string      `json:"title,omitempty"`
	Model    string      `json:"model,omitempty"`
	Provider string      `json:"provider,omitempty"`
	CWD      string      `json:"cwd,omitempty"`
	Seq      int         `json:"seq,omitempty"`
	Message  *ai.Message `json:"message,omitempty"`
	Payload  interface{} `json:"payload,omitempty"`
}

func exportJSONL(path, id, title, model, provider string, msgs []ai.Message) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(exportRecord{Type: "session", Version: 1, ID: id, Title: title, Model: model, Provider: provider}); err != nil {
		return err
	}
	for i := range msgs {
		if err := enc.Encode(exportRecord{Type: "message", Seq: i, Message: &msgs[i]}); err != nil {
			return err
		}
	}
	return f.Sync()
}

func exportHTML(path, title string, msgs []ai.Message) error {
	var b strings.Builder
	b.WriteString("<!doctype html><html><head><meta charset=\"utf-8\"><title>")
	b.WriteString(html.EscapeString(title))
	b.WriteString("</title><style>body{font:15px system-ui,sans-serif;max-width:1000px;margin:2rem auto;padding:0 1rem;background:#111;color:#eee}article{margin:1rem 0;padding:1rem;border:1px solid #444;border-radius:8px}h2{font-size:1rem;color:#8ab4f8}pre{white-space:pre-wrap;overflow-wrap:anywhere}</style></head><body>")
	if title != "" {
		b.WriteString("<h1>")
		b.WriteString(html.EscapeString(title))
		b.WriteString("</h1>")
	}
	for _, msg := range msgs {
		b.WriteString("<article><h2>")
		b.WriteString(html.EscapeString(displayRole(msg.Role)))
		b.WriteString("</h2><pre>")
		b.WriteString(html.EscapeString(msg.TextContent()))
		b.WriteString("</pre>")
		for _, tc := range msg.ToolCalls {
			b.WriteString("<code>")
			b.WriteString(html.EscapeString(tc.Function.Name))
			b.WriteString("</code>")
		}
		b.WriteString("</article>")
	}
	b.WriteString("</body></html>\n")
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func importJSONL(path string) ([]ai.Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []ai.Message
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 16<<20)
	for line := 1; scanner.Scan(); line++ {
		data := scanner.Bytes()
		var rec exportRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if rec.Type == "message" && rec.Message != nil {
			out = append(out, *rec.Message)
			continue
		}
		if rec.Type == "message" && rec.Payload != nil {
			payload, ok := rec.Payload.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("line %d: invalid message payload", line)
			}
			rawMessage, exists := payload["message"]
			if !exists || rawMessage == nil {
				return nil, fmt.Errorf("line %d: missing message payload", line)
			}
			encoded, err := json.Marshal(rawMessage)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			var msg ai.Message
			if err := json.Unmarshal(encoded, &msg); err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			out = append(out, msg)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func exportTranscript(path string, msgs []ai.Message) error {
	var b strings.Builder
	b.WriteString("# Session transcript\n\n")
	for _, msg := range msgs {
		switch msg.Role {
		case "tool":
			b.WriteString("#### Tool result\n\n" + msg.TextContent() + "\n\n")
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n", displayRole(msg.Role))
		if c := msg.TextContent(); c != "" {
			b.WriteString(c + "\n\n")
		}
		for _, tc := range msg.ToolCalls {
			fmt.Fprintf(&b, "`%s`\n\n", tc.Function.Name)
		}
	}

	return os.WriteFile(path, []byte(strings.TrimRight(b.String(), "\n")+"\n"), 0o600)
}

func displayRole(role string) string {
	switch role {
	case "user":
		return "User"
	case "assistant":
		return "Assistant"
	case "system":
		return "System"
	default:

		if role == "" {
			return role
		}
		return strings.ToUpper(role[:1]) + role[1:]
	}
}
