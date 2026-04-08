package remote

import (
	"context"
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

func newTestClient(t *testing.T) (*Client, func()) {
	t.Helper()
	cfg := newTestConfig(t)
	server := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &remoteTestProvider{}
	})
	httpServer := httptest.NewServer(server.Handler())
	client := NewClient(httpServer.URL, httpServer.Client())
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
	client := NewClient(httpServer.URL, httpServer.Client())
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
