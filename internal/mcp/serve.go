package mcp

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func Serve(ctx context.Context, version string) error {
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "k-brain", Version: version}, nil)
	for _, t := range tools.All() {
		srv.AddTool(&sdkmcp.Tool{
			Name:        t.Def.Function.Name,
			Description: t.Def.Function.Description,

			InputSchema: t.Def.Function.Parameters,
		}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			out, err := t.Run(ctx, req.Params.Arguments)
			if err != nil {
				out = "Error: " + err.Error()
			}
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: out}},
			}, nil
		})
	}
	if err := srv.Run(ctx, &sdkmcp.StdioTransport{}); err != nil {
		return fmt.Errorf("mcp serve: %w", err)
	}
	return nil
}
