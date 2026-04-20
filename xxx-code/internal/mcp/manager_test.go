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
	"sync"
	"testing"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	"github.com/gorilla/websocket"
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
	if _, ok := registry.Get("mcp_health"); !ok {
		t.Fatal("expected MCP health tool to be registered")
	}
	if _, ok := registry.Get("mcp_reload"); !ok {
		t.Fatal("expected MCP reload tool to be registered")
	}
	if _, ok := registry.Get("mcp_validate"); !ok {
		t.Fatal("expected MCP validate tool to be registered")
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

	if got := manager.ConfigPath(); got != configPath {
		t.Fatalf("expected manager config path %q, got %q", configPath, got)
	}
	if got := manager.ServerCount(); got != 1 {
		t.Fatalf("expected one connected MCP server, got %d", got)
	}
	if got := manager.ToolCount(); got != 1 {
		t.Fatalf("expected one connected MCP tool, got %d", got)
	}
	if report := manager.ValidationReport(); !report.Present || report.ConfigPath != configPath {
		t.Fatalf("expected validation report for current config, got %+v", report)
	}

	healthTool, _ := registry.Get("mcp_health")
	healthResult, err := healthTool.Call(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(healthResult.Content, `"status": "connected"`) {
		t.Fatalf("expected live MCP health output, got %s", healthResult.Content)
	}

	reloadTool, _ := registry.Get("mcp_reload")
	reloadResult, err := reloadTool.Call(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reloadResult.Content, configPath) {
		t.Fatalf("expected reload output to mention config path, got %s", reloadResult.Content)
	}

	validateTool, _ := registry.Get("mcp_validate")
	validateResult, err := validateTool.Call(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(validateResult.Content, `"present": true`) {
		t.Fatalf("expected validation output to report present config, got %s", validateResult.Content)
	}
}

func TestStartLoadsToolsFromRemoteHTTPAndSSEAndWSConfig(t *testing.T) {
	dir := t.TempDir()
	httpServer := newProtectedMCPHTTPServer(t, "http", "X-Test-Token", "streamable-secret")
	defer httpServer.Close()
	sseServer := newProtectedMCPHTTPServer(t, "sse", "X-Test-Token", "sse-secret")
	defer sseServer.Close()
	wsServer := newProtectedMCPWSServer(t, "X-Test-Token", "ws-secret")
	defer wsServer.Close()

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
    },
    "remote_ws": {
      "transport": "websocket",
      "url": %q,
      "headers": {"X-Test-Token": "ws-secret"}
    }
  }
}`, httpServer.URL, sseServer.URL, wsTestURL(wsServer.URL))
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
	if len(statuses) != 3 {
		t.Fatalf("expected 3 MCP statuses, got %d", len(statuses))
	}
	if statuses["remote_http"].Status != ServerStatusConnected {
		t.Fatalf("expected remote_http to connect, got %+v", statuses["remote_http"])
	}
	if statuses["remote_sse"].Status != ServerStatusConnected {
		t.Fatalf("expected remote_sse to connect, got %+v", statuses["remote_sse"])
	}
	if statuses["remote_ws"].Status != ServerStatusConnected {
		t.Fatalf("expected remote_ws to connect, got %+v", statuses["remote_ws"])
	}
	if statuses["remote_http"].URL != httpServer.URL {
		t.Fatalf("expected normalized http URL, got %+v", statuses["remote_http"])
	}
	if statuses["remote_sse"].URL != sseServer.URL {
		t.Fatalf("expected normalized sse URL, got %+v", statuses["remote_sse"])
	}
	if statuses["remote_ws"].URL != wsTestURL(wsServer.URL) {
		t.Fatalf("expected normalized ws URL, got %+v", statuses["remote_ws"])
	}

	for _, name := range []string{"mcp__remote_http__echo_text", "mcp__remote_sse__echo_text", "mcp__remote_ws__echo_text"} {
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
	if !strings.Contains(resourceListResult.Content, `"remote_http"`) || !strings.Contains(resourceListResult.Content, `"remote_sse"`) || !strings.Contains(resourceListResult.Content, `"remote_ws"`) {
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
	if !strings.Contains(promptListResult.Content, `"remote_http"`) || !strings.Contains(promptListResult.Content, `"remote_sse"`) || !strings.Contains(promptListResult.Content, `"remote_ws"`) {
		t.Fatalf("expected remote servers in prompt listing, got %s", promptListResult.Content)
	}

	getPromptTool, _ := registry.Get("get_mcp_prompt")
	getPromptInput, _ := json.Marshal(map[string]any{
		"server":    "remote_ws",
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

func TestExampleMCPConfigsValidate(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(cwd, "..", ".."))
	matches, err := filepath.Glob(filepath.Join(root, "examples", "mcp", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("expected example MCP configs to exist")
	}

	for _, configPath := range matches {
		t.Run(filepath.Base(configPath), func(t *testing.T) {
			report := ValidateOptions(Options{
				WorkingDir: root,
				ConfigFile: configPath,
			})
			if !report.Present {
				t.Fatalf("expected example config to be present, got %+v", report)
			}
			if !report.Valid {
				t.Fatalf("expected example config to validate, got %+v", report)
			}
			if report.ServerCount == 0 {
				t.Fatalf("expected example config to define at least one server, got %+v", report)
			}
		})
	}
}

func TestDemoWorkspaceMCPServerStartsAndLoadsTools(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(cwd, "..", ".."))
	workspace := filepath.Join(root, "examples", "demo-workspace")

	registry := engine.NewRegistry()
	manager, err := Start(context.Background(), registry, Options{WorkingDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if manager == nil {
		t.Fatal("expected MCP manager to be created for demo workspace")
	}
	defer func() {
		if err := manager.Close(); err != nil {
			t.Fatalf("close manager: %v", err)
		}
	}()

	if got := manager.ServerCount(); got != 1 {
		t.Fatalf("expected one demo MCP server, got %d", got)
	}
	tool, ok := registry.Get("mcp__demo__echo_text")
	if !ok {
		t.Fatal("expected demo MCP tool to be registered")
	}

	input, _ := json.Marshal(map[string]any{"value": "workspace smoke"})
	result, err := tool.Call(context.Background(), nil, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected demo MCP tool result to succeed, got %+v", result)
	}
	if !strings.Contains(result.Content, "demo: workspace smoke") {
		t.Fatalf("unexpected demo MCP tool output: %q", result.Content)
	}

	resource, err := manager.ReadResource(context.Background(), "demo", "memory://demo-guide")
	if err != nil {
		t.Fatal(err)
	}
	if len(resource.Contents) != 1 || !strings.Contains(resource.Contents[0].Text, "demo-guide") {
		t.Fatalf("unexpected demo MCP resource payload: %+v", resource)
	}

	prompt, err := manager.GetPrompt(context.Background(), "demo", "review_demo", map[string]string{"topic": "the example workspace"})
	if err != nil {
		t.Fatal(err)
	}
	if len(prompt.Messages) != 1 || !strings.Contains(prompt.Messages[0].Content, "plugin, MCP, and workflow") {
		t.Fatalf("unexpected demo MCP prompt payload: %+v", prompt)
	}
}

func TestStartMarksUnsupportedTransportAsFailed(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".mcp.json")
	configJSON := `{
  "mcpServers": {
    "remote": {
      "type": "tcp",
      "url": "tcp://example.com/mcp"
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

func TestManagerReloadReplacesToolsAndHealthIncludesFailedServers(t *testing.T) {
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	writeConfig := func(name string, includeBroken bool) {
		t.Helper()
		serverEntries := []string{}
		if includeBroken {
			serverEntries = append(serverEntries, `"broken": {"transport": "http"}`)
		}
		serverEntries = append(serverEntries, fmt.Sprintf(`%q: {
      "command": %q,
      "args": ["-test.run=TestMCPHelperProcess", "--", "mcp-echo-server"],
      "env": {"GO_WANT_MCP_HELPER": "1"}
    }`, name, exe))
		configJSON := "{\n  \"mcpServers\": {\n    " + strings.Join(serverEntries, ",\n    ") + "\n  }\n}"
		if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(configJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeConfig("alpha", false)

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

	if _, ok := registry.Get("mcp__alpha__echo_text"); !ok {
		t.Fatal("expected alpha tool to be registered")
	}

	writeConfig("beta", true)
	if err := manager.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, ok := registry.Get("mcp__alpha__echo_text"); ok {
		t.Fatal("expected alpha tool to be removed after reload")
	}
	if _, ok := registry.Get("mcp__beta__echo_text"); !ok {
		t.Fatal("expected beta tool to be registered after reload")
	}

	health, err := manager.Health(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(health) != 2 {
		t.Fatalf("expected health for both statuses, got %d", len(health))
	}
	byName := statusesByName(health)
	if byName["broken"].Status != ServerStatusFailed {
		t.Fatalf("expected broken server to remain failed, got %+v", byName["broken"])
	}
	if byName["beta"].Status != ServerStatusConnected || !byName["beta"].Healthy {
		t.Fatalf("expected beta server to be healthy, got %+v", byName["beta"])
	}
	if byName["beta"].LastCheckedAt == nil {
		t.Fatalf("expected beta server to record health check timestamp, got %+v", byName["beta"])
	}
}

func TestValidateOptionsReportsConfigIssues(t *testing.T) {
	dir := t.TempDir()
	configJSON := `{
  "mcpServers": {
    "": {
      "command": ""
    },
    "ws_bad": {
      "transport": "websocket",
      "url": "http://example.com/mcp"
    }
  }
}`
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	report := ValidateOptions(Options{WorkingDir: dir})
	if !report.Present {
		t.Fatalf("expected validation to find config file, got %+v", report)
	}
	if report.Valid {
		t.Fatalf("expected invalid config report, got %+v", report)
	}
	if report.ServerCount != 2 {
		t.Fatalf("expected 2 configured servers, got %+v", report)
	}
	if report.IssueCount < 2 {
		t.Fatalf("expected at least 2 validation issues, got %+v", report)
	}

	messages := make([]string, 0, len(report.Issues))
	for _, issue := range report.Issues {
		messages = append(messages, issue.Message)
	}
	joined := strings.Join(messages, " | ")
	if !strings.Contains(joined, "server name cannot be empty") {
		t.Fatalf("expected empty server name issue, got %+v", report)
	}
	if !strings.Contains(joined, "MCP websocket url must use ws or wss") {
		t.Fatalf("expected websocket URL validation issue, got %+v", report)
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

func newProtectedMCPWSServer(t *testing.T, header, value string) *httptest.Server {
	t.Helper()

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(header); got != value {
			http.Error(w, "missing required header", http.StatusForbidden)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}

		transport := &testWebSocketTransport{conn: conn, done: make(chan struct{})}
		server := newHelperServer()
		session, err := server.Connect(context.Background(), transport, nil)
		if err != nil {
			t.Errorf("connect websocket session: %v", err)
			_ = conn.Close()
			return
		}
		defer session.Close()

		<-transport.done
	}))
}

type testWebSocketTransport struct {
	conn *websocket.Conn
	done chan struct{}
	once sync.Once
}

func (t *testWebSocketTransport) Connect(ctx context.Context) (sdkmcp.Connection, error) {
	_ = ctx
	return &websocketConn{
		conn: t.conn,
		onClose: func() {
			t.once.Do(func() {
				close(t.done)
			})
		},
	}, nil
}

func wsTestURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	}
	return parsed.String()
}

func statusesByName(statuses []ServerStatus) map[string]ServerStatus {
	byName := make(map[string]ServerStatus, len(statuses))
	for _, status := range statuses {
		byName[status.Name] = status
	}
	return byName
}
