package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/config"
	"github.com/caowenhua/x-agent/xxx-code/internal/diag"
	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	"github.com/caowenhua/x-agent/xxx-code/internal/persist"
)

type daemonTestProvider struct{}

type blockingProvider struct {
	started chan struct{}
}

func (p *daemonTestProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
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

func (p *blockingProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	_ = request
	select {
	case <-p.started:
	default:
		close(p.started)
	}
	<-ctx.Done()
	return engine.CompletionResponse{}, ctx.Err()
}

func TestDaemonSessionLifecycle(t *testing.T) {
	server, testServer := newTestDaemon(t)
	defer func() {
		_ = server.Close()
		testServer.Close()
	}()

	created := postJSON(t, testServer.URL+"/v1/sessions", map[string]any{}, http.StatusCreated)
	session := created["session"].(map[string]any)
	sessionID := session["id"].(string)

	result := postJSON(t, testServer.URL+"/v1/sessions/"+sessionID+"/turns", map[string]any{
		"prompt": "hello daemon",
	}, http.StatusOK)
	runResult := result["result"].(map[string]any)
	if runResult["final_text"] != "reply:hello daemon" {
		t.Fatalf("unexpected final text: %+v", runResult)
	}

	messages := getJSON(t, testServer.URL+"/v1/sessions/"+sessionID+"/messages?limit=2", http.StatusOK)
	if got := len(messages["messages"].([]any)); got != 2 {
		t.Fatalf("expected 2 messages, got %d", got)
	}

	sessions := getJSON(t, testServer.URL+"/v1/sessions", http.StatusOK)
	items := sessions["sessions"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected one session, got %d", len(items))
	}
	summary := items[0].(map[string]any)
	if summary["id"] != sessionID {
		t.Fatalf("unexpected session summary: %+v", summary)
	}
	if summary["loaded"] != true {
		t.Fatalf("expected loaded session summary, got %+v", summary)
	}
}

func TestDaemonCanReloadSavedSession(t *testing.T) {
	cfg := newTestConfig(t)
	serverA := New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &daemonTestProvider{}
	})
	httpA := httptest.NewServer(serverA.Handler())

	created := postJSON(t, httpA.URL+"/v1/sessions", map[string]any{}, http.StatusCreated)
	sessionID := created["session"].(map[string]any)["id"].(string)
	postJSON(t, httpA.URL+"/v1/sessions/"+sessionID+"/turns", map[string]any{
		"prompt": "first turn",
	}, http.StatusOK)

	httpA.Close()
	if err := serverA.Close(); err != nil {
		t.Fatal(err)
	}

	serverB := New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &daemonTestProvider{}
	})
	httpB := httptest.NewServer(serverB.Handler())
	defer func() {
		_ = serverB.Close()
		httpB.Close()
	}()

	sessions := getJSON(t, httpB.URL+"/v1/sessions", http.StatusOK)
	items := sessions["sessions"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected one saved session, got %d", len(items))
	}
	summary := items[0].(map[string]any)
	if summary["id"] != sessionID {
		t.Fatalf("unexpected persisted session summary: %+v", summary)
	}
	if summary["loaded"] != false {
		t.Fatalf("expected persisted session to be unloaded before reopen, got %+v", summary)
	}

	postJSON(t, httpB.URL+"/v1/sessions/"+sessionID+"/turns", map[string]any{
		"prompt": "second turn",
	}, http.StatusOK)

	messages := getJSON(t, httpB.URL+"/v1/sessions/"+sessionID+"/messages", http.StatusOK)
	items = messages["messages"].([]any)
	if len(items) != 4 {
		t.Fatalf("expected 4 resumed messages, got %d", len(items))
	}
	var texts []string
	for _, item := range items {
		message := item.(map[string]any)
		content := message["content"].([]any)
		if len(content) == 0 {
			continue
		}
		block := content[0].(map[string]any)
		if text, ok := block["text"].(string); ok {
			texts = append(texts, text)
		}
	}
	joined := strings.Join(texts, " | ")
	if !strings.Contains(joined, "first turn") || !strings.Contains(joined, "second turn") {
		t.Fatalf("expected resumed transcript, got %s", joined)
	}
}

func TestDaemonCanRequireBearerToken(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.DaemonToken = "secret-token"
	server := New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &daemonTestProvider{}
	})
	testServer := httptest.NewServer(server.Handler())
	defer func() {
		_ = server.Close()
		testServer.Close()
	}()

	req, err := http.NewRequest(http.MethodGet, testServer.URL+"/v1/sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 401 without token, got %d: %s", resp.StatusCode, string(body))
	}

	req, err = http.NewRequest(http.MethodGet, testServer.URL+"/v1/sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret-token")
	payload := doJSON(t, req, http.StatusOK)
	if len(payload["sessions"].([]any)) != 0 {
		t.Fatalf("expected no sessions, got %+v", payload)
	}
}

func TestManagedSessionCloseCancelsActiveTurnAndPersistsState(t *testing.T) {
	cfg := newTestConfig(t)
	provider := &blockingProvider{started: make(chan struct{})}
	server := New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return provider
	})
	session, err := server.openSession(context.Background(), "closing-session", false)
	if err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, runErr := session.runTurn(context.Background(), "block until close")
		errCh <- runErr
	}()

	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for blocking turn to start")
	}

	if err := session.close(); err != nil {
		t.Fatal(err)
	}

	select {
	case runErr := <-errCh:
		if !errors.Is(runErr, context.Canceled) {
			t.Fatalf("expected close to cancel the active turn, got %v", runErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for active turn to stop during close")
	}

	state, err := persist.Load(session.sessionFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Main) != 1 {
		t.Fatalf("expected partially completed turn to be persisted, got %d messages", len(state.Main))
	}
	if got := strings.TrimSpace(state.Main[0].Text()); got != "block until close" {
		t.Fatalf("unexpected persisted message: %q", got)
	}
}

func TestManagedSessionPublishEventDoesNotBlockOnSlowSubscriber(t *testing.T) {
	session := &managedSession{
		subs: make(map[int]*eventSubscriber),
	}
	session.subs[1] = &eventSubscriber{ch: make(chan engine.Event, 1)}
	session.subs[1].ch <- engine.Event{Kind: engine.EventAssistantText}

	done := make(chan struct{})
	go func() {
		session.publishEvent(engine.Event{Kind: engine.EventToolCall})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("publishEvent blocked on a full subscriber channel")
	}

	if got := len(session.subs[1].ch); got != 1 {
		t.Fatalf("expected bounded subscriber buffer to stay full at 1 item, got %d", got)
	}
}

func TestDaemonErrorsIncludeStructuredCode(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.DaemonToken = "secret-token"
	server := New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &daemonTestProvider{}
	})
	testServer := httptest.NewServer(server.Handler())
	defer func() {
		_ = server.Close()
		testServer.Close()
	}()

	req, err := http.NewRequest(http.MethodGet, testServer.URL+"/v1/sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["code"] != "unauthorized" {
		t.Fatalf("expected unauthorized code, got %+v", payload)
	}

	req, err = http.NewRequest(http.MethodGet, testServer.URL+"/v1/sessions/missing-session", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret-token")
	payload = doJSON(t, req, http.StatusNotFound)
	if payload["code"] != "session_not_found" {
		t.Fatalf("expected session_not_found code, got %+v", payload)
	}
}

func TestDaemonAddsTraceIDHeader(t *testing.T) {
	server, testServer := newTestDaemon(t)
	defer func() {
		_ = server.Close()
		testServer.Close()
	}()

	req, err := http.NewRequest(http.MethodGet, testServer.URL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(diag.TraceHeader, "trace_test")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get(diag.TraceHeader); got != "trace_test" {
		t.Fatalf("expected trace header to be preserved, got %q", got)
	}

	req, err = http.NewRequest(http.MethodGet, testServer.URL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get(diag.TraceHeader), "trace_") {
		t.Fatalf("expected daemon to generate a trace id, got %q", resp.Header.Get(diag.TraceHeader))
	}
}

func newTestDaemon(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	cfg := newTestConfig(t)
	server := New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &daemonTestProvider{}
	})
	return server, httptest.NewServer(server.Handler())
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

func postJSON(t *testing.T, url string, body any, wantStatus int) map[string]any {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return doJSON(t, req, wantStatus)
}

func getJSON(t *testing.T, url string, wantStatus int) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return doJSON(t, req, wantStatus)
}

func doJSON(t *testing.T, req *http.Request, wantStatus int) map[string]any {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}
