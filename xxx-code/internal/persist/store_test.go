package persist

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
)

func TestSaveAndLoadSessionState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")

	session := engine.NewSession(
		engine.NewTextMessage(engine.RoleUser, "hello"),
		engine.NewTextMessage(engine.RoleAssistant, "world"),
	)
	runner := engine.NewRunner(nil, engine.NewRegistry(), engine.RunnerConfig{
		Model:        "test-model",
		SystemPrompt: "test",
		MaxTurns:     4,
		WorkingDir:   "/tmp/work",
	})

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

	if err := Save(path, session, runner); err != nil {
		t.Fatal(err)
	}

	state, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != StateVersion {
		t.Fatalf("unexpected version: %d", state.Version)
	}
	if len(state.Main) != 2 {
		t.Fatalf("expected 2 main messages, got %d", len(state.Main))
	}
	if len(state.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(state.Agents))
	}
	if state.Agents[0].Snapshot.Name != "worker" {
		t.Fatalf("unexpected agent name: %q", state.Agents[0].Snapshot.Name)
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
	if len(list) != 1 {
		t.Fatalf("expected restored agent, got %d", len(list))
	}
	if list[0].Status != engine.AgentIdle {
		t.Fatalf("expected restored idle agent, got %s", list[0].Status)
	}
	if list[0].Result != "done" {
		t.Fatalf("unexpected restored result: %q", list[0].Result)
	}
}
