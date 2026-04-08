package persist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	"github.com/caowenhua/x-agent/xxx-code/internal/tools"
)

const StateVersion = 1

type SessionState struct {
	Version   int                          `json:"version"`
	SavedAt   time.Time                    `json:"saved_at"`
	Main      []engine.Message             `json:"main"`
	Agents    []engine.PersistedAgentState `json:"agents,omitempty"`
	Workflows []tools.WorkflowSnapshot     `json:"workflows,omitempty"`
}

func Save(path string, session *engine.Session, runner *engine.Runner, workflows *tools.WorkflowManager) error {
	state := SessionState{
		Version:   StateVersion,
		SavedAt:   time.Now().UTC(),
		Main:      session.Snapshot(),
		Agents:    runner.ExportAgents(),
		Workflows: workflows.ExportWorkflows(),
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func Load(path string) (SessionState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SessionState{}, err
	}

	var state SessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return SessionState{}, err
	}
	return state, nil
}
