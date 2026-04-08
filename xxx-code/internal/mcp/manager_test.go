package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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

	if _, ok := registry.Get("list_mcp_resources"); !ok {
		t.Fatal("expected MCP resource listing tool to be registered")
	}
	if _, ok := registry.Get("read_mcp_resource"); !ok {
		t.Fatal("expected MCP resource read tool to be registered")
	}
	if _, ok := registry.Get("list_mcp_resource_templates"); !ok {
		t.Fatal("expected MCP resource template listing tool to be registered")
	}
	if _, ok := registry.Get("list_mcp_prompts"); !ok {
		t.Fatal("expected MCP prompt listing tool to be registered")
	}
	if _, ok := registry.Get("get_mcp_prompt"); !ok {
		t.Fatal("expected MCP prompt fetch tool to be registered")
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

	resourceListTool, _ := registry.Get("list_mcp_resources")
	resourceListResult, err := resourceListTool.Call(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resourceListResult.Content, `"memory://note"`) {
		t.Fatalf("expected listed resource, got %s", resourceListResult.Content)
	}

	readResourceTool, _ := registry.Get("read_mcp_resource")
	readInput, _ := json.Marshal(map[string]any{
		"server": "tester",
		"uri":    "memory://note",
	})
	readResult, err := readResourceTool.Call(context.Background(), nil, readInput)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readResult.Content, `hello resource`) {
		t.Fatalf("expected resource contents, got %s", readResult.Content)
	}

	templatesTool, _ := registry.Get("list_mcp_resource_templates")
	templateListResult, err := templatesTool.Call(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(templateListResult.Content, `"memory://{name}"`) {
		t.Fatalf("expected listed resource template, got %s", templateListResult.Content)
	}

	promptsTool, _ := registry.Get("list_mcp_prompts")
	promptListResult, err := promptsTool.Call(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(promptListResult.Content, `"review_code"`) {
		t.Fatalf("expected listed prompt, got %s", promptListResult.Content)
	}

	getPromptTool, _ := registry.Get("get_mcp_prompt")
	getPromptInput, _ := json.Marshal(map[string]any{
		"server":    "tester",
		"name":      "review_code",
		"arguments": map[string]string{"topic": "scheduler"},
	})
	getPromptResult, err := getPromptTool.Call(context.Background(), nil, getPromptInput)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(getPromptResult.Content, `Review scheduler carefully.`) {
		t.Fatalf("expected prompt message, got %s", getPromptResult.Content)
	}
}

func TestStartLoadsToolsFromRemoteHTTPAndSSEConfig(t *testing.T) {
	dir := t.TempDir()
	httpServer := newProtectedMCPHTTPServer(t, "http", "X-Test-Token", "streamable-secret")
	defer httpServer.Close()
	sseServer := newProtectedMCPHTTPServer(t, "sse", "X-Test-Token", "sse-secret")
	defer sseServer.Close()

	configPath := filepath.Join(dir, ".mcp.json")
	configJSON := fmt.Sprintf(`{
  "mcpServers": {
    "remote_http": {
      "transport": "streamable-http",
      "url": %q,
      "headers": {"X-Test-Token": "streamable-secret"}
    },
    "remote_sse": {
      "type": "sse",
      "url": %q,
      "headers": {"X-Test-Token": "sse-secret"}
    }
  }
}`, httpServer.URL, sseServer.URL)
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

	statuses := statusesByName(manager.Statuses())
	if len(statuses) != 2 {
		t.Fatalf("expected 2 MCP statuses, got %d", len(statuses))
	}
	if statuses["remote_http"].Status != ServerStatusConnected {
		t.Fatalf("expected remote_http to connect, got %+v", statuses["remote_http"])
	}
	if statuses["remote_sse"].Status != ServerStatusConnected {
		t.Fatalf("expected remote_sse to connect, got %+v", statuses["remote_sse"])
	}
	if statuses["remote_http"].URL != httpServer.URL {
		t.Fatalf("expected normalized http URL, got %+v", statuses["remote_http"])
	}
	if statuses["remote_sse"].URL != sseServer.URL {
		t.Fatalf("expected normalized sse URL, got %+v", statuses["remote_sse"])
	}

	for _, name := range []string{"mcp__remote_http__echo_text", "mcp__remote_sse__echo_text"} {
		tool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("expected bridged MCP tool %s to be registered", name)
		}
		input, _ := json.Marshal(map[string]any{"value": name})
		result, err := tool.Call(context.Background(), nil, input)
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("expected success result for %s, got error content: %s", name, result.Content)
		}
		if !strings.Contains(result.Content, name) {
			t.Fatalf("expected echoed payload for %s, got %q", name, result.Content)
		}
	}

	resourceListTool, _ := registry.Get("list_mcp_resources")
	resourceListResult, err := resourceListTool.Call(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resourceListResult.Content, `"remote_http"`) || !strings.Contains(resourceListResult.Content, `"remote_sse"`) {
		t.Fatalf("expected remote servers in resource listing, got %s", resourceListResult.Content)
	}

	readResourceTool, _ := registry.Get("read_mcp_resource")
	readInput, _ := json.Marshal(map[string]any{
		"server": "remote_http",
		"uri":    "memory://note",
	})
	readResult, err := readResourceTool.Call(context.Background(), nil, readInput)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readResult.Content, `hello resource`) {
		t.Fatalf("expected remote resource contents, got %s", readResult.Content)
	}

	promptsTool, _ := registry.Get("list_mcp_prompts")
	promptListResult, err := promptsTool.Call(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(promptListResult.Content, `"remote_http"`) || !strings.Contains(promptListResult.Content, `"remote_sse"`) {
		t.Fatalf("expected remote servers in prompt listing, got %s", promptListResult.Content)
	}

	getPromptTool, _ := registry.Get("get_mcp_prompt")
	getPromptInput, _ := json.Marshal(map[string]any{
		"server":    "remote_sse",
		"name":      "review_code",
		"arguments": map[string]string{"topic": "transports"},
	})
	getPromptResult, err := getPromptTool.Call(context.Background(), nil, getPromptInput)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(getPromptResult.Content, `Review transports carefully.`) {
		t.Fatalf("expected prompt message, got %s", getPromptResult.Content)
	}
}

func TestStartMarksUnsupportedTransportAsFailed(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".mcp.json")
	configJSON := `{
  "mcpServers": {
    "remote": {
      "type": "ws",
      "url": "https://example.com/mcp"
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

func TestStartMarksMissingRemoteURLAsFailed(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".mcp.json")
	configJSON := `{
  "mcpServers": {
    "remote": {
      "type": "http"
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
	if !strings.Contains(statuses[0].Error, "url cannot be empty") {
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

	server := newHelperServer()
	if err := server.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func newHelperServer() *sdkmcp.Server {
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
	server.AddResource(&sdkmcp.Resource{
		Name:        "note",
		Description: "A test resource",
		MIMEType:    "text/plain",
		URI:         "memory://note",
	}, func(ctx context.Context, req *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
		_ = ctx
		return &sdkmcp.ReadResourceResult{
			Contents: []*sdkmcp.ResourceContents{
				{
					URI:      req.Params.URI,
					MIMEType: "text/plain",
					Text:     "hello resource",
				},
			},
		}, nil
	})
	server.AddResourceTemplate(&sdkmcp.ResourceTemplate{
		Name:        "memory-note",
		Description: "Template for memory notes",
		MIMEType:    "text/plain",
		URITemplate: "memory://{name}",
	}, func(ctx context.Context, req *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
		_ = ctx
		u, err := url.Parse(req.Params.URI)
		if err != nil {
			return nil, err
		}
		return &sdkmcp.ReadResourceResult{
			Contents: []*sdkmcp.ResourceContents{
				{
					URI:      req.Params.URI,
					MIMEType: "text/plain",
					Text:     "template resource: " + strings.TrimPrefix(u.Host+u.Path, "/"),
				},
			},
		}, nil
	})
	server.AddPrompt(&sdkmcp.Prompt{
		Name:        "review_code",
		Description: "Ask for a focused code review",
		Arguments: []*sdkmcp.PromptArgument{
			{
				Name:        "topic",
				Description: "What to review",
				Required:    true,
			},
		},
	}, func(ctx context.Context, req *sdkmcp.GetPromptRequest) (*sdkmcp.GetPromptResult, error) {
		_ = ctx
		return &sdkmcp.GetPromptResult{
			Description: "review prompt",
			Messages: []*sdkmcp.PromptMessage{
				{
					Role:    "user",
					Content: &sdkmcp.TextContent{Text: "Review " + req.Params.Arguments["topic"] + " carefully."},
				},
			},
		}, nil
	})
	return server
}

func newProtectedMCPHTTPServer(t *testing.T, transport, header, value string) *httptest.Server {
	t.Helper()

	var handler http.Handler
	switch transport {
	case "http":
		handler = sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
			return newHelperServer()
		}, nil)
	case "sse":
		handler = sdkmcp.NewSSEHandler(func(*http.Request) *sdkmcp.Server {
			return newHelperServer()
		}, nil)
	default:
		t.Fatalf("unsupported test transport: %s", transport)
	}

	next := handler
	handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(header); got != value {
			http.Error(w, "missing required header", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
	return httptest.NewServer(handler)
}

func statusesByName(statuses []ServerStatus) map[string]ServerStatus {
	byName := make(map[string]ServerStatus, len(statuses))
	for _, status := range statuses {
		byName[status.Name] = status
	}
	return byName
}
