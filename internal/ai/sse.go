package ai

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const maxSSEEventSize = 10 * 1024 * 1024

type sseEvent struct {
	Type string
	Data string
}

type sseReader struct {
	scanner *bufio.Scanner
	err     error
	sawData bool
	preview string
}

func newSSEReader(r io.Reader) *sseReader {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), maxSSEEventSize)
	return &sseReader{scanner: s}
}

func (s *sseReader) next() (sseEvent, bool) {
	var event sseEvent
	var data strings.Builder
	hasData := false
	for s.err == nil && s.scanner.Scan() {
		line := s.scanner.Text()
		if line == "" {
			if hasData {
				event.Data = strings.TrimSuffix(data.String(), "\n")
				s.sawData = true
				return event, true
			}
			event = sseEvent{}
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event.Type = value
		case "data":
			if data.Len()+len(value)+1 > maxSSEEventSize {
				s.err = nonRetryable{fmt.Errorf("SSE event exceeds %d bytes", maxSSEEventSize)}
				return sseEvent{}, false
			}
			hasData = true
			data.WriteString(value)
			data.WriteByte('\n')
		case "", "id", "retry":
		default:
			if s.preview == "" {
				s.preview = line[:min(len(line), 160)]
			}
		}
	}
	if s.err == nil {
		s.err = s.scanner.Err()
	}
	if s.err == nil && hasData {
		s.err = io.ErrUnexpectedEOF
	}
	return sseEvent{}, false
}

func (s *sseReader) endError(protocol string) error {
	if s.err != nil {
		return s.err
	}
	if !s.sawData {
		preview := s.preview
		if preview == "" {
			preview = "empty response"
		}
		return nonRetryable{fmt.Errorf("invalid %s streaming response: expected SSE data, got %q", protocol, preview)}
	}
	return fmt.Errorf("%s stream ended before a completion event: %w", protocol, io.ErrUnexpectedEOF)
}

func decodeStreamEvent(data string, value any) error {
	if !strings.HasPrefix(strings.TrimSpace(data), "{") {
		return nonRetryable{fmt.Errorf("invalid streaming JSON: expected an object")}
	}
	if err := json.Unmarshal([]byte(data), value); err != nil {
		return nonRetryable{fmt.Errorf("invalid streaming JSON: %w", err)}
	}
	return nil
}

func providerStreamError(raw json.RawMessage, fallback string) error {
	var e struct{ Message, Code, Type string }
	_ = json.Unmarshal(raw, &e)
	detail := e.Message
	if detail == "" {
		detail = fallback
	}
	code := e.Code
	if code == "" {
		code = e.Type
	}
	if code != "" {
		detail = code + ": " + detail
	}
	return nonRetryable{fmt.Errorf("API stream error: %s", detail)}
}
