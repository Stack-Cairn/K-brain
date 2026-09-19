package ai

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"strings"
)

func imageSource(p ContentPart) (map[string]any, error) {
	if p.ImageURL == nil || p.ImageURL.URL == "" {
		return nil, fmt.Errorf("image_url requires a URL")
	}
	s := p.ImageURL.URL
	if strings.HasPrefix(s, "data:") {
		header, data, ok := strings.Cut(strings.TrimPrefix(s, "data:"), ";base64,")
		if !ok || !strings.HasPrefix(header, "image/") || data == "" {
			return nil, fmt.Errorf("image data URL must contain a media type and base64 data")
		}
		if _, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding, strings.NewReader(data))); err != nil {
			return nil, fmt.Errorf("image data URL contains invalid base64")
		}
		return map[string]any{"type": "base64", "media_type": header, "data": data}, nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("image URL must use HTTP, HTTPS, or base64 data")
	}
	return map[string]any{"type": "url", "url": s}, nil
}

func responsesContent(m Message) ([]any, error) {
	blocks := make([]any, 0, len(m.Parts)+1)
	for _, part := range m.ContentParts() {
		switch part.Type {
		case "text":
			kind := "input_text"
			if m.Role == "assistant" {
				kind = "output_text"
			}
			blocks = append(blocks, map[string]any{"type": kind, "text": part.Text})
		case "image_url":
			if m.Role != "user" && m.Role != "tool" {
				return nil, fmt.Errorf("Responses images require a user or tool message")
			}
			if _, err := imageSource(part); err != nil {
				return nil, err
			}
			blocks = append(blocks, map[string]any{"type": "input_image", "image_url": part.ImageURL.URL, "detail": "auto"})
		default:
			return nil, fmt.Errorf("unsupported message content type %q", part.Type)
		}
	}
	return blocks, nil
}

func anthropicContent(m Message) ([]any, error) {
	blocks := make([]any, 0, len(m.Parts)+1)
	for _, part := range m.ContentParts() {
		switch part.Type {
		case "text":
			if part.Text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": part.Text})
			}
		case "image_url":
			if m.Role != "user" && m.Role != "tool" {
				return nil, fmt.Errorf("Anthropic images require a user or tool message")
			}
			source, err := imageSource(part)
			if err != nil {
				return nil, err
			}
			if source["type"] == "base64" {
				switch source["media_type"] {
				case "image/jpeg", "image/png", "image/gif", "image/webp":
				default:
					return nil, fmt.Errorf("Anthropic image media type %q is unsupported", source["media_type"])
				}
			}
			blocks = append(blocks, map[string]any{"type": "image", "source": source})
		default:
			return nil, fmt.Errorf("unsupported message content type %q", part.Type)
		}
	}
	return blocks, nil
}

func chatMessages(messages []Message) ([]Message, error) {
	out := make([]Message, 0, len(messages))
	var toolImages []ContentPart
	flush := func() {
		if len(toolImages) > 0 {
			out = append(out, Message{Role: "user", Parts: toolImages})
			toolImages = nil
		}
	}
	for _, m := range messages {
		if m.Role != "tool" {
			flush()
		}
		var images []ContentPart
		for _, part := range m.Parts {
			switch part.Type {
			case "text":
			case "image_url":
				if m.Role != "user" && m.Role != "tool" {
					return nil, fmt.Errorf("Chat Completions images require a user or tool message")
				}
				if _, err := imageSource(part); err != nil {
					return nil, err
				}
				images = append(images, part)
			default:
				return nil, fmt.Errorf("unsupported message content type %q", part.Type)
			}
		}
		if m.Role == "tool" {
			m.Content = m.TextContent()
			m.Parts = nil
			if len(images) > 0 {
				toolImages = append(toolImages, ContentPart{Type: "text", Text: fmt.Sprintf("Images from tool %s (%s):", m.Name, m.ToolCallID)})
				toolImages = append(toolImages, images...)
			}
		}
		out = append(out, m)
	}
	flush()
	return out, nil
}
