package backend

import (
	"os"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/mcp/mcptest"
	"github.com/Stack-Cairn/K-brain/internal/testhome"
)

func TestMain(m *testing.M) {
	if os.Getenv(mcptest.ElicitServerEnv) != "" {
		// Started by elicitations_test.go as a stdio MCP server.
		_ = mcptest.ServeElicit(os.Stdin, os.Stdout)
		os.Exit(0)
	}
	os.Exit(testhome.Run(m))
}
