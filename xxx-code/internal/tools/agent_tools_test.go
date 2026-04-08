package tools

import (
	"context"
	"encoding/json"
	"errors"
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

type conditionalToolPromptProvider struct{}

func (p *conditionalToolPromptProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	_ = ctx
	text := latestToolUserText(request.Messages)
	if strings.Contains(text, "fail") {
		return engine.CompletionResponse{}, errors.New("forced failure: " + text)
	}
	return engine.CompletionResponse{
		Message: engine.NewTextMessage(engine.RoleAssistant, "reply:"+text),
	}, nil
}

type fanoutResponse struct {
	Agents []engine.AgentSnapshot `json:"agents"`
	Tasks  []fanoutTaskPayload    `json:"tasks"`
}

type fanoutTaskPayload struct {
	Name           string   `json:"name"`
	Status         string   `json:"status"`
	Prompt         string   `json:"prompt"`
	ResolvedPrompt string   `json:"resolved_prompt"`
	DependsOn      []string `json:"depends_on"`
	AgentID        string   `json:"agent_id"`
	Result         string   `json:"result"`
	Error          string   `json:"error"`
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

func TestAgentFanoutToolSupportsDependencies(t *testing.T) {
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
			{"name": "plan", "prompt": "plan work"},
			{"name": "implement", "prompt": "implement work", "depends_on": []string{"plan"}},
			{"name": "docs", "prompt": "document work", "depends_on": []string{"plan"}},
		},
	})

	result, err := (&AgentFanoutTool{}).Call(context.Background(), execCtx, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %s", result.Content)
	}

	var payload fanoutResponse
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Agents) != 3 || len(payload.Tasks) != 3 {
		t.Fatalf("expected 3 agents and 3 tasks, got %+v", payload)
	}

	byName := mapFanoutTasks(payload.Tasks)
	if byName["plan"].Status != "idle" {
		t.Fatalf("expected plan task to complete, got %+v", byName["plan"])
	}
	if byName["implement"].Status != "idle" {
		t.Fatalf("expected implement task to complete, got %+v", byName["implement"])
	}
	if byName["docs"].Status != "idle" {
		t.Fatalf("expected docs task to complete, got %+v", byName["docs"])
	}
}

func TestAgentFanoutToolSkipsTasksWithFailedDependencies(t *testing.T) {
	dir := t.TempDir()
	runner := engine.NewRunner(&conditionalToolPromptProvider{}, engine.NewRegistry(), engine.RunnerConfig{
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
			{"name": "plan", "prompt": "fail planning"},
			{"name": "implement", "prompt": "implement work", "depends_on": []string{"plan"}},
			{"name": "docs", "prompt": "document work"},
		},
	})

	result, err := (&AgentFanoutTool{}).Call(context.Background(), execCtx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("expected workflow error result, got success: %s", result.Content)
	}

	var payload fanoutResponse
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Agents) != 2 {
		t.Fatalf("expected only spawned agents to be returned, got %+v", payload.Agents)
	}

	byName := mapFanoutTasks(payload.Tasks)
	if byName["plan"].Status != "failed" {
		t.Fatalf("expected failed plan task, got %+v", byName["plan"])
	}
	if byName["implement"].Status != "skipped" {
		t.Fatalf("expected skipped dependent task, got %+v", byName["implement"])
	}
	if byName["implement"].AgentID != "" {
		t.Fatalf("expected skipped dependent task to have no agent id, got %+v", byName["implement"])
	}
	if !strings.Contains(byName["implement"].Error, "dependency plan") {
		t.Fatalf("expected dependency skip reason, got %+v", byName["implement"])
	}
	if byName["docs"].Status != "idle" {
		t.Fatalf("expected independent task to complete, got %+v", byName["docs"])
	}
}

func TestAgentFanoutToolRejectsDependenciesWithoutWait(t *testing.T) {
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
		"wait": false,
		"tasks": []map[string]any{
			{"name": "plan", "prompt": "plan work"},
			{"name": "implement", "prompt": "implement work", "depends_on": []string{"plan"}},
		},
	})

	_, err := (&AgentFanoutTool{}).Call(context.Background(), execCtx, input)
	if err == nil || !strings.Contains(err.Error(), "depends_on requires wait=true") {
		t.Fatalf("expected depends_on wait validation error, got %v", err)
	}
}

func TestAgentFanoutToolRejectsDependencyCycles(t *testing.T) {
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
			{"name": "plan", "prompt": "plan work", "depends_on": []string{"implement"}},
			{"name": "implement", "prompt": "implement work", "depends_on": []string{"plan"}},
		},
	})

	_, err := (&AgentFanoutTool{}).Call(context.Background(), execCtx, input)
	if err == nil || !strings.Contains(err.Error(), "dependency cycle") {
		t.Fatalf("expected dependency cycle error, got %v", err)
	}
}

func TestAgentFanoutToolInjectsDependencyResultsIntoPrompt(t *testing.T) {
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
			{"name": "reader", "prompt": "read source"},
			{"name": "writer", "prompt": "summarize {{tasks.reader.result}}", "depends_on": []string{"reader"}},
		},
	})

	result, err := (&AgentFanoutTool{}).Call(context.Background(), execCtx, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %s", result.Content)
	}

	var payload fanoutResponse
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatal(err)
	}
	byName := mapFanoutTasks(payload.Tasks)
	if !strings.Contains(byName["writer"].ResolvedPrompt, "reply:read source") {
		t.Fatalf("expected resolved prompt to include upstream result, got %+v", byName["writer"])
	}
	if !strings.Contains(byName["writer"].Result, "reply:summarize reply:read source") {
		t.Fatalf("expected downstream result to reflect injected prompt, got %+v", byName["writer"])
	}
}

func TestAgentFanoutToolRejectsPromptReferencesWithoutDependencies(t *testing.T) {
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
			{"name": "reader", "prompt": "read source"},
			{"name": "writer", "prompt": "summarize {{tasks.reader.result}}"},
		},
	})

	_, err := (&AgentFanoutTool{}).Call(context.Background(), execCtx, input)
	if err == nil || !strings.Contains(err.Error(), "does not declare depends_on") {
		t.Fatalf("expected missing depends_on validation error, got %v", err)
	}
}

func TestAgentFanoutToolRejectsPromptReferencesToUnknownTasks(t *testing.T) {
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
			{"name": "writer", "prompt": "summarize {{tasks.reader.result}}", "depends_on": []string{"reader"}},
		},
	})

	_, err := (&AgentFanoutTool{}).Call(context.Background(), execCtx, input)
	if err == nil || !strings.Contains(err.Error(), "unknown task") {
		t.Fatalf("expected unknown task validation error, got %v", err)
	}
}

func mapFanoutTasks(tasks []fanoutTaskPayload) map[string]fanoutTaskPayload {
	byName := make(map[string]fanoutTaskPayload, len(tasks))
	for _, task := range tasks {
		byName[task.Name] = task
	}
	return byName
}
