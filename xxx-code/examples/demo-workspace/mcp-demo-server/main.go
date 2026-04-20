package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "xxx-code-demo-mcp",
		Version: "0.1.0",
	}, nil)

	server.AddResource(&sdkmcp.Resource{
		Name:        "demo-guide",
		Description: "Guide describing what this demo MCP server exposes.",
		URI:         "memory://demo-guide",
	}, func(ctx context.Context, req *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
		_ = ctx
		if req.Params.URI != "memory://demo-guide" {
			return nil, sdkmcp.ResourceNotFoundError(req.Params.URI)
		}
		return &sdkmcp.ReadResourceResult{
			Contents: []*sdkmcp.ResourceContents{{
				URI: "memory://demo-guide",
				Text: strings.Join([]string{
					"demo-guide",
					"",
					"- The MCP server exposes one tool called echo_text.",
					"- The MCP server exposes one prompt called review_demo.",
					"- The MCP server exists so xxx-code can demonstrate stdio MCP integration.",
				}, "\n"),
			}},
		}, nil
	})

	server.AddPrompt(&sdkmcp.Prompt{
		Name:        "review_demo",
		Description: "A prompt that asks the caller to review the demo workspace.",
		Arguments: []*sdkmcp.PromptArgument{{
			Name:        "topic",
			Description: "Focus area to review",
			Required:    true,
		}},
	}, func(ctx context.Context, req *sdkmcp.GetPromptRequest) (*sdkmcp.GetPromptResult, error) {
		_ = ctx
		topic := strings.TrimSpace(req.Params.Arguments["topic"])
		if topic == "" {
			topic = "the demo workspace"
		}
		return &sdkmcp.GetPromptResult{
			Description: "Demo workspace review prompt",
			Messages: []*sdkmcp.PromptMessage{{
				Role:    "user",
				Content: &sdkmcp.TextContent{Text: "Review " + topic + " and explain how plugin, MCP, and workflow fit together."},
			}},
		}, nil
	})

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "echo_text",
		Description: "Echo text back to xxx-code with a demo prefix.",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, input struct {
		Value string `json:"value" jsonschema:"Value to echo back to the caller."`
	}) (*sdkmcp.CallToolResult, map[string]string, error) {
		_ = ctx
		_ = req
		return nil, map[string]string{
			"echo": "demo: " + input.Value,
		}, nil
	})

	if err := server.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
