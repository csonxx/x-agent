package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/config"
	"github.com/caowenhua/x-agent/xxx-code/internal/daemon"
	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	"github.com/caowenhua/x-agent/xxx-code/internal/tools"
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

type remoteBlockingProvider struct{}

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

func (p *remoteBlockingProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	_ = request
	<-ctx.Done()
	return engine.CompletionResponse{}, ctx.Err()
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
		ReadRoots:         []string{dir},
		WriteRoots:        []string{dir},
		BashEnabled:       true,
	}
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
