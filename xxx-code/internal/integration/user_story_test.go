package integration

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

type agentStoryProvider struct{}

type selectiveWorkflowStoryProvider struct{}

type policyHookStoryProvider struct{}

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

func (p *agentStoryProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	if toolResult, ok := latestToolResult(request.Messages); ok {
		return engine.CompletionResponse{
			Message: engine.NewTextMessage(engine.RoleAssistant, "agent-story:"+toolResult),
		}, nil
	}

	switch latestUserText(request.Messages) {
	case "delegate analyst":
		input, _ := json.Marshal(map[string]any{
			"name":       "analyst",
			"prompt":     "child task",
			"background": false,
		})
		return engine.CompletionResponse{
			Message: engine.Message{
				Role: engine.RoleAssistant,
				Content: []engine.Block{
					{Type: engine.BlockText, Text: "delegating analyst"},
					{Type: engine.BlockToolUse, ID: "toolu_story_delegate", Name: "agent_spawn", Input: input},
				},
			},
		}, nil
	case "start background watcher":
		input, _ := json.Marshal(map[string]any{
			"name":       "watcher",
			"prompt":     "block child",
			"background": true,
		})
		return engine.CompletionResponse{
			Message: engine.Message{
				Role: engine.RoleAssistant,
				Content: []engine.Block{
					{Type: engine.BlockText, Text: "starting watcher"},
					{Type: engine.BlockToolUse, ID: "toolu_story_background", Name: "agent_spawn", Input: input},
				},
			},
		}, nil
	case "block child":
		<-ctx.Done()
		return engine.CompletionResponse{}, ctx.Err()
	default:
		return engine.CompletionResponse{
			Message: engine.NewTextMessage(engine.RoleAssistant, "reply:"+latestUserText(request.Messages)),
		}, nil
	}
}

func (p *selectiveWorkflowStoryProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	_ = ctx

	if toolResult, ok := latestToolResult(request.Messages); ok {
		return engine.CompletionResponse{
			Message: engine.NewTextMessage(engine.RoleAssistant, "workflow-story:"+toolResult),
		}, nil
	}

	if latestUserText(request.Messages) == "prepare daily digest" {
		input, _ := json.Marshal(map[string]any{
			"wait":         true,
			"max_parallel": 1,
			"tasks": []map[string]any{
				{"name": "research", "prompt": "collect report facts"},
				{"name": "draft", "prompt": "draft summary block"},
			},
		})
		return engine.CompletionResponse{
			Message: engine.Message{
				Role: engine.RoleAssistant,
				Content: []engine.Block{
					{Type: engine.BlockText, Text: "preparing digest"},
					{Type: engine.BlockToolUse, ID: "toolu_story_digest", Name: "agent_fanout", Input: input},
				},
			},
		}, nil
	}

	return engine.CompletionResponse{
		Message: engine.NewTextMessage(engine.RoleAssistant, "reply:"+latestUserText(request.Messages)),
	}, nil
}

func (p *policyHookStoryProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	_ = ctx

	if toolResult, ok := latestToolResult(request.Messages); ok {
		return engine.CompletionResponse{
			Message: engine.NewTextMessage(engine.RoleAssistant, "policy-story:"+toolResult),
		}, nil
	}

	if latestUserText(request.Messages) == "run diagnostics" {
		input, _ := json.Marshal(map[string]any{
			"command": "pwd",
		})
		return engine.CompletionResponse{
			Message: engine.Message{
				Role: engine.RoleAssistant,
				Content: []engine.Block{
					{Type: engine.BlockText, Text: "running diagnostics"},
					{Type: engine.BlockToolUse, ID: "toolu_story_bash", Name: "bash", Input: input},
				},
			},
		}, nil
	}

	return engine.CompletionResponse{
		Message: engine.NewTextMessage(engine.RoleAssistant, "reply:"+latestUserText(request.Messages)),
	}, nil
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

func TestUserStoryUserCanTrackDelegatedAgentsAcrossForegroundAndBackgroundWork(t *testing.T) {
	server, httpServer, cfg := newDaemonHarness(t, &agentStoryProvider{}, nil)
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	client := remote.NewClient(httpServer.URL, cfg.DaemonToken, httpServer.Client())

	foregroundSession, err := client.EnsureSession(context.Background(), "story-agent-foreground")
	if err != nil {
		t.Fatal(err)
	}

	result, updated, err := client.RunTurn(context.Background(), foregroundSession.ID, "delegate analyst", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.FinalText, "reply:child task") {
		t.Fatalf("expected delegated child result in final answer, got %s", result.FinalText)
	}
	if updated.AgentCount != 1 {
		t.Fatalf("expected one tracked foreground agent, got %+v", updated)
	}

	agents, err := client.ListAgents(context.Background(), foregroundSession.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected one foreground agent, got %+v", agents)
	}

	waited, err := client.WaitAgent(context.Background(), foregroundSession.ID, agents[0].ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if waited.Status != engine.AgentIdle {
		t.Fatalf("expected delegated analyst to be idle after completion, got %+v", waited)
	}

	sent, err := client.SendAgent(context.Background(), foregroundSession.ID, agents[0].ID, "follow-up", false)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Status != engine.AgentIdle || sent.Result != "reply:follow-up" {
		t.Fatalf("expected delegated analyst to handle follow-up, got %+v", sent)
	}

	backgroundSession, err := client.EnsureSession(context.Background(), "story-agent-background")
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := client.RunTurn(context.Background(), backgroundSession.ID, "start background watcher", 0); err != nil {
		t.Fatal(err)
	}

	backgroundAgents, err := client.ListAgents(context.Background(), backgroundSession.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(backgroundAgents) < 1 {
		t.Fatalf("expected a tracked background agent, got %+v", backgroundAgents)
	}

	var backgroundID string
	for _, agent := range backgroundAgents {
		if agent.Name == "watcher" || agent.Prompt == "block child" {
			backgroundID = agent.ID
			break
		}
	}
	if backgroundID == "" {
		t.Fatalf("expected watcher agent in %+v", backgroundAgents)
	}

	cancelled, err := client.CancelAgent(context.Background(), backgroundSession.ID, backgroundID, true)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != engine.AgentCancelled {
		t.Fatalf("expected watcher to cancel cleanly, got %+v", cancelled)
	}
}

func TestUserStoryUserCanRerunOneWorkflowTaskWithoutRepeatingEverything(t *testing.T) {
	server, httpServer, cfg := newDaemonHarness(t, &selectiveWorkflowStoryProvider{}, nil)
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	client := remote.NewClient(httpServer.URL, cfg.DaemonToken, httpServer.Client())
	session, err := client.EnsureSession(context.Background(), "story-workflow-selective")
	if err != nil {
		t.Fatal(err)
	}

	result, updated, err := client.RunTurn(context.Background(), session.ID, "prepare daily digest", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.FinalText, "reply:collect report facts") || !strings.Contains(result.FinalText, "reply:draft summary block") {
		t.Fatalf("expected initial digest workflow to include both task outputs, got %s", result.FinalText)
	}
	if updated.WorkflowCount != 1 {
		t.Fatalf("expected one persisted workflow, got %+v", updated)
	}

	workflows, err := client.ListWorkflows(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(workflows) != 1 {
		t.Fatalf("expected one workflow snapshot, got %+v", workflows)
	}

	tasks, err := client.ListWorkflowTasks(context.Background(), session.ID, workflows[0].ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected two workflow tasks, got %+v", tasks)
	}

	resumed, err := client.ResumeWorkflow(context.Background(), session.ID, workflows[0].ID, remote.WorkflowResumeOptions{
		TaskNames: []string{"research"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Workflow.Status != tools.WorkflowCompleted {
		t.Fatalf("expected workflow to remain completed after selective rerun, got %+v", resumed.Workflow)
	}

	byName := map[string]tools.FanoutTaskResultAlias{}
	for _, task := range resumed.Tasks {
		byName[task.Name] = task
	}
	if byName["research"].Attempts != 2 {
		t.Fatalf("expected selected task to rerun exactly once, got %+v", byName["research"])
	}
	if byName["draft"].Attempts != 1 {
		t.Fatalf("expected unselected task to keep its original attempt count, got %+v", byName["draft"])
	}
}

func TestUserStoryOperatorCanRotateSharedTokenFileWithoutDroppingTheRemoteBridge(t *testing.T) {
	cfg := newTestConfig(t)
	tokenFile := filepath.Join(t.TempDir(), "shared-token.txt")
	if err := os.WriteFile(tokenFile, []byte("token-a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.DaemonTokenFile = tokenFile

	server := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &toolStoryProvider{}
	})
	httpServer := httptest.NewServer(server.Handler())
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	client := remote.NewClientWithTokenFile(httpServer.URL, "", tokenFile, httpServer.Client())
	session, err := client.EnsureSession(context.Background(), "story-token-rotation")
	if err != nil {
		t.Fatal(err)
	}

	first, _, err := client.RunTurn(context.Background(), session.ID, "before rotation", 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.FinalText != "reply:before rotation" {
		t.Fatalf("expected first remote turn to succeed before rotation, got %+v", first)
	}

	if err := os.WriteFile(tokenFile, []byte(`["token-b","token-a"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	middle, _, err := client.RunTurn(context.Background(), session.ID, "during rotation", 0)
	if err != nil {
		t.Fatal(err)
	}
	if middle.FinalText != "reply:during rotation" {
		t.Fatalf("expected remote bridge to survive dual-token rotation, got %+v", middle)
	}

	if err := os.WriteFile(tokenFile, []byte("token-b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	last, _, err := client.RunTurn(context.Background(), session.ID, "after rotation", 0)
	if err != nil {
		t.Fatal(err)
	}
	if last.FinalText != "reply:after rotation" {
		t.Fatalf("expected remote bridge to keep working after cutover, got %+v", last)
	}

	staleClient := remote.NewClient(httpServer.URL, "token-a", httpServer.Client())
	_, err = staleClient.ListSessions(context.Background())
	if err == nil {
		t.Fatal("expected stale token to be rejected after cutover")
	}
	var remoteErr *remote.Error
	if !errors.As(err, &remoteErr) || remoteErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected stale token to fail with 401, got %v", err)
	}
}

func TestUserStoryOperatorCanTraceAHookBlockedToolAcrossPolicyHooksAndAudit(t *testing.T) {
	server, httpServer, cfg := newDaemonHarness(t, &policyHookStoryProvider{}, func(cfg *config.Config) {
		cfg.HookBeforeTool = "echo compliance denied >&2; exit 7"
		cfg.HookAfterTool = ""
		cfg.HookAfterTurn = ""
		cfg.HookAgentEvent = ""
		cfg.HookEventFile = filepath.Join(cfg.WorkingDir, ".xxx-code", "hooks", "events.jsonl")
	})
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	client := remote.NewClient(httpServer.URL, cfg.DaemonToken, httpServer.Client())
	session, err := client.EnsureSession(context.Background(), "story-hook-audit")
	if err != nil {
		t.Fatal(err)
	}

	policy, err := client.GetPolicy(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.BashEnabled {
		t.Fatalf("expected bash to stay enabled so the hook, not policy, blocks the tool: %+v", policy)
	}

	hooks, err := client.GetHooks(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hooks.BeforeTool == "" || hooks.EventFile == "" {
		t.Fatalf("expected hook config to expose the blocking script and event file, got %+v", hooks)
	}

	result, _, err := client.RunTurn(context.Background(), session.ID, "run diagnostics", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.FinalText, "tool blocked by before_tool hook") {
		t.Fatalf("expected final answer to surface hook-blocked tool context, got %s", result.FinalText)
	}

	sessionAudit, err := client.ListSessionAudit(context.Background(), session.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRemoteAuditEvent(sessionAudit, "hook_error", "hook_error") {
		t.Fatalf("expected hook_error in session audit, got %+v", sessionAudit)
	}

	globalAudit, err := client.ListAudit(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRemoteAuditAction(globalAudit, "request", session.ID) {
		t.Fatalf("expected global audit to include the turn request for %s, got %+v", session.ID, globalAudit)
	}

	hookEvents := readIntegrationHookEvents(t, hooks.EventFile)
	if len(hookEvents) < 2 {
		t.Fatalf("expected hook event file to contain before_tool and after_turn entries, got %+v", hookEvents)
	}
	if hookEvents[0].Kind != engine.HookBeforeTool || hookEvents[0].ToolName != "bash" {
		t.Fatalf("expected first hook event to be before_tool for bash, got %+v", hookEvents[0])
	}
	last := hookEvents[len(hookEvents)-1]
	if last.Kind != engine.HookAfterTurn || last.Status != "completed" {
		t.Fatalf("expected after_turn completion event at the end, got %+v", last)
	}
}

func TestUserStoryUserCanStreamAReplyAndAuditTheTurnAfterward(t *testing.T) {
	server, httpServer, cfg := newDaemonHarness(t, &streamingEchoProvider{}, nil)
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	client := remote.NewClient(httpServer.URL, cfg.DaemonToken, httpServer.Client())
	session, err := client.EnsureSession(context.Background(), "story-stream-audit")
	if err != nil {
		t.Fatal(err)
	}

	var chunks []string
	result, updated, err := client.StreamTurn(context.Background(), session.ID, "draft my update", 0, func(event remote.TurnStreamEvent) {
		if event.Type == string(engine.EventAssistantTextDelta) {
			chunks = append(chunks, event.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "reply:draft my update" {
		t.Fatalf("expected streamed final text, got %+v", result)
	}
	if strings.Join(chunks, "") != result.FinalText {
		t.Fatalf("expected streamed chunks to reconstruct final text, got chunks=%+v result=%+v", chunks, result)
	}
	if updated.ID != session.ID {
		t.Fatalf("expected updated session summary for the same session, got %+v", updated)
	}

	sessionAudit, err := client.ListSessionAudit(context.Background(), session.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRemoteAuditAction(sessionAudit, string(engine.EventAssistantTextDelta), session.ID) {
		t.Fatalf("expected session audit to record assistant stream deltas, got %+v", sessionAudit)
	}

	globalAudit, err := client.ListAudit(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRemoteAuditAction(globalAudit, "request", session.ID) {
		t.Fatalf("expected global audit to include the streamed turn request, got %+v", globalAudit)
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

func hasRemoteAuditEvent(events []remote.AuditEvent, action, code string) bool {
	for _, event := range events {
		if event.Action == action && event.Code == code {
			return true
		}
	}
	return false
}

func hasRemoteAuditAction(events []remote.AuditEvent, action, sessionID string) bool {
	for _, event := range events {
		if event.Action != action {
			continue
		}
		if sessionID == "" || event.SessionID == sessionID {
			return true
		}
	}
	return false
}

func readIntegrationHookEvents(t *testing.T, path string) []engine.HookEvent {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var events []engine.HookEvent
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event engine.HookEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("parse hook event line %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}
