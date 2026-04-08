package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
)

type toolPromptProvider struct{}

func (p *toolPromptProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	_ = ctx
	return engine.CompletionResponse{
		Message: engine.NewTextMessage(engine.RoleAssistant, "reply:"+latestToolUserText(request.Messages)),
	}, nil
}

func latestToolUserText(messages []engine.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == engine.RoleUser {
			return messages[i].Text()
		}
	}
	return ""
}

func TestAgentFanoutToolWaitsForBatch(t *testing.T) {
	dir := t.TempDir()
	runner := engine.NewRunner(&toolPromptProvider{}, engine.NewRegistry(), engine.RunnerConfig{
		Model:             "test-model",
		SystemPrompt:      "test",
		MaxTurns:          4,
		WorkingDir:        dir,
		MaxParallelAgents: 2,
	})

	execCtx := &engine.ExecutionContext{
		Runner:     runner,
		Session:    engine.NewSession(),
		WorkingDir: dir,
	}

	input, _ := json.Marshal(map[string]any{
		"wait": true,
		"tasks": []map[string]any{
			{"name": "one", "prompt": "task one"},
			{"name": "two", "prompt": "task two"},
		},
	})

	result, err := (&AgentFanoutTool{}).Call(context.Background(), execCtx, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %s", result.Content)
	}
	if !strings.Contains(result.Content, `"task one"`) || !strings.Contains(result.Content, `"task two"`) {
		t.Fatalf("expected both tasks in result, got %s", result.Content)
	}
	if !strings.Contains(result.Content, `"status": "idle"`) {
		t.Fatalf("expected idle snapshots, got %s", result.Content)
	}
}

func TestAgentWaitToolCanWaitAll(t *testing.T) {
	dir := t.TempDir()
	runner := engine.NewRunner(&toolPromptProvider{}, engine.NewRegistry(), engine.RunnerConfig{
		Model:             "test-model",
		SystemPrompt:      "test",
		MaxTurns:          4,
		WorkingDir:        dir,
		MaxParallelAgents: 2,
	})

	execCtx := &engine.ExecutionContext{
		Runner:     runner,
		Session:    engine.NewSession(),
		WorkingDir: dir,
	}

	if _, err := runner.SpawnAgent(execCtx, engine.SpawnRequest{Name: "one", Prompt: "task one", Background: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.SpawnAgent(execCtx, engine.SpawnRequest{Name: "two", Prompt: "task two", Background: true}); err != nil {
		t.Fatal(err)
	}

	input, _ := json.Marshal(map[string]any{
		"all": true,
	})

	result, err := (&AgentWaitTool{}).Call(context.Background(), execCtx, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %s", result.Content)
	}
	if !strings.Contains(result.Content, `"agents"`) {
		t.Fatalf("expected agents field, got %s", result.Content)
	}
}

func TestAgentSpawnToolPropagatesPriority(t *testing.T) {
	dir := t.TempDir()
	runner := engine.NewRunner(&toolPromptProvider{}, engine.NewRegistry(), engine.RunnerConfig{
		Model:             "test-model",
		SystemPrompt:      "test",
		MaxTurns:          4,
		WorkingDir:        dir,
		MaxParallelAgents: 1,
	})

	execCtx := &engine.ExecutionContext{
		Runner:     runner,
		Session:    engine.NewSession(),
		WorkingDir: dir,
	}

	input, _ := json.Marshal(map[string]any{
		"name":       "priority-worker",
		"prompt":     "task priority",
		"priority":   7,
		"background": true,
	})

	result, err := (&AgentSpawnTool{}).Call(context.Background(), execCtx, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %s", result.Content)
	}
	if !strings.Contains(result.Content, `"priority": 7`) {
		t.Fatalf("expected priority in response, got %s", result.Content)
	}

	snapshots := runner.ListAgents()
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(snapshots))
	}
	if snapshots[0].Priority != 7 {
		t.Fatalf("expected priority 7, got %+v", snapshots[0])
	}
}
