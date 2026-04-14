package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

type flakyWorkflowProvider struct {
	mu       sync.Mutex
	failures map[string]int
	attempts map[string]int
}

func newFlakyWorkflowProvider(failures map[string]int) *flakyWorkflowProvider {
	return &flakyWorkflowProvider{
		failures: failures,
		attempts: make(map[string]int),
	}
}

func (p *flakyWorkflowProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	_ = ctx
	text := latestToolUserText(request.Messages)

	p.mu.Lock()
	p.attempts[text]++
	attempt := p.attempts[text]
	limit := p.failures[text]
	p.mu.Unlock()

	if attempt <= limit {
		return engine.CompletionResponse{}, errors.New("forced retry failure: " + text)
	}
	return engine.CompletionResponse{
		Message: engine.NewTextMessage(engine.RoleAssistant, "reply:"+text),
	}, nil
}

type gatedWorkflowProvider struct {
	mu      sync.Mutex
	started map[string]chan struct{}
	release map[string]chan struct{}
}

func newGatedWorkflowProvider() *gatedWorkflowProvider {
	return &gatedWorkflowProvider{
		started: make(map[string]chan struct{}),
		release: make(map[string]chan struct{}),
	}
}

func (p *gatedWorkflowProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	prompt := latestToolUserText(request.Messages)
	p.markStarted(prompt)

	select {
	case <-p.releaseChan(prompt):
	case <-ctx.Done():
		return engine.CompletionResponse{}, ctx.Err()
	}

	if strings.Contains(prompt, "fail") {
		return engine.CompletionResponse{}, errors.New("forced failure: " + prompt)
	}
	return engine.CompletionResponse{
		Message: engine.NewTextMessage(engine.RoleAssistant, "reply:"+prompt),
	}, nil
}

func (p *gatedWorkflowProvider) startedChan(key string) chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ch, ok := p.started[key]; ok {
		return ch
	}
	ch := make(chan struct{}, 1)
	p.started[key] = ch
	return ch
}

func (p *gatedWorkflowProvider) releaseChan(key string) chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ch, ok := p.release[key]; ok {
		return ch
	}
	ch := make(chan struct{})
	p.release[key] = ch
	return ch
}

func (p *gatedWorkflowProvider) markStarted(prompt string) {
	ch := p.startedChan(prompt)
	select {
	case ch <- struct{}{}:
	default:
	}
}

type fanoutResponse struct {
	Workflow WorkflowSnapshot       `json:"workflow"`
	Agents   []engine.AgentSnapshot `json:"agents"`
	Tasks    []fanoutTaskPayload    `json:"tasks"`
}

type fanoutTaskPayload struct {
	Name           string   `json:"name"`
	Status         string   `json:"status"`
	Prompt         string   `json:"prompt"`
	ResolvedPrompt string   `json:"resolved_prompt"`
	DependsOn      []string `json:"depends_on"`
	Resource       string   `json:"resource"`
	Retries        int      `json:"retries"`
	Attempts       int      `json:"attempts"`
	Preemptions    int      `json:"preemptions"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	AgentID        string   `json:"agent_id"`
	Result         string   `json:"result"`
	Error          string   `json:"error"`
	ArtifactFile   string   `json:"artifact_file"`
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

func TestAgentFanoutToolAcceptsStringifiedTasks(t *testing.T) {
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

	tasksJSON := `[{"name":"one","prompt":"task one"},{"name":"two","prompt":"task two"}]`
	input, _ := json.Marshal(map[string]any{
		"wait":  true,
		"tasks": tasksJSON,
	})

	result, err := (&AgentFanoutTool{}).Call(context.Background(), execCtx, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected success for stringified tasks, got %s", result.Content)
	}
	if !strings.Contains(result.Content, `"task one"`) || !strings.Contains(result.Content, `"task two"`) {
		t.Fatalf("expected both tasks in result, got %s", result.Content)
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

func TestAgentFanoutToolHonorsWorkflowMaxParallel(t *testing.T) {
	dir := t.TempDir()
	provider := newGatedWorkflowProvider()
	runner := engine.NewRunner(provider, engine.NewRegistry(), engine.RunnerConfig{
		Model:             "test-model",
		SystemPrompt:      "test",
		MaxTurns:          4,
		WorkingDir:        dir,
		MaxParallelAgents: 3,
	})

	execCtx := &engine.ExecutionContext{
		Runner:     runner,
		Session:    engine.NewSession(),
		WorkingDir: dir,
	}

	resultCh := make(chan engine.ToolResult, 1)
	errCh := make(chan error, 1)
	go func() {
		input, _ := json.Marshal(map[string]any{
			"wait":         true,
			"max_parallel": 1,
			"tasks": []map[string]any{
				{"name": "one", "prompt": "one"},
				{"name": "two", "prompt": "two"},
				{"name": "three", "prompt": "three"},
			},
		})
		result, err := (&AgentFanoutTool{}).Call(context.Background(), execCtx, input)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	oneStarted := provider.startedChan("one")
	twoStarted := provider.startedChan("two")
	threeStarted := provider.startedChan("three")

	select {
	case <-oneStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first task did not start")
	}
	select {
	case <-twoStarted:
		t.Fatal("second task started before workflow slot was released")
	case <-time.After(150 * time.Millisecond):
	}
	close(provider.releaseChan("one"))

	select {
	case <-twoStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("second task did not start after first completed")
	}
	select {
	case <-threeStarted:
		t.Fatal("third task started before second completed")
	case <-time.After(150 * time.Millisecond):
	}
	close(provider.releaseChan("two"))

	select {
	case <-threeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("third task did not start after second completed")
	}
	close(provider.releaseChan("three"))

	select {
	case err := <-errCh:
		t.Fatal(err)
	case result := <-resultCh:
		if result.IsError {
			t.Fatalf("expected success, got error result: %s", result.Content)
		}
	}
}

func TestAgentFanoutToolFailFastCancelsActiveTasks(t *testing.T) {
	dir := t.TempDir()
	provider := newGatedWorkflowProvider()
	runner := engine.NewRunner(provider, engine.NewRegistry(), engine.RunnerConfig{
		Model:             "test-model",
		SystemPrompt:      "test",
		MaxTurns:          4,
		WorkingDir:        dir,
		MaxParallelAgents: 3,
	})

	execCtx := &engine.ExecutionContext{
		Runner:     runner,
		Session:    engine.NewSession(),
		WorkingDir: dir,
	}

	resultCh := make(chan engine.ToolResult, 1)
	errCh := make(chan error, 1)
	go func() {
		input, _ := json.Marshal(map[string]any{
			"wait":         true,
			"fail_fast":    true,
			"max_parallel": 2,
			"tasks": []map[string]any{
				{"name": "fast", "prompt": "fast fail"},
				{"name": "slow", "prompt": "slow work"},
				{"name": "later", "prompt": "later work"},
			},
		})
		result, err := (&AgentFanoutTool{}).Call(context.Background(), execCtx, input)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	fastStarted := provider.startedChan("fast fail")
	slowStarted := provider.startedChan("slow work")
	laterStarted := provider.startedChan("later work")

	select {
	case <-fastStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("fast task did not start")
	}
	select {
	case <-slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("slow task did not start")
	}

	close(provider.releaseChan("fast fail"))

	select {
	case err := <-errCh:
		t.Fatal(err)
	case result := <-resultCh:
		if !result.IsError {
			t.Fatalf("expected fail_fast workflow to return error result, got %s", result.Content)
		}
		var payload fanoutResponse
		if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
			t.Fatal(err)
		}
		byName := mapFanoutTasks(payload.Tasks)
		if byName["fast"].Status != "failed" {
			t.Fatalf("expected fast task to fail, got %+v", byName["fast"])
		}
		if byName["slow"].Status != "cancelled" {
			t.Fatalf("expected slow task to be cancelled, got %+v", byName["slow"])
		}
		if byName["later"].Status != "skipped" {
			t.Fatalf("expected later task to be skipped, got %+v", byName["later"])
		}
	}

	select {
	case <-laterStarted:
		t.Fatal("later task started despite fail_fast")
	case <-time.After(150 * time.Millisecond):
	}
}

func TestAgentFanoutToolRetriesFailedTaskUntilSuccess(t *testing.T) {
	dir := t.TempDir()
	runner := engine.NewRunner(newFlakyWorkflowProvider(map[string]int{
		"flaky work": 1,
	}), engine.NewRegistry(), engine.RunnerConfig{
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
			{"name": "flaky", "prompt": "flaky work", "retries": 1},
		},
	})

	result, err := (&AgentFanoutTool{}).Call(context.Background(), execCtx, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected retried workflow to succeed, got %s", result.Content)
	}

	var payload fanoutResponse
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatal(err)
	}
	byName := mapFanoutTasks(payload.Tasks)
	if byName["flaky"].Status != "idle" {
		t.Fatalf("expected flaky task to recover after retry, got %+v", byName["flaky"])
	}
	if byName["flaky"].Attempts != 2 {
		t.Fatalf("expected two attempts, got %+v", byName["flaky"])
	}
}

func TestAgentFanoutToolRetriesTimedOutTask(t *testing.T) {
	dir := t.TempDir()
	provider := newGatedWorkflowProvider()
	runner := engine.NewRunner(provider, engine.NewRegistry(), engine.RunnerConfig{
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

	resultCh := make(chan engine.ToolResult, 1)
	errCh := make(chan error, 1)
	go func() {
		input, _ := json.Marshal(map[string]any{
			"wait": true,
			"tasks": []map[string]any{
				{"name": "slow", "prompt": "slow retry", "timeout_seconds": 1, "retries": 1},
			},
		})
		result, err := (&AgentFanoutTool{}).Call(context.Background(), execCtx, input)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	started := provider.startedChan("slow retry")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first attempt did not start")
	}

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("second attempt did not start after timeout retry")
	}
	close(provider.releaseChan("slow retry"))

	select {
	case err := <-errCh:
		t.Fatal(err)
	case result := <-resultCh:
		if result.IsError {
			t.Fatalf("expected timeout retry workflow to succeed, got %s", result.Content)
		}
		var payload fanoutResponse
		if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
			t.Fatal(err)
		}
		byName := mapFanoutTasks(payload.Tasks)
		if byName["slow"].Status != "idle" {
			t.Fatalf("expected timed out task to recover, got %+v", byName["slow"])
		}
		if byName["slow"].Attempts != 2 {
			t.Fatalf("expected timed out task to use two attempts, got %+v", byName["slow"])
		}
	}
}

func TestAgentFanoutToolFailFastWaitsForRetriesToExhaust(t *testing.T) {
	dir := t.TempDir()
	provider := newGatedWorkflowProvider()
	runner := engine.NewRunner(provider, engine.NewRegistry(), engine.RunnerConfig{
		Model:             "test-model",
		SystemPrompt:      "test",
		MaxTurns:          4,
		WorkingDir:        dir,
		MaxParallelAgents: 3,
	})

	execCtx := &engine.ExecutionContext{
		Runner:     runner,
		Session:    engine.NewSession(),
		WorkingDir: dir,
	}

	resultCh := make(chan engine.ToolResult, 1)
	errCh := make(chan error, 1)
	go func() {
		input, _ := json.Marshal(map[string]any{
			"wait":         true,
			"fail_fast":    true,
			"max_parallel": 2,
			"tasks": []map[string]any{
				{"name": "fast", "prompt": "fast fail", "retries": 1},
				{"name": "slow", "prompt": "slow guarded"},
				{"name": "later", "prompt": "later guarded"},
			},
		})
		result, err := (&AgentFanoutTool{}).Call(context.Background(), execCtx, input)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	fastStarted := provider.startedChan("fast fail")
	slowStarted := provider.startedChan("slow guarded")
	laterStarted := provider.startedChan("later guarded")

	select {
	case <-fastStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("fast task did not start")
	}
	select {
	case <-slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("slow task did not start")
	}

	close(provider.releaseChan("fast fail"))

	select {
	case <-fastStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("fast task did not retry before fail_fast triggered")
	}

	select {
	case <-laterStarted:
		t.Fatal("later task started before fast retries were exhausted")
	case <-time.After(150 * time.Millisecond):
	}

	select {
	case err := <-errCh:
		t.Fatal(err)
	case result := <-resultCh:
		if !result.IsError {
			t.Fatalf("expected fail_fast workflow to return error result, got %s", result.Content)
		}
		var payload fanoutResponse
		if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
			t.Fatal(err)
		}
		byName := mapFanoutTasks(payload.Tasks)
		if byName["fast"].Status != "failed" {
			t.Fatalf("expected fast task to end failed, got %+v", byName["fast"])
		}
		if byName["fast"].Attempts != 2 {
			t.Fatalf("expected fast task to exhaust retries, got %+v", byName["fast"])
		}
		if byName["slow"].Status != "cancelled" {
			t.Fatalf("expected slow task to be cancelled after fail_fast, got %+v", byName["slow"])
		}
		if byName["later"].Status != "skipped" {
			t.Fatalf("expected later task to be skipped after fail_fast, got %+v", byName["later"])
		}
	}
}

func TestAgentFanoutToolHonorsResourceLimits(t *testing.T) {
	dir := t.TempDir()
	provider := newGatedWorkflowProvider()
	runner := engine.NewRunner(provider, engine.NewRegistry(), engine.RunnerConfig{
		Model:             "test-model",
		SystemPrompt:      "test",
		MaxTurns:          4,
		WorkingDir:        dir,
		MaxParallelAgents: 4,
	})

	execCtx := &engine.ExecutionContext{
		Runner:     runner,
		Session:    engine.NewSession(),
		WorkingDir: dir,
	}

	resultCh := make(chan engine.ToolResult, 1)
	errCh := make(chan error, 1)
	go func() {
		input, _ := json.Marshal(map[string]any{
			"wait":            true,
			"resource_limits": map[string]int{"browser": 1},
			"tasks": []map[string]any{
				{"name": "browser_a", "prompt": "browser a", "resource": "browser"},
				{"name": "browser_b", "prompt": "browser b", "resource": "browser"},
				{"name": "cpu_job", "prompt": "cpu job", "resource": "cpu"},
			},
		})
		result, err := (&AgentFanoutTool{}).Call(context.Background(), execCtx, input)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	browserAStarted := provider.startedChan("browser a")
	browserBStarted := provider.startedChan("browser b")
	cpuStarted := provider.startedChan("cpu job")

	select {
	case <-browserAStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("browser_a did not start")
	}
	select {
	case <-cpuStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("cpu job did not start")
	}
	select {
	case <-browserBStarted:
		t.Fatal("browser_b started before browser resource was released")
	case <-time.After(150 * time.Millisecond):
	}

	close(provider.releaseChan("cpu job"))
	close(provider.releaseChan("browser a"))

	select {
	case <-browserBStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("browser_b did not start after browser resource was released")
	}
	close(provider.releaseChan("browser b"))

	select {
	case err := <-errCh:
		t.Fatal(err)
	case result := <-resultCh:
		if result.IsError {
			t.Fatalf("expected success, got error result: %s", result.Content)
		}
		var payload fanoutResponse
		if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
			t.Fatal(err)
		}
		byName := mapFanoutTasks(payload.Tasks)
		if byName["browser_a"].Resource != "browser" || byName["browser_b"].Resource != "browser" {
			t.Fatalf("expected browser resource labels, got %+v", byName)
		}
	}
}

func TestAgentFanoutToolRejectsInvalidResourceLimits(t *testing.T) {
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
		"wait":            true,
		"resource_limits": map[string]int{"browser": 0},
		"tasks": []map[string]any{
			{"name": "browser_a", "prompt": "browser a", "resource": "browser"},
		},
	})

	_, err := (&AgentFanoutTool{}).Call(context.Background(), execCtx, input)
	if err == nil || !strings.Contains(err.Error(), "must be greater than 0") {
		t.Fatalf("expected invalid resource limit error, got %v", err)
	}
}

func TestAgentFanoutToolPreemptsLowerPriorityTaskForGlobalSlot(t *testing.T) {
	dir := t.TempDir()
	provider := newGatedWorkflowProvider()
	runner := engine.NewRunner(provider, engine.NewRegistry(), engine.RunnerConfig{
		Model:             "test-model",
		SystemPrompt:      "test",
		MaxTurns:          4,
		WorkingDir:        dir,
		MaxParallelAgents: 3,
	})

	execCtx := &engine.ExecutionContext{
		Runner:     runner,
		Session:    engine.NewSession(),
		WorkingDir: dir,
	}

	resultCh := make(chan engine.ToolResult, 1)
	errCh := make(chan error, 1)
	go func() {
		input, _ := json.Marshal(map[string]any{
			"wait":                   true,
			"max_parallel":           2,
			"preempt_lower_priority": true,
			"tasks": []map[string]any{
				{"name": "low_one", "prompt": "low one", "priority": 1},
				{"name": "low_two", "prompt": "low two", "priority": 2},
				{"name": "high", "prompt": "high work", "priority": 10},
			},
		})
		result, err := (&AgentFanoutTool{}).Call(context.Background(), execCtx, input)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	lowOneStarted := provider.startedChan("low one")
	lowTwoStarted := provider.startedChan("low two")
	highStarted := provider.startedChan("high work")

	select {
	case <-lowTwoStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("low_two task did not start")
	}

	select {
	case <-highStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("high task did not preempt lower-priority work")
	}

	close(provider.releaseChan("high work"))
	close(provider.releaseChan("low two"))

	select {
	case <-lowOneStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("low_one task did not restart after preemption")
	}
	close(provider.releaseChan("low one"))

	select {
	case err := <-errCh:
		t.Fatal(err)
	case result := <-resultCh:
		if result.IsError {
			t.Fatalf("expected successful preemptive workflow, got %s", result.Content)
		}
		var payload fanoutResponse
		if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
			t.Fatal(err)
		}
		byName := mapFanoutTasks(payload.Tasks)
		if byName["low_one"].Preemptions != 1 {
			t.Fatalf("expected one preemption, got %+v", byName["low_one"])
		}
		if byName["low_one"].Attempts < 1 {
			t.Fatalf("expected low_one task to run after preemption, got %+v", byName["low_one"])
		}
	}
}

func TestAgentFanoutToolPreemptsWithinSameResourcePool(t *testing.T) {
	dir := t.TempDir()
	provider := newGatedWorkflowProvider()
	runner := engine.NewRunner(provider, engine.NewRegistry(), engine.RunnerConfig{
		Model:             "test-model",
		SystemPrompt:      "test",
		MaxTurns:          4,
		WorkingDir:        dir,
		MaxParallelAgents: 4,
	})

	execCtx := &engine.ExecutionContext{
		Runner:     runner,
		Session:    engine.NewSession(),
		WorkingDir: dir,
	}

	resultCh := make(chan engine.ToolResult, 1)
	errCh := make(chan error, 1)
	go func() {
		input, _ := json.Marshal(map[string]any{
			"wait":                   true,
			"resource_limits":        map[string]int{"browser": 1},
			"preempt_lower_priority": true,
			"tasks": []map[string]any{
				{"name": "browser_low", "prompt": "browser low", "resource": "browser", "priority": 1},
				{"name": "cpu_task", "prompt": "cpu task", "resource": "cpu", "priority": 2},
				{"name": "browser_high", "prompt": "browser high", "resource": "browser", "priority": 10},
			},
		})
		result, err := (&AgentFanoutTool{}).Call(context.Background(), execCtx, input)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	browserLowStarted := provider.startedChan("browser low")
	cpuStarted := provider.startedChan("cpu task")
	browserHighStarted := provider.startedChan("browser high")

	select {
	case <-cpuStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("cpu task did not start")
	}

	select {
	case <-browserHighStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("browser high did not preempt the browser resource")
	}

	close(provider.releaseChan("browser high"))
	close(provider.releaseChan("cpu task"))

	select {
	case <-browserLowStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("browser low did not restart after browser high completed")
	}
	close(provider.releaseChan("browser low"))

	select {
	case err := <-errCh:
		t.Fatal(err)
	case result := <-resultCh:
		if result.IsError {
			t.Fatalf("expected successful resource preemption workflow, got %s", result.Content)
		}
		var payload fanoutResponse
		if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
			t.Fatal(err)
		}
		byName := mapFanoutTasks(payload.Tasks)
		if byName["browser_low"].Preemptions != 1 {
			t.Fatalf("expected browser_low to be preempted once, got %+v", byName["browser_low"])
		}
		if byName["cpu_task"].Preemptions != 0 {
			t.Fatalf("expected cpu task to avoid browser preemption, got %+v", byName["cpu_task"])
		}
	}
}

func TestWorkflowManagerImportInterruptsRunningWorkflow(t *testing.T) {
	manager := NewWorkflowManager()
	now := time.Now().UTC()
	completed := now.Add(-time.Minute)

	err := manager.ImportWorkflows([]WorkflowSnapshot{
		{
			ID:        "workflow_running",
			Status:    WorkflowRunning,
			CreatedAt: now.Add(-2 * time.Minute),
			UpdatedAt: now.Add(-time.Minute),
			Options: WorkflowOptions{
				MaxParallel: 1,
			},
			Tasks: []WorkflowTaskState{
				{
					Input:    agentTaskInput{Name: "plan", Prompt: "plan work"},
					Started:  true,
					Attempts: 1,
					AgentID:  "agent_plan",
					Result: fanoutTaskResult{
						Name:      "plan",
						Prompt:    "plan work",
						AgentID:   "agent_plan",
						Attempts:  1,
						Status:    string(engine.AgentRunning),
						DependsOn: nil,
					},
					Snapshot: &engine.AgentSnapshot{
						ID:        "agent_plan",
						Name:      "plan",
						Status:    engine.AgentRunning,
						Prompt:    "plan work",
						StartedAt: now.Add(-30 * time.Second),
					},
				},
				{
					Input:   agentTaskInput{Name: "done", Prompt: "done work"},
					Done:    true,
					AgentID: "agent_done",
					Result: fanoutTaskResult{
						Name:      "done",
						Prompt:    "done work",
						Status:    string(engine.AgentIdle),
						Result:    "reply:done work",
						AgentID:   "agent_done",
						Attempts:  1,
						DependsOn: nil,
					},
					Snapshot: &engine.AgentSnapshot{
						ID:          "agent_done",
						Name:        "done",
						Status:      engine.AgentIdle,
						Prompt:      "done work",
						Result:      "reply:done work",
						StartedAt:   now.Add(-2 * time.Minute),
						CompletedAt: &completed,
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, ok := manager.GetWorkflow("workflow_running")
	if !ok {
		t.Fatal("expected imported workflow")
	}
	if snapshot.Status != WorkflowInterrupted {
		t.Fatalf("expected interrupted workflow, got %+v", snapshot)
	}
	if snapshot.Tasks[0].Started {
		t.Fatalf("expected running task to reset for resume, got %+v", snapshot.Tasks[0])
	}
	if snapshot.Tasks[0].AgentID != "" || snapshot.Tasks[0].Result.AgentID != "" {
		t.Fatalf("expected running task agent ids to clear, got %+v", snapshot.Tasks[0])
	}
	if snapshot.Tasks[0].Attempts != 1 || snapshot.Tasks[0].Result.Attempts != 1 {
		t.Fatalf("expected attempt count to be preserved, got %+v", snapshot.Tasks[0])
	}
	if !snapshot.Tasks[1].Done || snapshot.Tasks[1].Result.Status != string(engine.AgentIdle) {
		t.Fatalf("expected completed task to stay completed, got %+v", snapshot.Tasks[1])
	}
}

func TestWorkflowResumeToolResumesInterruptedWorkflow(t *testing.T) {
	dir := t.TempDir()
	provider := newGatedWorkflowProvider()
	manager := NewWorkflowManager()
	runner := engine.NewRunner(provider, engine.NewRegistry(), engine.RunnerConfig{
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

	fanoutTool := &AgentFanoutTool{Manager: manager}
	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		input, _ := json.Marshal(map[string]any{
			"wait":         true,
			"max_parallel": 1,
			"tasks": []map[string]any{
				{"name": "one", "prompt": "one"},
				{"name": "two", "prompt": "two"},
			},
		})
		_, err := fanoutTool.Call(runCtx, execCtx, input)
		errCh <- err
	}()

	select {
	case <-provider.startedChan("one"):
	case <-time.After(2 * time.Second):
		t.Fatal("first task did not start")
	}
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected workflow cancellation, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("workflow did not stop after cancellation")
	}

	exported := manager.ExportWorkflows()
	if len(exported) != 1 {
		t.Fatalf("expected one exported workflow, got %d", len(exported))
	}
	if exported[0].Status != WorkflowInterrupted {
		t.Fatalf("expected interrupted export, got %+v", exported[0])
	}

	resumeManager := NewWorkflowManager()
	if err := resumeManager.ImportWorkflows(exported); err != nil {
		t.Fatal(err)
	}
	resumeRunner := engine.NewRunner(&toolPromptProvider{}, engine.NewRegistry(), engine.RunnerConfig{
		Model:             "test-model",
		SystemPrompt:      "test",
		MaxTurns:          4,
		WorkingDir:        dir,
		MaxParallelAgents: 1,
	})
	resumeTool := &WorkflowResumeTool{Manager: resumeManager}

	input, _ := json.Marshal(map[string]any{
		"workflow_id": exported[0].ID,
	})
	result, err := resumeTool.Call(context.Background(), &engine.ExecutionContext{
		Runner:     resumeRunner,
		Session:    engine.NewSession(),
		WorkingDir: dir,
	}, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected resumed workflow success, got %s", result.Content)
	}

	var payload fanoutResponse
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Workflow.Status != WorkflowCompleted {
		t.Fatalf("expected completed workflow, got %+v", payload.Workflow)
	}
	byName := mapFanoutTasks(payload.Tasks)
	if byName["one"].Status != string(engine.AgentIdle) || byName["two"].Status != string(engine.AgentIdle) {
		t.Fatalf("expected resumed tasks to finish successfully, got %+v", byName)
	}
	if byName["one"].Attempts != 2 {
		t.Fatalf("expected resumed first task to preserve and increment attempts, got %+v", byName["one"])
	}
}

func TestWorkflowResumeToolCanRerunOnlyFailedTasksAndWriteArtifacts(t *testing.T) {
	dir := t.TempDir()
	manager := NewWorkflowManager()
	manager.SetArtifactRoot(filepath.Join(dir, "artifacts"))

	failRunner := engine.NewRunner(&conditionalToolPromptProvider{}, engine.NewRegistry(), engine.RunnerConfig{
		Model:             "test-model",
		SystemPrompt:      "test",
		MaxTurns:          4,
		WorkingDir:        dir,
		MaxParallelAgents: 2,
	})
	fanoutTool := &AgentFanoutTool{Manager: manager}
	execCtx := &engine.ExecutionContext{
		Runner:     failRunner,
		Session:    engine.NewSession(),
		WorkingDir: dir,
	}

	input, _ := json.Marshal(map[string]any{
		"wait": true,
		"tasks": []map[string]any{
			{"name": "one", "prompt": "fail one"},
			{"name": "two", "prompt": "two follows one", "depends_on": []string{"one"}},
		},
	})
	result, err := fanoutTool.Call(context.Background(), execCtx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("expected initial workflow failure payload, got %s", result.Content)
	}

	var initial fanoutResponse
	if err := json.Unmarshal([]byte(result.Content), &initial); err != nil {
		t.Fatal(err)
	}
	initialByName := mapFanoutTasks(initial.Tasks)
	if initialByName["one"].Status != string(engine.AgentFailed) || initialByName["two"].Status != "skipped" {
		t.Fatalf("unexpected initial workflow task status: %+v", initialByName)
	}
	if initialByName["one"].ArtifactFile == "" || initialByName["two"].ArtifactFile == "" {
		t.Fatalf("expected artifact files for persisted workflow tasks, got %+v", initialByName)
	}
	if _, err := os.Stat(initialByName["one"].ArtifactFile); err != nil {
		t.Fatalf("expected task artifact file to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "artifacts", initial.Workflow.ID, "manifest.json")); err != nil {
		t.Fatalf("expected workflow manifest to exist: %v", err)
	}

	tasksTool := &WorkflowTasksTool{Manager: manager}
	taskQuery, _ := json.Marshal(map[string]any{
		"workflow_id": initial.Workflow.ID,
		"status":      "failed",
	})
	taskResult, err := tasksTool.Call(context.Background(), execCtx, taskQuery)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(taskResult.Content, `"one"`) {
		t.Fatalf("expected filtered workflow task payload, got %s", taskResult.Content)
	}

	resumeRunner := engine.NewRunner(&toolPromptProvider{}, engine.NewRegistry(), engine.RunnerConfig{
		Model:             "test-model",
		SystemPrompt:      "test",
		MaxTurns:          4,
		WorkingDir:        dir,
		MaxParallelAgents: 2,
	})
	resumeTool := &WorkflowResumeTool{Manager: manager}
	resumeInput, _ := json.Marshal(map[string]any{
		"workflow_id": initial.Workflow.ID,
		"only_failed": true,
	})
	resumed, err := resumeTool.Call(context.Background(), &engine.ExecutionContext{
		Runner:     resumeRunner,
		Session:    engine.NewSession(),
		WorkingDir: dir,
	}, resumeInput)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.IsError {
		t.Fatalf("expected failed-only resume to succeed, got %s", resumed.Content)
	}

	var payload fanoutResponse
	if err := json.Unmarshal([]byte(resumed.Content), &payload); err != nil {
		t.Fatal(err)
	}
	byName := mapFanoutTasks(payload.Tasks)
	if byName["one"].Status != string(engine.AgentIdle) || byName["two"].Status != string(engine.AgentIdle) {
		t.Fatalf("expected failed tasks and downstream dependents to recover, got %+v", byName)
	}
	if byName["one"].Attempts != 2 {
		t.Fatalf("expected failed task to rerun, got %+v", byName["one"])
	}
	if byName["two"].Attempts != 1 {
		t.Fatalf("expected skipped dependent to run once after resume, got %+v", byName["two"])
	}
}

func TestWorkflowResumeToolCanRerunSelectedTasksOnCompletedWorkflow(t *testing.T) {
	dir := t.TempDir()
	manager := NewWorkflowManager()

	runner := engine.NewRunner(&toolPromptProvider{}, engine.NewRegistry(), engine.RunnerConfig{
		Model:             "test-model",
		SystemPrompt:      "test",
		MaxTurns:          4,
		WorkingDir:        dir,
		MaxParallelAgents: 2,
	})
	fanoutTool := &AgentFanoutTool{Manager: manager}
	execCtx := &engine.ExecutionContext{
		Runner:     runner,
		Session:    engine.NewSession(),
		WorkingDir: dir,
	}

	input, _ := json.Marshal(map[string]any{
		"wait":         true,
		"max_parallel": 1,
		"tasks": []map[string]any{
			{"name": "one", "prompt": "task one"},
			{"name": "two", "prompt": "task two"},
		},
	})
	result, err := fanoutTool.Call(context.Background(), execCtx, input)
	if err != nil {
		t.Fatal(err)
	}

	var initial fanoutResponse
	if err := json.Unmarshal([]byte(result.Content), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.Workflow.Status != WorkflowCompleted {
		t.Fatalf("expected completed workflow, got %+v", initial.Workflow)
	}

	resumeTool := &WorkflowResumeTool{Manager: manager}
	resumeInput, _ := json.Marshal(map[string]any{
		"workflow_id": initial.Workflow.ID,
		"task_names":  []string{"one"},
	})
	resumed, err := resumeTool.Call(context.Background(), &engine.ExecutionContext{
		Runner:     runner,
		Session:    engine.NewSession(),
		WorkingDir: dir,
	}, resumeInput)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.IsError {
		t.Fatalf("expected selective rerun to succeed, got %s", resumed.Content)
	}

	var payload fanoutResponse
	if err := json.Unmarshal([]byte(resumed.Content), &payload); err != nil {
		t.Fatal(err)
	}
	byName := mapFanoutTasks(payload.Tasks)
	if byName["one"].Attempts != 2 {
		t.Fatalf("expected selected task to rerun, got %+v", byName["one"])
	}
	if byName["two"].Attempts != 1 {
		t.Fatalf("expected unselected task to keep prior attempt count, got %+v", byName["two"])
	}
}

func mapFanoutTasks(tasks []fanoutTaskPayload) map[string]fanoutTaskPayload {
	byName := make(map[string]fanoutTaskPayload, len(tasks))
	for _, task := range tasks {
		byName[task.Name] = task
	}
	return byName
}
