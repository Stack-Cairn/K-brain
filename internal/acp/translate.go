package acp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func toolKind(name string) acp.ToolKind {
	switch name {
	case "read":
		return acp.ToolKindRead
	case "write", "edit":
		return acp.ToolKindEdit
	case "bash":
		return acp.ToolKindExecute
	case "task":
		return acp.ToolKindThink
	default:

		return acp.ToolKindOther
	}
}

func pathArg(name, args string) string {
	switch name {
	case "read", "write", "edit":
	default:
		return ""
	}
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return ""
	}
	return a.Path
}

func toolTitle(name, args string) string {
	if p := pathArg(name, args); p != "" {
		verb := map[string]string{"read": "Read", "write": "Write", "edit": "Edit"}[name]
		return verb + " " + p
	}
	switch name {
	case "bash":
		var a struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(args), &a); err == nil && a.Command != "" {
			return "$ " + a.Command
		}
		return "Run command"
	case "task":
		var a struct {
			Description string `json:"description"`
		}
		if err := json.Unmarshal([]byte(args), &a); err == nil && a.Description != "" {
			return "Subagent: " + a.Description
		}
		return "Subagent"
	}
	return name
}

func startToolCall(id, name, args string) acp.SessionUpdate {
	opts := []acp.ToolCallStartOpt{
		acp.WithStartKind(toolKind(name)),
		acp.WithStartStatus(acp.ToolCallStatusInProgress),
	}
	if p := pathArg(name, args); p != "" {
		opts = append(opts, acp.WithStartLocations([]acp.ToolCallLocation{{Path: p}}))
	}
	var raw any
	if json.Unmarshal([]byte(args), &raw) == nil {
		opts = append(opts, acp.WithStartRawInput(raw))
	}
	return acp.StartToolCall(acp.ToolCallId(id), toolTitle(name, args), opts...)
}

func todoStatusToACP(s string) acp.PlanEntryStatus {
	switch s {
	case "in_progress":
		return acp.PlanEntryStatusInProgress
	case "completed":
		return acp.PlanEntryStatusCompleted
	default:
		return acp.PlanEntryStatusPending
	}
}

func isErrorResult(result string) bool {
	return strings.HasPrefix(result, "Error: ")
}

func endToolCall(id, name, args, result string) acp.SessionUpdate {
	status := acp.ToolCallStatusCompleted
	if isErrorResult(result) {
		status = acp.ToolCallStatusFailed
	}
	return acp.UpdateToolCall(acp.ToolCallId(id),
		acp.WithUpdateStatus(status),
		acp.WithUpdateContent(toolCallContent(name, args, result)),
	)
}

func toolCallContent(name, args, result string) []acp.ToolCallContent {
	out := []acp.ToolCallContent{acp.ToolContent(acp.TextBlock(result))}
	if isErrorResult(result) {
		return out
	}
	switch name {
	case "write":
		var a struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if json.Unmarshal([]byte(args), &a) == nil && a.Path != "" {
			out = append(out, acp.ToolDiffContent(a.Path, a.Content))
		}
	case "edit":
		var a struct {
			Path      string `json:"path"`
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		}
		if json.Unmarshal([]byte(args), &a) == nil && a.Path != "" {
			out = append(out, acp.ToolDiffContent(a.Path, a.NewString, a.OldString))
		}
	}
	return out
}

func promptFromBlocks(blocks []acp.ContentBlock, vision bool) (text string, parts []ai.ContentPart) {
	var sb strings.Builder
	sep := func() {
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
	}
	for _, b := range blocks {
		switch {
		case b.Text != nil:
			sep()
			sb.WriteString(b.Text.Text)
		case b.ResourceLink != nil:

			sep()
			fmt.Fprintf(&sb, "@%s", b.ResourceLink.Uri)
		case b.Resource != nil:
			sep()
			r := b.Resource.Resource
			switch {
			case r.TextResourceContents != nil:
				fmt.Fprintf(&sb, "File: %s\n```\n%s\n```", r.TextResourceContents.Uri, r.TextResourceContents.Text)
			case r.BlobResourceContents != nil:
				fmt.Fprintf(&sb, "[binary resource: %s]", r.BlobResourceContents.Uri)
			}
		case b.Image != nil:
			if vision {
				if data, err := base64.StdEncoding.DecodeString(b.Image.Data); err == nil {
					ext, data := ai.NormalizeImage(mimeExt(b.Image.MimeType), data)
					parts = append(parts, ai.ImagePart(ext, data))
					continue
				}
			}
			sep()
			fmt.Fprintf(&sb, "[image: %s]", b.Image.MimeType)
		case b.Audio != nil:
			sep()
			fmt.Fprintf(&sb, "[audio: %s — not supported]", b.Audio.MimeType)
		}
	}
	return sb.String(), parts
}

func mimeExt(mime string) string {
	return strings.TrimPrefix(mime, "image/")
}

func replayUpdates(msgs []ai.Message) []acp.SessionUpdate {
	var out []acp.SessionUpdate

	argsByID := map[string]struct{ name, args string }{}
	for _, m := range msgs {
		switch m.Role {
		case "user":
			if t := strings.TrimSpace(m.TextContent()); t != "" {
				out = append(out, acp.UpdateUserMessageText(t))
			}
		case "assistant":
			if t := m.Content; t != "" {
				out = append(out, acp.UpdateAgentMessageText(t))
			}
			for _, tc := range m.ToolCalls {
				argsByID[tc.ID] = struct{ name, args string }{tc.Function.Name, tc.Function.Arguments}
				out = append(out, acp.StartToolCall(acp.ToolCallId(tc.ID),
					toolTitle(tc.Function.Name, tc.Function.Arguments),
					acp.WithStartKind(toolKind(tc.Function.Name)),
					acp.WithStartStatus(acp.ToolCallStatusInProgress),
				))
			}
		case "tool":
			info, ok := argsByID[m.ToolCallID]
			if !ok {
				continue
			}
			out = append(out, endToolCall(m.ToolCallID, info.name, info.args, m.Content))
		}
	}
	return out
}
