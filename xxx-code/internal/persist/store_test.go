package persist

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	"github.com/caowenhua/x-agent/xxx-code/internal/tools"
)

type persistPromptProvider struct{}

func (p *persistPromptProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
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

func TestSaveAndLoadSessionState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")

	session := engine.NewSession(
		engine.NewTextMessage(engine.RoleUser, "hello"),
		engine.NewTextMessage(engine.RoleAssistant, "world"),
	)
	runner := engine.NewRunner(&persistPromptProvider{}, engine.NewRegistry(), engine.RunnerConfig{
		Model:        "test-model",
		SystemPrompt: "test",
		MaxTurns:     4,
		WorkingDir:   "/tmp/work",
	})
	workflowManager := tools.NewWorkflowManager()

	finished := time.Now().UTC()
	err := runner.ImportAgents([]engine.PersistedAgentState{
		{
			Snapshot: engine.AgentSnapshot{
				ID:          "agent_test",
				Name:        "worker",
				Status:      engine.AgentIdle,
				Prompt:      "analyze",
				Result:      "done",
				Background:  false,
				StartedAt:   finished.Add(-time.Minute),
				CompletedAt: &finished,
			},
			Session: []engine.Message{
				engine.NewTextMessage(engine.RoleUser, "analyze"),
				engine.NewTextMessage(engine.RoleAssistant, "done"),
			},
			Depth:      1,
			Model:      "agent-model",
			MaxTurns:   6,
			WorkingDir: "/tmp/agent",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	fanoutTool := &tools.AgentFanoutTool{Manager: workflowManager}
	execCtx := &engine.ExecutionContext{
		Runner:     runner,
		Session:    session,
		WorkingDir: "/tmp/work",
	}
	input, _ := json.Marshal(map[string]any{
		"wait":         true,
		"max_parallel": 1,
		"tasks": []map[string]any{
			{"name": "plan", "prompt": "plan work"},
		},
	})
	if _, err := fanoutTool.Call(context.Background(), execCtx, input); err != nil {
		t.Fatal(err)
	}

	if err := Save(path, session, runner, workflowManager); err != nil {
		t.Fatal(err)
	}

	state, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != StateVersion {
		t.Fatalf("unexpected version: %d", state.Version)
	}
	if len(state.Main) < 2 {
		t.Fatalf("expected persisted main messages, got %d", len(state.Main))
	}
	if len(state.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(state.Agents))
	}
	if len(state.Workflows) != 1 {
		t.Fatalf("expected 1 workflow, got %d", len(state.Workflows))
	}
	if state.Agents[0].Snapshot.Name != "worker" {
		t.Fatalf("unexpected first agent name: %q", state.Agents[0].Snapshot.Name)
	}
	if state.Workflows[0].Status != tools.WorkflowCompleted {
		t.Fatalf("unexpected workflow status: %+v", state.Workflows[0])
	}

	restored := engine.NewRunner(nil, engine.NewRegistry(), engine.RunnerConfig{
		Model:        "test-model",
		SystemPrompt: "test",
		MaxTurns:     4,
		WorkingDir:   "/tmp/work",
	})
	if err := restored.ImportAgents(state.Agents); err != nil {
		t.Fatal(err)
	}
	list := restored.ListAgents()
	if len(list) != 2 {
		t.Fatalf("expected restored agents, got %d", len(list))
	}

	restoredWorkflows := tools.NewWorkflowManager()
	if err := restoredWorkflows.ImportWorkflows(state.Workflows); err != nil {
		t.Fatal(err)
	}
	workflows := restoredWorkflows.ListWorkflows()
	if len(workflows) != 1 {
		t.Fatalf("expected restored workflow, got %d", len(workflows))
	}
	if workflows[0].Status != tools.WorkflowCompleted {
		t.Fatalf("expected completed workflow, got %+v", workflows[0])
	}
}
