package acp

import (
	"os"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func osReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func llmTool(name string) ai.Tool {
	return ai.NewTool(name, name, `{"type":"object","properties":{}}`)
}

type errStringT string

func (e errStringT) Error() string { return string(e) }

func errString(s string) error { return errStringT(s) }
