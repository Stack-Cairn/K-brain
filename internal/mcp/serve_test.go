package mcp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServeInProcess(t *testing.T) {
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, "test") }()

	t.Cleanup(func() {
		cancel()
		_ = inW.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return after stdin close + cancel")
		}
		os.Stdin, os.Stdout = oldIn, oldOut
	})

	cli := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := cli.Connect(ctx, &sdkmcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = cs.Close() }()

	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
	}
	if len(list.Tools) != 4 || !names["read"] || names["task"] {
		t.Fatalf("served tools = %v (want k-brain's 4, task excluded)", names)
	}

	res, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "read",
		Arguments: map[string]any{"path": "serve.go", "limit": 3},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	txt, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok || !strings.Contains(txt.Text, "package mcp") {
		t.Fatalf("read via MCP = %#v", res.Content)
	}

	res, err = cs.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "read",
		Arguments: map[string]any{"path": "does-not-exist.xyz"},
	})
	if err != nil {
		t.Fatalf("failing tool call should not be a protocol error: %v", err)
	}
	txt, ok = res.Content[0].(*sdkmcp.TextContent)
	if !ok || !strings.HasPrefix(txt.Text, "Error: ") {
		t.Fatalf("tool error surfaced as %#v", res.Content)
	}
}
