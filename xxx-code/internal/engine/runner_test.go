package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

type stubProvider struct {
	calls int
}

func (p *stubProvider) CreateMessage(ctx context.Context, request CompletionRequest) (CompletionResponse, error) {
	_ = ctx
	p.calls++
	if p.calls == 1 {
		input, _ := json.Marshal(map[string]any{"value": "done"})
		return CompletionResponse{
			Message: Message{
				Role: RoleAssistant,
				Content: []Block{
					{Type: BlockText, Text: "calling tool"},
					{Type: BlockToolUse, ID: "tool-1", Name: "echo_tool", Input: input},
				},
			},
		}, nil
	}
	return CompletionResponse{
		Message: NewTextMessage(RoleAssistant, "final answer"),
	}, nil
}

type echoTool struct{}

func (t *echoTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        "echo_tool",
		Description: "Echo input",
		InputSchema: map[string]any{"type": "object"},
	}
}

func (t *echoTool) Call(ctx context.Context, exec *ExecutionContext, input json.RawMessage) (ToolResult, error) {
	_ = ctx
	_ = exec
	return ToolResult{Content: string(input)}, nil
}

func TestRunnerExecutesToolLoop(t *testing.T) {
	provider := &stubProvider{}
	runner := NewRunner(provider, NewRegistry(&echoTool{}), RunnerConfig{
		Model:        "test-model",
		SystemPrompt: "test",
		MaxTurns:     4,
	})
	session := NewSession()

	result, err := runner.RunTurn(context.Background(), session, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "final answer" {
		t.Fatalf("unexpected final text: %q", result.FinalText)
	}
	if provider.calls != 2 {
		t.Fatalf("expected 2 provider calls, got %d", provider.calls)
	}
}

type promptProvider struct{}

func (p *promptProvider) CreateMessage(ctx context.Context, request CompletionRequest) (CompletionResponse, error) {
	_ = ctx
	return CompletionResponse{
		Message: NewTextMessage(RoleAssistant, "reply:"+latestUserText(request.Messages)),
	}, nil
}

func latestUserText(messages []Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == RoleUser {
			return messages[i].Text()
		}
	}
	return ""
}

func TestRunnerCanReuseSpawnedAgent(t *testing.T) {
	runner := NewRunner(&promptProvider{}, NewRegistry(), RunnerConfig{
		Model:        "test-model",
		SystemPrompt: "test",
		MaxTurns:     4,
	})

	first, err := runner.SpawnAgent(nil, SpawnRequest{
		Name:   "worker",
		Prompt: "first task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != AgentIdle {
		t.Fatalf("expected idle status, got %s", first.Status)
	}
	if first.Result != "reply:first task" {
		t.Fatalf("unexpected first result: %q", first.Result)
	}

	second, err := runner.SendAgent(context.Background(), first.ID, "second task", false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != AgentIdle {
		t.Fatalf("expected idle status after send, got %s", second.Status)
	}
	if second.Result != "reply:second task" {
		t.Fatalf("unexpected second result: %q", second.Result)
	}

	agents := runner.ExportAgents()
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if len(agents[0].Session) != 4 {
		t.Fatalf("expected 4 messages in agent session, got %d", len(agents[0].Session))
	}

	gotTexts := make([]string, 0, len(agents[0].Session))
	for _, message := range agents[0].Session {
		gotTexts = append(gotTexts, message.Text())
	}
	joined := strings.Join(gotTexts, " | ")
	if !strings.Contains(joined, "first task") || !strings.Contains(joined, "second task") {
		t.Fatalf("expected preserved agent history, got %q", joined)
	}
}

type blockingProvider struct {
	once    sync.Once
	started chan struct{}
}

func (p *blockingProvider) CreateMessage(ctx context.Context, request CompletionRequest) (CompletionResponse, error) {
	_ = request
	p.once.Do(func() {
		close(p.started)
	})
	<-ctx.Done()
	return CompletionResponse{}, ctx.Err()
}

func TestRunnerCanCancelAgent(t *testing.T) {
	runner := NewRunner(&blockingProvider{
		started: make(chan struct{}),
	}, NewRegistry(), RunnerConfig{
		Model:        "test-model",
		SystemPrompt: "test",
		MaxTurns:     4,
	})

	snapshot, err := runner.SpawnAgent(nil, SpawnRequest{
		Name:       "worker",
		Prompt:     "long task",
		Background: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	blocking := runner.provider.(*blockingProvider)
	select {
	case <-blocking.started:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not start")
	}

	cancelled, err := runner.CancelAgent(context.Background(), snapshot.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != AgentCancelled {
		t.Fatalf("expected cancelled status, got %s", cancelled.Status)
	}
}

func TestRunnerCompactsLargeSession(t *testing.T) {
	runner := NewRunner(&promptProvider{}, NewRegistry(), RunnerConfig{
		Model:               "test-model",
		SystemPrompt:        "test",
		MaxTurns:            4,
		ContextBudget:       20,
		CompactKeepMessages: 2,
	})

	session := NewSession(
		NewTextMessage(RoleUser, strings.Repeat("alpha ", 40)),
		NewTextMessage(RoleAssistant, strings.Repeat("beta ", 40)),
		NewTextMessage(RoleUser, strings.Repeat("gamma ", 30)),
		NewTextMessage(RoleAssistant, strings.Repeat("delta ", 30)),
		NewTextMessage(RoleUser, "recent question"),
		NewTextMessage(RoleAssistant, "recent answer"),
	)

	report, changed := runner.CompactSession(session)
	if !changed {
		t.Fatal("expected session compaction")
	}
	if report.AfterMessages >= report.BeforeMessages {
		t.Fatalf("expected fewer messages after compaction, got %+v", report)
	}

	messages := session.Snapshot()
	if len(messages) != 5 {
		t.Fatalf("expected compacted session length 5, got %d", len(messages))
	}
	if !strings.Contains(messages[0].Text(), "[compacted summary]") {
		t.Fatalf("expected summary marker, got %q", messages[0].Text())
	}
}
