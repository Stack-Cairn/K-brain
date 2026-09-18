package acp

import (
	"os"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func osReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func llmTool(name string) ai.Tool {
	return ai.NewTool(name, name, `{"type":"object","properties":{}}`)
}

func checkGateForTest(tool, command string) string {
	if tools.Gate == nil {
		return ""
	}
	decision, redirect := tools.Gate(tools.GateRequest{
		Tool:    tool,
		Command: command,
		Rule:    tools.CommandRule(command),
	})
	switch decision {
	case tools.GateReject:
		if redirect == "" {
			redirect = "the user rejected this action"
		}
		return "Permission denied: " + redirect
	default:
		return ""
	}
}

type errStringT string

func (e errStringT) Error() string { return string(e) }

func errString(s string) error { return errStringT(s) }
