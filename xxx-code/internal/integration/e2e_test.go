package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/config"
	"github.com/caowenhua/x-agent/xxx-code/internal/daemon"
	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	"github.com/caowenhua/x-agent/xxx-code/internal/remote"
)

type streamingEchoProvider struct{}

func (p *streamingEchoProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	_ = ctx
	prompt := latestUserText(request.Messages)
	return engine.CompletionResponse{
		Message: engine.NewTextMessage(engine.RoleAssistant, "reply:"+prompt),
	}, nil
}

func (p *streamingEchoProvider) CreateMessageStream(ctx context.Context, request engine.CompletionRequest, handle func(engine.StreamEvent)) (engine.CompletionResponse, error) {
	_ = ctx
	prompt := latestUserText(request.Messages)
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

type orchestrationProvider struct{}

func (p *orchestrationProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	_ = ctx

	if toolResult, ok := latestToolResult(request.Messages); ok {
		return engine.CompletionResponse{
			Message: engine.NewTextMessage(engine.RoleAssistant, "tool-result:"+toolResult),
		}, nil
	}

	switch prompt := latestUserText(request.Messages); prompt {
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
	case "fanout work":
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
	default:
		return engine.CompletionResponse{
			Message: engine.NewTextMessage(engine.RoleAssistant, "reply:"+prompt),
		}, nil
	}
}

func TestRemoteStreamingRoundTripSurvivesDaemonRestart(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.DaemonToken = "stream-secret"

	serverA := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &streamingEchoProvider{}
	})
	httpA := httptest.NewServer(serverA.Handler())

	clientA := remote.NewClient(httpA.URL, cfg.DaemonToken, httpA.Client())
	session, err := clientA.EnsureSession(context.Background(), "stream-session")
	if err != nil {
		t.Fatal(err)
	}

	var chunks []string
	result, updated, err := clientA.StreamTurn(context.Background(), session.ID, "hello integration", 0, func(event remote.TurnStreamEvent) {
		if event.Type == string(engine.EventAssistantTextDelta) {
			chunks = append(chunks, event.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "reply:hello integration" {
		t.Fatalf("unexpected final text: %+v", result)
	}
	if strings.Join(chunks, "") != "reply:hello integration" {
		t.Fatalf("unexpected stream chunks: %+v", chunks)
	}
	if updated.ID != session.ID {
		t.Fatalf("unexpected updated session: %+v", updated)
	}

	httpA.Close()
	if err := serverA.Close(); err != nil {
		t.Fatal(err)
	}

	serverB := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &streamingEchoProvider{}
	})
	httpB := httptest.NewServer(serverB.Handler())
	defer func() {
		httpB.Close()
		_ = serverB.Close()
	}()

	clientB := remote.NewClient(httpB.URL, cfg.DaemonToken, httpB.Client())
	reloaded, err := clientB.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.MessageCount != 2 {
		t.Fatalf("expected 2 persisted messages, got %+v", reloaded)
	}
	messages, err := clientB.ListMessages(context.Background(), session.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages after restart, got %d", len(messages))
	}
}

func TestRemoteDaemonCanExecuteDelegatingAgentTurn(t *testing.T) {
	server, httpServer, cfg := newDaemonHarness(t, &orchestrationProvider{}, nil)
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	client := remote.NewClient(httpServer.URL, cfg.DaemonToken, httpServer.Client())
	session, err := client.EnsureSession(context.Background(), "delegate-session")
	if err != nil {
		t.Fatal(err)
	}

	result, updated, err := client.RunTurn(context.Background(), session.ID, "delegate work", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.FinalText, "reply:child task") {
		t.Fatalf("expected delegated child result in final text, got %s", result.FinalText)
	}
	if updated.AgentCount != 1 {
		t.Fatalf("expected one tracked agent, got %+v", updated)
	}

	agents, err := client.ListAgents(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected one agent, got %d", len(agents))
	}
	if agents[0].Status != engine.AgentIdle {
		t.Fatalf("expected finished agent status idle, got %+v", agents[0])
	}
}

func TestRemoteDaemonCanExecuteWorkflowTurn(t *testing.T) {
	server, httpServer, cfg := newDaemonHarness(t, &orchestrationProvider{}, nil)
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	client := remote.NewClient(httpServer.URL, cfg.DaemonToken, httpServer.Client())
	session, err := client.EnsureSession(context.Background(), "workflow-session")
	if err != nil {
		t.Fatal(err)
	}

	result, updated, err := client.RunTurn(context.Background(), session.ID, "fanout work", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.FinalText, "reply:task one") || !strings.Contains(result.FinalText, "reply:task two") {
		t.Fatalf("expected both workflow task results, got %s", result.FinalText)
	}
	if updated.WorkflowCount != 1 {
		t.Fatalf("expected one workflow summary, got %+v", updated)
	}

	workflows, err := client.ListWorkflows(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(workflows) != 1 {
		t.Fatalf("expected one workflow, got %d", len(workflows))
	}
	if workflows[0].Status != "completed" {
		t.Fatalf("expected completed workflow, got %+v", workflows[0])
	}
}

func newDaemonHarness(t *testing.T, provider engine.Provider, mutate func(*config.Config)) (*daemon.Server, *httptest.Server, config.Config) {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.DaemonToken = "integration-secret"
	if mutate != nil {
		mutate(&cfg)
	}
	server := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return provider
	})
	httpServer := httptest.NewServer(server.Handler())
	return server, httpServer, cfg
}

func newTestConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	return config.Config{
		Model:             "test-model",
		SystemPrompt:      "test",
		MaxTurns:          6,
		MaxTokens:         4096,
		MaxParallelAgents: 2,
		ContextBudget:     4000,
		CompactKeep:       6,
		WorkingDir:        dir,
		DaemonDir:         filepath.Join(dir, ".xxx-code", "daemon"),
		ToolTimeout:       2 * time.Second,
		HookTimeout:       time.Second,
		ReadRoots:         []string{dir},
		WriteRoots:        []string{dir},
		BashEnabled:       true,
	}
}

func latestUserText(messages []engine.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == engine.RoleUser {
			text := strings.TrimSpace(messages[i].Text())
			if text != "" {
				return text
			}
		}
	}
	return ""
}

func latestToolResult(messages []engine.Message) (string, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != engine.RoleUser {
			continue
		}
		for j := len(messages[i].Content) - 1; j >= 0; j-- {
			block := messages[i].Content[j]
			if block.Type == engine.BlockToolResult {
				return block.Result, true
			}
		}
	}
	return "", false
}
