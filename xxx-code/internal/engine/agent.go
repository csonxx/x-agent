package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

type AgentStatus string

const (
	AgentRunning AgentStatus = "running"
	AgentIdle    AgentStatus = "idle"
	AgentFailed  AgentStatus = "failed"
)

type AgentSnapshot struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Status      AgentStatus `json:"status"`
	Prompt      string      `json:"prompt"`
	Result      string      `json:"result,omitempty"`
	Error       string      `json:"error,omitempty"`
	Background  bool        `json:"background"`
	StartedAt   time.Time   `json:"started_at"`
	CompletedAt *time.Time  `json:"completed_at,omitempty"`
}

type PersistedAgentState struct {
	Snapshot   AgentSnapshot `json:"snapshot"`
	Session    []Message     `json:"session"`
	Depth      int           `json:"depth"`
	Model      string        `json:"model,omitempty"`
	MaxTurns   int           `json:"max_turns,omitempty"`
	WorkingDir string        `json:"working_dir,omitempty"`
}

type SpawnRequest struct {
	Name           string
	Prompt         string
	Background     bool
	Model          string
	MaxTurns       int
	WorkingDir     string
	InheritHistory bool
}

type agentState struct {
	mu     sync.RWMutex
	agents map[string]*managedAgent
}

type managedAgent struct {
	snapshot AgentSnapshot
	session  *Session
	runner   *Runner
	depth    int
	done     chan struct{}
}

func newAgentID() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "agent_" + hex.EncodeToString(buf), nil
}

func closedDone() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (r *Runner) SpawnAgent(parent *ExecutionContext, request SpawnRequest) (AgentSnapshot, error) {
	if parent != nil && parent.Depth >= r.config.MaxAgentDepth {
		return AgentSnapshot{}, errors.New("maximum agent depth reached")
	}

	id, err := newAgentID()
	if err != nil {
		return AgentSnapshot{}, err
	}

	name := request.Name
	if name == "" {
		name = id
	}

	depth := 0
	if parent != nil {
		depth = parent.Depth + 1
	}

	childRunner := r.cloneForAgent(request)
	childSession := NewSession()
	if request.InheritHistory && parent != nil && parent.Session != nil {
		childSession.Replace(parent.Session.Snapshot())
	}

	agent := &managedAgent{
		snapshot: AgentSnapshot{
			ID:         id,
			Name:       name,
			Status:     AgentRunning,
			Prompt:     request.Prompt,
			Background: request.Background,
			StartedAt:  time.Now(),
		},
		session: childSession,
		runner:  childRunner,
		depth:   depth,
		done:    make(chan struct{}),
	}

	r.agentState.mu.Lock()
	r.agentState.agents[id] = agent
	r.agentState.mu.Unlock()

	r.emit(Event{
		Kind:      EventAgentSpawned,
		AgentID:   id,
		AgentName: name,
		Text:      request.Prompt,
	})

	snapshot := agent.snapshot
	r.runManagedAgent(agent, request.Prompt, agent.done)

	if request.Background {
		return snapshot, nil
	}

	return r.WaitAgent(context.Background(), id)
}

func (r *Runner) SendAgent(ctx context.Context, id, prompt string, background bool) (AgentSnapshot, error) {
	if strings.TrimSpace(prompt) == "" {
		return AgentSnapshot{}, errors.New("prompt is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	r.agentState.mu.Lock()
	agent, ok := r.agentState.agents[id]
	if !ok {
		r.agentState.mu.Unlock()
		return AgentSnapshot{}, errors.New("agent not found")
	}
	if agent.snapshot.Status == AgentRunning {
		r.agentState.mu.Unlock()
		return AgentSnapshot{}, errors.New("agent is already running")
	}

	agent.snapshot.Status = AgentRunning
	agent.snapshot.Prompt = prompt
	agent.snapshot.Result = ""
	agent.snapshot.Error = ""
	agent.snapshot.Background = background
	agent.snapshot.CompletedAt = nil
	agent.done = make(chan struct{})

	snapshot := agent.snapshot
	done := agent.done
	r.agentState.mu.Unlock()

	r.runManagedAgent(agent, prompt, done)

	if background {
		return snapshot, nil
	}
	return r.WaitAgent(ctx, id)
}

func (r *Runner) WaitAgent(ctx context.Context, id string) (AgentSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	r.agentState.mu.RLock()
	agent, ok := r.agentState.agents[id]
	if !ok {
		r.agentState.mu.RUnlock()
		return AgentSnapshot{}, errors.New("agent not found")
	}
	snapshot := agent.snapshot
	done := agent.done
	r.agentState.mu.RUnlock()

	if snapshot.Status != AgentRunning {
		return snapshot, nil
	}

	select {
	case <-ctx.Done():
		return AgentSnapshot{}, ctx.Err()
	case <-done:
		r.agentState.mu.RLock()
		defer r.agentState.mu.RUnlock()
		return r.agentState.agents[id].snapshot, nil
	}
}

func (r *Runner) ListAgents() []AgentSnapshot {
	r.agentState.mu.RLock()
	defer r.agentState.mu.RUnlock()

	snapshots := make([]AgentSnapshot, 0, len(r.agentState.agents))
	for _, agent := range r.agentState.agents {
		snapshots = append(snapshots, agent.snapshot)
	}
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].StartedAt.Before(snapshots[j].StartedAt)
	})
	return snapshots
}

func (r *Runner) ExportAgents() []PersistedAgentState {
	r.agentState.mu.RLock()
	defer r.agentState.mu.RUnlock()

	states := make([]PersistedAgentState, 0, len(r.agentState.agents))
	for _, agent := range r.agentState.agents {
		states = append(states, PersistedAgentState{
			Snapshot:   agent.snapshot,
			Session:    agent.session.Snapshot(),
			Depth:      agent.depth,
			Model:      agent.runner.config.Model,
			MaxTurns:   agent.runner.config.MaxTurns,
			WorkingDir: agent.runner.config.WorkingDir,
		})
	}
	sort.Slice(states, func(i, j int) bool {
		return states[i].Snapshot.StartedAt.Before(states[j].Snapshot.StartedAt)
	})
	return states
}

func (r *Runner) ImportAgents(states []PersistedAgentState) error {
	r.agentState.mu.Lock()
	defer r.agentState.mu.Unlock()

	for _, state := range states {
		if strings.TrimSpace(state.Snapshot.ID) == "" {
			return errors.New("persisted agent is missing an id")
		}
		if _, exists := r.agentState.agents[state.Snapshot.ID]; exists {
			return errors.New("duplicate agent id: " + state.Snapshot.ID)
		}

		snapshot := state.Snapshot
		if snapshot.Status == "" {
			snapshot.Status = AgentIdle
		}
		if snapshot.Status == AgentRunning {
			snapshot.Status = AgentFailed
			if strings.TrimSpace(snapshot.Error) == "" {
				snapshot.Error = "agent was running when the session was saved and must be restarted"
			}
			finished := time.Now()
			snapshot.CompletedAt = &finished
		}

		childRunner := r.cloneForAgent(SpawnRequest{
			Model:      state.Model,
			MaxTurns:   state.MaxTurns,
			WorkingDir: state.WorkingDir,
		})

		r.agentState.agents[snapshot.ID] = &managedAgent{
			snapshot: snapshot,
			session:  NewSession(state.Session...),
			runner:   childRunner,
			depth:    state.Depth,
			done:     closedDone(),
		}
	}

	return nil
}

func (r *Runner) runManagedAgent(agent *managedAgent, prompt string, done chan struct{}) {
	go func() {
		defer close(done)

		result, runErr := agent.runner.runTurn(context.Background(), &ExecutionContext{
			Runner:     agent.runner,
			Session:    agent.session,
			WorkingDir: agent.runner.config.WorkingDir,
			AgentID:    agent.snapshot.ID,
			AgentName:  agent.snapshot.Name,
			Depth:      agent.depth,
		}, prompt)

		finished := time.Now()

		r.agentState.mu.Lock()
		if runErr != nil {
			agent.snapshot.Status = AgentFailed
			agent.snapshot.Error = runErr.Error()
			agent.snapshot.Result = ""
		} else {
			agent.snapshot.Status = AgentIdle
			agent.snapshot.Error = ""
			agent.snapshot.Result = result.FinalText
		}
		agent.snapshot.CompletedAt = &finished
		snapshot := agent.snapshot
		r.agentState.mu.Unlock()

		text := snapshot.Result
		if snapshot.Error != "" {
			text = snapshot.Error
		}
		r.emit(Event{
			Kind:      EventAgentCompleted,
			AgentID:   snapshot.ID,
			AgentName: snapshot.Name,
			Text:      text,
		})
	}()
}

func (r *Runner) cloneForAgent(request SpawnRequest) *Runner {
	cfg := r.config
	if request.Model != "" {
		cfg.Model = request.Model
	}
	if request.MaxTurns > 0 {
		cfg.MaxTurns = request.MaxTurns
	}
	if request.WorkingDir != "" {
		cfg.WorkingDir = request.WorkingDir
	}

	return &Runner{
		provider:   r.provider,
		registry:   r.registry,
		config:     cfg,
		agentState: r.agentState,
	}
}
