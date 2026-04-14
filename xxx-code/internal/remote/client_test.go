package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/config"
	"github.com/caowenhua/x-agent/xxx-code/internal/daemon"
	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	"github.com/caowenhua/x-agent/xxx-code/internal/tools"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type remoteTestProvider struct{}

func (p *remoteTestProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	_ = ctx
	text := ""
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if request.Messages[i].Role == engine.RoleUser {
			text = request.Messages[i].Text()
			break
		}
	}
	return engine.CompletionResponse{
		Message: engine.NewTextMessage(engine.RoleAssistant, "reply:"+text),
	}, nil
}

type remoteStreamingTestProvider struct{}

type remoteWorkflowProvider struct{}

type remoteAgentProvider struct{}

type remoteBlockingProvider struct{}

type remoteMCPToolProvider struct{}

func (p *remoteStreamingTestProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	_ = ctx
	prompt := latestRemoteUserText(request.Messages)
	return engine.CompletionResponse{
		Message: engine.NewTextMessage(engine.RoleAssistant, "reply:"+prompt),
	}, nil
}

func (p *remoteStreamingTestProvider) CreateMessageStream(ctx context.Context, request engine.CompletionRequest, handle func(engine.StreamEvent)) (engine.CompletionResponse, error) {
	_ = ctx
	prompt := latestRemoteUserText(request.Messages)
	for _, chunk := range []string{"reply:", prompt} {
		handle(engine.StreamEvent{
			Kind: engine.StreamEventTextDelta,
			Text: chunk,
		})
	}
	return engine.CompletionResponse{
		Message: engine.NewTextMessage(engine.RoleAssistant, "reply:"+prompt),
	}, nil
}

func (p *remoteWorkflowProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	_ = ctx

	if toolResult, ok := latestRemoteToolResult(request.Messages); ok {
		return engine.CompletionResponse{
			Message: engine.NewTextMessage(engine.RoleAssistant, "tool-result:"+toolResult),
		}, nil
	}

	if prompt := latestRemoteUserText(request.Messages); prompt == "fanout work" {
		input, _ := json.Marshal(map[string]any{
			"wait":         true,
			"max_parallel": 1,
			"tasks": []map[string]any{
				{"name": "one", "prompt": "task one"},
				{"name": "two", "prompt": "task two"},
			},
		})
		return engine.CompletionResponse{
			Message: engine.Message{
				Role: engine.RoleAssistant,
				Content: []engine.Block{
					{Type: engine.BlockText, Text: "fanout"},
					{Type: engine.BlockToolUse, ID: "toolu_fanout", Name: "agent_fanout", Input: input},
				},
			},
		}, nil
	}

	return engine.CompletionResponse{
		Message: engine.NewTextMessage(engine.RoleAssistant, "reply:"+latestRemoteUserText(request.Messages)),
	}, nil
}

func (p *remoteAgentProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	if toolResult, ok := latestRemoteToolResult(request.Messages); ok {
		return engine.CompletionResponse{
			Message: engine.NewTextMessage(engine.RoleAssistant, "tool-result:"+toolResult),
		}, nil
	}

	switch prompt := latestRemoteUserText(request.Messages); prompt {
	case "delegate work":
		input, _ := json.Marshal(map[string]any{
			"name":       "worker",
			"prompt":     "child task",
			"background": false,
		})
		return engine.CompletionResponse{
			Message: engine.Message{
				Role: engine.RoleAssistant,
				Content: []engine.Block{
					{Type: engine.BlockText, Text: "delegating"},
					{Type: engine.BlockToolUse, ID: "toolu_delegate", Name: "agent_spawn", Input: input},
				},
			},
		}, nil
	case "background work":
		input, _ := json.Marshal(map[string]any{
			"name":       "worker",
			"prompt":     "block child",
			"background": true,
		})
		return engine.CompletionResponse{
			Message: engine.Message{
				Role: engine.RoleAssistant,
				Content: []engine.Block{
					{Type: engine.BlockText, Text: "delegating"},
					{Type: engine.BlockToolUse, ID: "toolu_delegate_bg", Name: "agent_spawn", Input: input},
				},
			},
		}, nil
	case "block child":
		<-ctx.Done()
		return engine.CompletionResponse{}, ctx.Err()
	default:
		return engine.CompletionResponse{
			Message: engine.NewTextMessage(engine.RoleAssistant, "reply:"+prompt),
		}, nil
	}
}

func (p *remoteBlockingProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	_ = request
	<-ctx.Done()
	return engine.CompletionResponse{}, ctx.Err()
}

func (p *remoteMCPToolProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	_ = ctx
	if toolResult, ok := latestRemoteToolResult(request.Messages); ok {
		return engine.CompletionResponse{
			Message: engine.NewTextMessage(engine.RoleAssistant, "tool-result:"+toolResult),
		}, nil
	}
	if latestRemoteUserText(request.Messages) == "use stdio mcp" {
		input, _ := json.Marshal(map[string]any{"value": "hello from stdio"})
		return engine.CompletionResponse{
			Message: engine.Message{
				Role: engine.RoleAssistant,
				Content: []engine.Block{
					{Type: engine.BlockText, Text: "calling mcp"},
					{Type: engine.BlockToolUse, ID: "toolu_mcp_stdio", Name: "mcp__tester__echo_text", Input: input},
				},
			},
		}, nil
	}
	return engine.CompletionResponse{
		Message: engine.NewTextMessage(engine.RoleAssistant, "reply:"+latestRemoteUserText(request.Messages)),
	}, nil
}

func TestClientSessionLifecycle(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	session, err := client.EnsureSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if session.ID == "" {
		t.Fatal("expected a generated session ID")
	}

	result, updated, err := client.RunTurn(context.Background(), session.ID, "hello remote", 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "reply:hello remote" {
		t.Fatalf("unexpected final text: %+v", result)
	}
	if updated.MessageCount != 2 {
		t.Fatalf("expected 2 messages after one turn, got %d", updated.MessageCount)
	}

	messages, err := client.ListMessages(context.Background(), session.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}

	saved, err := client.SaveSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID != session.ID {
		t.Fatalf("unexpected saved session summary: %+v", saved)
	}

	sessions, err := client.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].ID != session.ID {
		t.Fatalf("unexpected listed session: %+v", sessions[0])
	}
}

func TestClientEnsureNamedSession(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	session, err := client.EnsureSession(context.Background(), "named-session")
	if err != nil {
		t.Fatal(err)
	}
	if session.ID != "named-session" {
		t.Fatalf("unexpected session ID: %s", session.ID)
	}

	again, err := client.EnsureSession(context.Background(), "named-session")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != session.ID {
		t.Fatalf("expected to reopen the same session, got %+v", again)
	}
}

func TestClientCanInspectPolicyHooksAndMCPStatus(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	session, err := client.EnsureSession(context.Background(), "inspect-session")
	if err != nil {
		t.Fatal(err)
	}

	policy, err := client.GetPolicy(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.BashEnabled {
		t.Fatalf("expected bash to be enabled in policy: %+v", policy)
	}
	if len(policy.ReadRoots) != 1 || policy.ReadRoots[0] == "" {
		t.Fatalf("unexpected read roots: %+v", policy)
	}

	hooks, err := client.GetHooks(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hooks.BeforeTool != "echo before" || hooks.Timeout != time.Second.String() {
		t.Fatalf("unexpected hook config: %+v", hooks)
	}
	if hooks.EventFile == "" {
		t.Fatalf("expected hook event file to be exposed, got %+v", hooks)
	}

	mcpSummary, err := client.GetMCP(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mcpSummary.ServerCount != 0 || mcpSummary.ToolCount != 0 || len(mcpSummary.Statuses) != 0 {
		t.Fatalf("expected empty MCP summary, got %+v", mcpSummary)
	}

	_, err = client.ListMCPResources(context.Background(), session.ID, "")
	if err == nil {
		t.Fatal("expected MCP resources call to fail without MCP config")
	}
	var remoteErr *Error
	if !errors.As(err, &remoteErr) || remoteErr.StatusCode != 400 {
		t.Fatalf("expected 400 remote error, got %v", err)
	}
	if remoteErr.Code != "mcp_not_configured" {
		t.Fatalf("expected mcp_not_configured code, got %+v", remoteErr)
	}
}

func TestClientCanValidateReloadAndHealthCheckMCP(t *testing.T) {
	cfg := newTestConfig(t)
	mcpServer := newRemoteMCPHTTPServer(t)
	defer mcpServer.Close()

	server := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &remoteTestProvider{}
	})
	httpServer := httptest.NewServer(server.Handler())
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	client := NewClient(httpServer.URL, "", httpServer.Client())
	session, err := client.EnsureSession(context.Background(), "remote-mcp")
	if err != nil {
		t.Fatal(err)
	}

	report, err := client.ValidateMCP(context.Background(), session.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if report.Present {
		t.Fatalf("expected missing MCP config to be reported, got %+v", report)
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

	summary, err := client.ReloadMCP(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.ServerCount != 1 || summary.ToolCount != 1 {
		t.Fatalf("expected one connected MCP server after reload, got %+v", summary)
	}

	health, err := client.GetMCPHealth(context.Background(), session.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(health) != 1 {
		t.Fatalf("expected one MCP health status, got %+v", health)
	}
	if !health[0].Healthy || health[0].LastCheckedAt == nil {
		t.Fatalf("expected healthy MCP status with timestamp, got %+v", health[0])
	}
}

func TestClientCanUseStdioMCPToolsAcrossRemoteTurns(t *testing.T) {
	cfg := newTestConfig(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	configJSON := fmt.Sprintf(`{
  "mcpServers": {
    "tester": {
      "command": %q,
      "args": ["-test.run=TestRemoteMCPHelperProcess", "--", "mcp-echo-server"],
      "env": {"GO_WANT_REMOTE_MCP_HELPER": "1"}
    }
  }
}`, exe)
	if err := os.WriteFile(filepath.Join(cfg.WorkingDir, ".mcp.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	server := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &remoteMCPToolProvider{}
	})
	httpServer := httptest.NewServer(server.Handler())
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	client := NewClient(httpServer.URL, "", httpServer.Client())
	session, err := client.EnsureSession(context.Background(), "remote-mcp-stdio")
	if err != nil {
		t.Fatal(err)
	}

	summary, err := client.GetMCP(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.ServerCount != 1 || summary.ToolCount != 1 {
		t.Fatalf("expected one connected stdio MCP server, got %+v", summary)
	}

	result, _, err := client.RunTurn(context.Background(), session.ID, "use stdio mcp", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.FinalText, "hello from stdio") {
		t.Fatalf("expected stdio MCP result to flow back into remote turn, got %+v", result)
	}
}

func TestClientCanInspectAndReloadPlugins(t *testing.T) {
	cfg := newTestConfig(t)
	writeRemotePlugin(t, cfg.WorkingDir, "echoer", "#!/bin/sh\nprintf 'echo plugin'\n")

	server := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &remoteTestProvider{}
	})
	httpServer := httptest.NewServer(server.Handler())
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	client := NewClient(httpServer.URL, "", httpServer.Client())
	session, err := client.EnsureSession(context.Background(), "remote-plugins")
	if err != nil {
		t.Fatal(err)
	}

	plugins, err := client.GetPlugins(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plugins.PluginCount != 1 || plugins.ToolCount != 1 {
		t.Fatalf("expected one loaded plugin, got %+v", plugins)
	}
	if len(plugins.Statuses) != 1 || plugins.Statuses[0].Name != "echoer" {
		t.Fatalf("unexpected plugin statuses: %+v", plugins)
	}

	if err := os.RemoveAll(filepath.Join(cfg.WorkingDir, ".xxx-code", "plugins", "echoer")); err != nil {
		t.Fatal(err)
	}
	writeRemotePlugin(t, cfg.WorkingDir, "writer", "#!/bin/sh\nprintf 'writer plugin'\n")

	reloaded, err := client.ReloadPlugins(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PluginCount != 1 || len(reloaded.Statuses) != 1 || reloaded.Statuses[0].Name != "writer" {
		t.Fatalf("expected reloaded plugin summary, got %+v", reloaded)
	}
}

func TestClientCanValidateInstallAndRemovePlugins(t *testing.T) {
	cfg := newTestConfig(t)
	sourceDir := writeRemotePluginSource(t, filepath.Join(cfg.WorkingDir, "plugin-sources"), "echoer", "#!/bin/sh\ncat\n")

	server := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &remoteTestProvider{}
	})
	httpServer := httptest.NewServer(server.Handler())
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	client := NewClient(httpServer.URL, "", httpServer.Client())
	session, err := client.EnsureSession(context.Background(), "remote-plugin-lifecycle")
	if err != nil {
		t.Fatal(err)
	}

	validation, err := client.ValidatePlugin(context.Background(), session.ID, sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	if !validation.Valid || validation.PluginName != "echoer" || validation.ToolCount != 1 {
		t.Fatalf("expected valid plugin report, got %+v", validation)
	}

	installed, err := client.InstallPlugin(context.Background(), session.ID, sourceDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if installed.PluginCount != 1 || len(installed.Statuses) != 1 || installed.Statuses[0].Name != "echoer" {
		t.Fatalf("expected installed plugin summary, got %+v", installed)
	}
	if _, err := os.Stat(filepath.Join(cfg.WorkingDir, ".xxx-code", "plugins", "echoer", "plugin.json")); err != nil {
		t.Fatalf("expected installed plugin to exist in plugin dir: %v", err)
	}

	removed, err := client.RemovePlugin(context.Background(), session.ID, "echoer")
	if err != nil {
		t.Fatal(err)
	}
	if removed.PluginCount != 0 || removed.ToolCount != 0 {
		t.Fatalf("expected empty plugin summary after removal, got %+v", removed)
	}
	if _, err := os.Stat(filepath.Join(cfg.WorkingDir, ".xxx-code", "plugins", "echoer")); !os.IsNotExist(err) {
		t.Fatalf("expected installed plugin directory to be removed, got err=%v", err)
	}
}

func TestClientCanListSessionAudit(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	session, err := client.EnsureSession(context.Background(), "audit-session")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.RunTurn(context.Background(), session.ID, "hello audit", 0); err != nil {
		t.Fatal(err)
	}

	events, err := client.ListSessionAudit(context.Background(), session.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("expected non-empty session audit stream")
	}
	foundTurnRequest := false
	for _, event := range events {
		if event.Action == "request" && event.Mode == "turns" {
			foundTurnRequest = true
			break
		}
	}
	if !foundTurnRequest {
		t.Fatalf("expected a turn request audit event, got %+v", events)
	}

	globalEvents, err := client.ListAudit(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(globalEvents) == 0 {
		t.Fatal("expected non-empty global audit stream")
	}
}

func TestClientStreamTurn(t *testing.T) {
	client, cleanup := newStreamingTestClient(t)
	defer cleanup()

	session, err := client.EnsureSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}

	var events []TurnStreamEvent
	result, updated, err := client.StreamTurn(context.Background(), session.ID, "stream me", 0, func(event TurnStreamEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "reply:stream me" {
		t.Fatalf("unexpected final text: %+v", result)
	}
	if updated.ID != session.ID {
		t.Fatalf("unexpected updated session: %+v", updated)
	}

	streamed := ""
	for _, event := range events {
		if event.Type == string(engine.EventAssistantTextDelta) {
			streamed += event.Text
		}
	}
	if streamed != "reply:stream me" {
		t.Fatalf("unexpected streamed text: %q", streamed)
	}
}

func TestClientCanUseRemoteToken(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.DaemonToken = "shared-secret"
	server := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &remoteTestProvider{}
	})
	httpServer := httptest.NewServer(server.Handler())
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	unauthorized := NewClient(httpServer.URL, "", httpServer.Client())
	_, err := unauthorized.ListSessions(context.Background())
	if err == nil {
		t.Fatal("expected unauthorized client to fail")
	}
	var remoteErr *Error
	if !errors.As(err, &remoteErr) || remoteErr.StatusCode != 401 {
		t.Fatalf("expected 401 from unauthorized client, got %v", err)
	}
	if remoteErr.Code != "unauthorized" {
		t.Fatalf("expected unauthorized code, got %+v", remoteErr)
	}

	authorized := NewClient(httpServer.URL, "shared-secret", httpServer.Client())
	session, err := authorized.EnsureSession(context.Background(), "protected")
	if err != nil {
		t.Fatal(err)
	}
	if session.ID != "protected" {
		t.Fatalf("unexpected session: %+v", session)
	}
}

func TestClientCanReloadRemoteTokenFile(t *testing.T) {
	cfg := newTestConfig(t)
	tokenFile := filepath.Join(t.TempDir(), "remote-token.txt")
	if err := os.WriteFile(tokenFile, []byte("token-a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.DaemonTokenFile = tokenFile
	server := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &remoteTestProvider{}
	})
	httpServer := httptest.NewServer(server.Handler())
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	client := NewClientWithTokenFile(httpServer.URL, "", tokenFile, httpServer.Client())
	session, err := client.EnsureSession(context.Background(), "rotating-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.RunTurn(context.Background(), session.ID, "first", 0); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(tokenFile, []byte("token-b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.RunTurn(context.Background(), session.ID, "second", 0); err != nil {
		t.Fatal(err)
	}
}

func TestClientCanQueryWorkflowTasksAndResumeSelectedRemoteTask(t *testing.T) {
	cfg := newTestConfig(t)
	server := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &remoteWorkflowProvider{}
	})
	httpServer := httptest.NewServer(server.Handler())
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	client := NewClient(httpServer.URL, "", httpServer.Client())
	session, err := client.EnsureSession(context.Background(), "workflow-remote")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.RunTurn(context.Background(), session.ID, "fanout work", 0); err != nil {
		t.Fatal(err)
	}

	workflows, err := client.ListWorkflows(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(workflows) != 1 {
		t.Fatalf("expected one workflow, got %d", len(workflows))
	}

	tasks, err := client.ListWorkflowTasks(context.Background(), session.ID, workflows[0].ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected two workflow tasks, got %d", len(tasks))
	}

	resumed, err := client.ResumeWorkflow(context.Background(), session.ID, workflows[0].ID, WorkflowResumeOptions{
		TaskNames: []string{"one"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Workflow.Status != tools.WorkflowCompleted {
		t.Fatalf("expected completed workflow after selective resume, got %+v", resumed.Workflow)
	}
	byName := map[string]tools.FanoutTaskResultAlias{}
	for _, task := range resumed.Tasks {
		byName[task.Name] = task
	}
	if byName["one"].Attempts != 2 {
		t.Fatalf("expected selected task to rerun remotely, got %+v", byName["one"])
	}
	if byName["two"].Attempts != 1 {
		t.Fatalf("expected unselected task to keep prior attempts, got %+v", byName["two"])
	}
}

func TestClientCanInspectWorkflowDetails(t *testing.T) {
	cfg := newTestConfig(t)
	server := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &remoteWorkflowProvider{}
	})
	httpServer := httptest.NewServer(server.Handler())
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	client := NewClient(httpServer.URL, "", httpServer.Client())
	session, err := client.EnsureSession(context.Background(), "workflow-detail-remote")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.RunTurn(context.Background(), session.ID, "fanout work", 0); err != nil {
		t.Fatal(err)
	}

	workflows, err := client.ListWorkflows(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(workflows) != 1 {
		t.Fatalf("expected one workflow, got %d", len(workflows))
	}

	snapshot, err := client.GetWorkflow(context.Background(), session.ID, workflows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ID != workflows[0].ID || snapshot.Status != tools.WorkflowCompleted {
		t.Fatalf("unexpected workflow snapshot: %+v", snapshot)
	}
}

func TestClientCanListWaitSendAndCancelAgents(t *testing.T) {
	cfg := newTestConfig(t)
	server := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &remoteAgentProvider{}
	})
	httpServer := httptest.NewServer(server.Handler())
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	client := NewClient(httpServer.URL, "", httpServer.Client())
	session, err := client.EnsureSession(context.Background(), "agent-remote")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.RunTurn(context.Background(), session.ID, "delegate work", 0); err != nil {
		t.Fatal(err)
	}

	agents, err := client.ListAgents(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected one delegated agent, got %d", len(agents))
	}

	waited, err := client.WaitAgent(context.Background(), session.ID, agents[0].ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if waited.Status != engine.AgentIdle {
		t.Fatalf("expected idle delegated agent, got %+v", waited)
	}

	sent, err := client.SendAgent(context.Background(), session.ID, agents[0].ID, "follow-up", false)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Status != engine.AgentIdle || sent.Result != "reply:follow-up" {
		t.Fatalf("unexpected agent send result: %+v", sent)
	}

	backgroundSession, err := client.EnsureSession(context.Background(), "agent-remote-background")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.RunTurn(context.Background(), backgroundSession.ID, "background work", 0); err != nil {
		t.Fatal(err)
	}
	agents, err = client.ListAgents(context.Background(), backgroundSession.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) < 1 {
		t.Fatalf("expected background agent to be tracked, got %+v", agents)
	}

	var backgroundID string
	for _, agent := range agents {
		if agent.Prompt == "block child" {
			backgroundID = agent.ID
			break
		}
	}
	if backgroundID == "" {
		t.Fatalf("expected to find background agent in %+v", agents)
	}

	cancelled, err := client.CancelAgent(context.Background(), backgroundSession.ID, backgroundID, true)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != engine.AgentCancelled {
		t.Fatalf("expected cancelled background agent, got %+v", cancelled)
	}
}

func TestClientStreamTurnReturnsStructuredTimeoutError(t *testing.T) {
	cfg := newTestConfig(t)
	server := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &remoteBlockingProvider{}
	})
	httpServer := httptest.NewServer(server.Handler())
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	client := NewClient(httpServer.URL, "", httpServer.Client())
	session, err := client.EnsureSession(context.Background(), "stream-timeout")
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = client.StreamTurn(context.Background(), session.ID, "hang", 1, nil)
	if err == nil {
		t.Fatal("expected streaming turn to time out")
	}
	var remoteErr *Error
	if !errors.As(err, &remoteErr) {
		t.Fatalf("expected structured remote error, got %v", err)
	}
	if remoteErr.Code != "timeout" || !remoteErr.Retryable {
		t.Fatalf("expected retryable timeout code, got %+v", remoteErr)
	}
}

func TestClientParsesStructuredConflictAndNotFoundErrors(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	session, err := client.CreateSession(context.Background(), "conflict-session", false)
	if err != nil {
		t.Fatal(err)
	}
	if session.ID != "conflict-session" {
		t.Fatalf("unexpected session: %+v", session)
	}

	_, err = client.CreateSession(context.Background(), "conflict-session", false)
	if err == nil {
		t.Fatal("expected duplicate session creation to fail")
	}
	var remoteErr *Error
	if !errors.As(err, &remoteErr) || remoteErr.StatusCode != 409 {
		t.Fatalf("expected 409 conflict error, got %v", err)
	}
	if remoteErr.Code != "session_exists" {
		t.Fatalf("expected session_exists code, got %+v", remoteErr)
	}

	_, err = client.GetSession(context.Background(), "missing-session")
	if err == nil {
		t.Fatal("expected missing session lookup to fail")
	}
	if !errors.As(err, &remoteErr) || remoteErr.StatusCode != 404 {
		t.Fatalf("expected 404 not found error, got %v", err)
	}
	if remoteErr.Code != "session_not_found" {
		t.Fatalf("expected session_not_found code, got %+v", remoteErr)
	}
}

func newTestClient(t *testing.T) (*Client, func()) {
	t.Helper()
	cfg := newTestConfig(t)
	server := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &remoteTestProvider{}
	})
	httpServer := httptest.NewServer(server.Handler())
	client := NewClient(httpServer.URL, "", httpServer.Client())
	return client, func() {
		httpServer.Close()
		_ = server.Close()
	}
}

func newStreamingTestClient(t *testing.T) (*Client, func()) {
	t.Helper()
	cfg := newTestConfig(t)
	server := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &remoteStreamingTestProvider{}
	})
	httpServer := httptest.NewServer(server.Handler())
	client := NewClient(httpServer.URL, "", httpServer.Client())
	return client, func() {
		httpServer.Close()
		_ = server.Close()
	}
}

func newRemoteMCPHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()

	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		server := sdkmcp.NewServer(&sdkmcp.Implementation{
			Name:    "remote-test-mcp",
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

func TestRemoteMCPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_REMOTE_MCP_HELPER") != "1" {
		return
	}
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "mcp-echo-server" {
		os.Exit(2)
	}

	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "remote-stdio-helper",
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

	if err := server.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func newTestConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	return config.Config{
		Model:             "test-model",
		SystemPrompt:      "test",
		MaxTurns:          4,
		MaxTokens:         4096,
		MaxParallelAgents: 2,
		ContextBudget:     4000,
		CompactKeep:       6,
		WorkingDir:        dir,
		DaemonDir:         filepath.Join(dir, ".xxx-code", "daemon"),
		ToolTimeout:       2 * time.Second,
		HookTimeout:       time.Second,
		HookBeforeTool:    "echo before",
		HookAfterTool:     "echo after",
		HookAfterTurn:     "echo turn",
		HookAgentEvent:    "echo agent",
		HookEventFile:     filepath.Join(dir, ".xxx-code", "hooks.jsonl"),
		ReadRoots:         []string{dir},
		WriteRoots:        []string{dir},
		BashEnabled:       true,
	}
}

func writeRemotePlugin(t *testing.T, workingDir, pluginName, script string) {
	t.Helper()
	writeRemotePluginSource(t, filepath.Join(workingDir, ".xxx-code", "plugins"), pluginName, script)
}

func writeRemotePluginSource(t *testing.T, rootDir, pluginName, script string) string {
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

func latestRemoteUserText(messages []engine.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == engine.RoleUser {
			return strings.TrimSpace(messages[i].Text())
		}
	}
	return ""
}

func latestRemoteToolResult(messages []engine.Message) (string, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		for _, block := range messages[i].Content {
			if block.Type == engine.BlockToolResult {
				return strings.TrimSpace(block.Result), true
			}
		}
	}
	return "", false
}
