package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type helperInput struct {
	Value string `json:"value" jsonschema:"value to echo back"`
}

type helperOutput struct {
	Echo string `json:"echo"`
}

func TestStartLoadsToolsFromDefaultConfig(t *testing.T) {
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(dir, ".mcp.json")
	configJSON := fmt.Sprintf(`{
  "mcpServers": {
    "tester": {
      "command": %q,
      "args": ["-test.run=TestMCPHelperProcess", "--", "mcp-echo-server"],
      "env": {"GO_WANT_MCP_HELPER": "1"}
    }
  }
}`, exe)
	if err := os.WriteFile(configPath, []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	registry := engine.NewRegistry()
	manager, err := Start(context.Background(), registry, Options{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if manager == nil {
		t.Fatal("expected MCP manager to be created")
	}
	defer func() {
		if err := manager.Close(); err != nil {
			t.Fatalf("close manager: %v", err)
		}
	}()

	statuses := manager.Statuses()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 MCP status, got %d", len(statuses))
	}
	if statuses[0].Status != ServerStatusConnected {
		t.Fatalf("expected connected status, got %+v", statuses[0])
	}
	if len(statuses[0].ToolNames) != 1 {
		t.Fatalf("expected 1 bridged tool, got %+v", statuses[0])
	}

	tool, ok := registry.Get("mcp__tester__echo_text")
	if !ok {
		t.Fatal("expected bridged MCP tool to be registered")
	}

	input, _ := json.Marshal(map[string]any{"value": "hello from MCP"})
	result, err := tool.Call(context.Background(), nil, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected success result, got error content: %s", result.Content)
	}
	if !strings.Contains(result.Content, "hello from MCP") {
		t.Fatalf("expected echoed payload, got %q", result.Content)
	}
}

func TestStartMarksUnsupportedTransportAsFailed(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".mcp.json")
	configJSON := `{
  "mcpServers": {
    "remote": {
      "type": "http",
      "command": "ignored"
    }
  }
}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	manager, err := Start(context.Background(), engine.NewRegistry(), Options{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if manager == nil {
		t.Fatal("expected manager even when server load fails")
	}

	statuses := manager.Statuses()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	if statuses[0].Status != ServerStatusFailed {
		t.Fatalf("expected failed status, got %+v", statuses[0])
	}
	if !strings.Contains(statuses[0].Error, "unsupported MCP transport") {
		t.Fatalf("unexpected error: %+v", statuses[0])
	}
}

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") != "1" {
		return
	}

	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "mcp-echo-server" {
		os.Exit(2)
	}

	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "helper-server",
		Version: "1.0.0",
	}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "echo_text",
		Description: "Echo text back to the caller",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, input helperInput) (*sdkmcp.CallToolResult, helperOutput, error) {
		_ = ctx
		_ = req
		return nil, helperOutput{Echo: input.Value}, nil
	})

	if err := server.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
