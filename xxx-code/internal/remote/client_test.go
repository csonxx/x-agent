package remote

import (
	"context"
	"io"
	"net/http/httptest"
	"path/filepath"
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
		ReadRoots:         []string{dir},
		WriteRoots:        []string{dir},
		BashEnabled:       true,
	}
}
