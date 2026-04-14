package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caowenhua/x-agent/xxx-code/internal/config"
	"github.com/caowenhua/x-agent/xxx-code/internal/daemon"
	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	"github.com/caowenhua/x-agent/xxx-code/internal/remote"
	"github.com/caowenhua/x-agent/xxx-code/internal/tools"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type toolStoryProvider struct{}

type workflowFailureStoryProvider struct{}

type workflowSuccessStoryProvider struct{}

func (p *toolStoryProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	_ = ctx

	if toolResult, ok := latestToolResult(request.Messages); ok {
		return engine.CompletionResponse{
			Message: engine.NewTextMessage(engine.RoleAssistant, "story-result:"+toolResult),
		}, nil
	}

	switch latestUserText(request.Messages) {
	case "use installed plugin":
		input, _ := json.Marshal(map[string]any{"value": "story via plugin"})
		return engine.CompletionResponse{
			Message: engine.Message{
				Role: engine.RoleAssistant,
				Content: []engine.Block{
					{Type: engine.BlockText, Text: "using plugin"},
					{Type: engine.BlockToolUse, ID: "toolu_plugin_story", Name: "plugin__echoer__echo", Input: input},
				},
			},
		}, nil
	case "use reloaded mcp":
		input, _ := json.Marshal(map[string]any{"value": "story via mcp"})
		return engine.CompletionResponse{
			Message: engine.Message{
				Role: engine.RoleAssistant,
				Content: []engine.Block{
					{Type: engine.BlockText, Text: "using mcp"},
					{Type: engine.BlockToolUse, ID: "toolu_mcp_story", Name: "mcp__tester__echo_text", Input: input},
				},
			},
		}, nil
	default:
		return engine.CompletionResponse{
			Message: engine.NewTextMessage(engine.RoleAssistant, "reply:"+latestUserText(request.Messages)),
		}, nil
	}
}

func (p *workflowFailureStoryProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	return workflowStoryResponse(ctx, request, true)
}

func (p *workflowSuccessStoryProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	return workflowStoryResponse(ctx, request, false)
}

func workflowStoryResponse(ctx context.Context, request engine.CompletionRequest, failChild bool) (engine.CompletionResponse, error) {
	_ = ctx

	if toolResult, ok := latestToolResult(request.Messages); ok {
		prefix := "workflow completed: "
		if strings.Contains(strings.ToLower(toolResult), "failed") || strings.Contains(strings.ToLower(toolResult), "skipped") || strings.Contains(strings.ToLower(toolResult), "error") {
			prefix = "workflow failed: "
		}
		return engine.CompletionResponse{
			Message: engine.NewTextMessage(engine.RoleAssistant, prefix+toolResult),
		}, nil
	}

	switch latestUserText(request.Messages) {
	case "recover my workflow":
		input, _ := json.Marshal(map[string]any{
			"wait":         true,
			"max_parallel": 1,
			"tasks": []map[string]any{
				{"name": "collect", "prompt": "collect failing context"},
				{"name": "summarize", "prompt": "summarize recovered context", "depends_on": []string{"collect"}},
			},
		})
		return engine.CompletionResponse{
			Message: engine.Message{
				Role: engine.RoleAssistant,
				Content: []engine.Block{
					{Type: engine.BlockText, Text: "starting workflow"},
					{Type: engine.BlockToolUse, ID: "toolu_story_fanout", Name: "agent_fanout", Input: input},
				},
			},
		}, nil
	case "collect failing context":
		if failChild {
			return engine.CompletionResponse{}, fmt.Errorf("forced workflow story failure")
		}
		return engine.CompletionResponse{
			Message: engine.NewTextMessage(engine.RoleAssistant, "reply:collected context"),
		}, nil
	default:
		return engine.CompletionResponse{
			Message: engine.NewTextMessage(engine.RoleAssistant, "reply:"+latestUserText(request.Messages)),
		}, nil
	}
}

func TestUserStoryOperatorCanInstallPluginAndUseItInATurn(t *testing.T) {
	// Given a running daemon and a plugin source the operator wants to install.
	server, httpServer, cfg := newDaemonHarness(t, &toolStoryProvider{}, nil)
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	client := remote.NewClient(httpServer.URL, cfg.DaemonToken, httpServer.Client())
	session, err := client.EnsureSession(context.Background(), "story-plugin-session")
	if err != nil {
		t.Fatal(err)
	}

	sourceDir := writeIntegrationPluginSource(t, filepath.Join(cfg.WorkingDir, "plugin-sources"), "echoer", "#!/bin/sh\ncat\n")

	// When the operator validates and installs the plugin.
	validation, err := client.ValidatePlugin(context.Background(), session.ID, sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	if !validation.Valid || validation.PluginName != "echoer" {
		t.Fatalf("expected valid echoer plugin report, got %+v", validation)
	}

	installed, err := client.InstallPlugin(context.Background(), session.ID, sourceDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if installed.PluginCount != 1 || installed.ToolCount != 1 {
		t.Fatalf("expected installed plugin summary, got %+v", installed)
	}

	// Then a normal user turn can call the newly installed plugin tool.
	result, updated, err := client.RunTurn(context.Background(), session.ID, "use installed plugin", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.FinalText, "story via plugin") {
		t.Fatalf("expected plugin tool result in final answer, got %s", result.FinalText)
	}
	if updated.MessageCount < 2 {
		t.Fatalf("expected updated session transcript, got %+v", updated)
	}
}

func TestUserStoryOperatorCanReloadMCPAndUseItInATurn(t *testing.T) {
	// Given a daemon with an MCP config added after startup.
	server, httpServer, cfg := newDaemonHarness(t, &toolStoryProvider{}, nil)

	mcpServer := newIntegrationMCPHTTPServer(t)
	defer func() {
		httpServer.Close()
		_ = server.Close()
		mcpServer.Close()
	}()

	client := remote.NewClient(httpServer.URL, cfg.DaemonToken, httpServer.Client())
	session, err := client.EnsureSession(context.Background(), "story-mcp-session")
	if err != nil {
		t.Fatal(err)
	}

	configJSON := fmt.Sprintf(`{
  "mcpServers": {
    "tester": {
      "transport": "http",
      "url": %q
    }
  }
}`, mcpServer.URL)
	if err := os.WriteFile(filepath.Join(cfg.WorkingDir, ".mcp.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	// When the operator reloads MCP on the running daemon.
	summary, err := client.ReloadMCP(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.ServerCount != 1 || summary.ToolCount != 1 {
		t.Fatalf("expected reloaded MCP summary, got %+v", summary)
	}

	// Then a later user turn can call the freshly loaded MCP tool.
	result, _, err := client.RunTurn(context.Background(), session.ID, "use reloaded mcp", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.FinalText, "story via mcp") {
		t.Fatalf("expected MCP tool result in final answer, got %s", result.FinalText)
	}
}

func TestUserStoryUserCanResumeFailedWorkflowAfterDaemonRestart(t *testing.T) {
	// Given a workflow that failed mid-run and was persisted by the daemon.
	cfg := newTestConfig(t)
	cfg.DaemonToken = "workflow-story-secret"

	serverA := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &workflowFailureStoryProvider{}
	})
	httpA := httptest.NewServer(serverA.Handler())

	clientA := remote.NewClient(httpA.URL, cfg.DaemonToken, httpA.Client())
	session, err := clientA.EnsureSession(context.Background(), "story-workflow-session")
	if err != nil {
		t.Fatal(err)
	}

	result, updated, err := clientA.RunTurn(context.Background(), session.ID, "recover my workflow", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(result.FinalText), "workflow failed") {
		t.Fatalf("expected failure summary in initial workflow result, got %s", result.FinalText)
	}
	if updated.WorkflowCount != 1 {
		t.Fatalf("expected persisted workflow count, got %+v", updated)
	}

	workflows, err := clientA.ListWorkflows(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(workflows) != 1 {
		t.Fatalf("expected one failed workflow, got %+v", workflows)
	}
	if workflows[0].Status != tools.WorkflowCompleted || workflows[0].FailedTasks != 1 || workflows[0].SkippedTasks != 1 {
		t.Fatalf("expected completed workflow with one failed and one skipped task, got %+v", workflows[0])
	}

	workflowID := workflows[0].ID

	httpA.Close()
	if err := serverA.Close(); err != nil {
		t.Fatal(err)
	}

	// When the daemon comes back with a healthy provider and the user resumes only failed work.
	serverB := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &workflowSuccessStoryProvider{}
	})
	httpB := httptest.NewServer(serverB.Handler())
	defer func() {
		httpB.Close()
		_ = serverB.Close()
	}()

	clientB := remote.NewClient(httpB.URL, cfg.DaemonToken, httpB.Client())
	resumed, err := clientB.ResumeWorkflow(context.Background(), session.ID, workflowID, remote.WorkflowResumeOptions{
		OnlyFailed: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Then the recovered workflow completes and only the failed branch is retried.
	if resumed.Workflow.Status != tools.WorkflowCompleted {
		t.Fatalf("expected completed workflow after resume, got %+v", resumed.Workflow)
	}
	byName := map[string]tools.FanoutTaskResultAlias{}
	for _, task := range resumed.Tasks {
		byName[task.Name] = task
	}
	if byName["collect"].Attempts != 2 {
		t.Fatalf("expected failed task to rerun once, got %+v", byName["collect"])
	}
	if byName["summarize"].Attempts != 1 {
		t.Fatalf("expected dependent task to run once after recovery, got %+v", byName["summarize"])
	}
	if byName["collect"].Status != string(engine.AgentIdle) || byName["summarize"].Status != string(engine.AgentIdle) {
		t.Fatalf("expected recovered workflow tasks to finish idle, got %+v", byName)
	}
}

func writeIntegrationPluginSource(t *testing.T, rootDir, pluginName, script string) string {
	t.Helper()
	pluginDir := filepath.Join(rootDir, pluginName)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "tool.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "name": "` + pluginName + `",
  "tools": [{
    "name": "echo",
    "description": "Echo plugin",
    "input_schema": {"type": "object"},
    "command": "./tool.sh"
  }]
}`
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return pluginDir
}

func newIntegrationMCPHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()

	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		server := sdkmcp.NewServer(&sdkmcp.Implementation{
			Name:    "integration-test-mcp",
			Version: "1.0.0",
		}, nil)
		sdkmcp.AddTool(server, &sdkmcp.Tool{
			Name:        "echo_text",
			Description: "Echo text back to the caller",
		}, func(ctx context.Context, req *sdkmcp.CallToolRequest, input struct {
			Value string `json:"value" jsonschema:"value to echo back"`
		}) (*sdkmcp.CallToolResult, map[string]string, error) {
			_ = ctx
			_ = req
			return nil, map[string]string{"echo": input.Value}, nil
		})
		return server
	}, nil)

	return httptest.NewServer(handler)
}
