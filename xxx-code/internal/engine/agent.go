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
	AgentRunning   AgentStatus = "running"
	AgentIdle      AgentStatus = "idle"
	AgentFailed    AgentStatus = "failed"
	AgentCancelled AgentStatus = "cancelled"
)

type AgentSnapshot struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	ParentID    string      `json:"parent_id,omitempty"`
	Children    []string    `json:"children,omitempty"`
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
	cancel   context.CancelFunc
	runSeq   uint64
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
	parentID := ""
	if parent != nil {
		depth = parent.Depth + 1
		parentID = parent.AgentID
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
			ParentID:   parentID,
			Status:     AgentRunning,
			Prompt:     request.Prompt,
			Background: request.Background,
			StartedAt:  time.Now(),
		},
		session: childSession,
		runner:  childRunner,
		depth:   depth,
		done:    make(chan struct{}),
		runSeq:  1,
	}

	runCtx, cancel := context.WithCancel(context.Background())
	agent.cancel = cancel

	r.agentState.mu.Lock()
	r.agentState.agents[id] = agent
	if parentID != "" {
		if parentAgent, ok := r.agentState.agents[parentID]; ok {
			parentAgent.snapshot.Children = appendUnique(parentAgent.snapshot.Children, id)
		}
	}
	r.agentState.mu.Unlock()

	r.emit(Event{
		Kind:      EventAgentSpawned,
		AgentID:   id,
		AgentName: name,
		Text:      request.Prompt,
	})

	snapshot := cloneAgentSnapshot(agent.snapshot)
	r.runManagedAgent(agent, request.Prompt, agent.done, runCtx, agent.runSeq)

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

	agent.runSeq++
	agent.snapshot.Status = AgentRunning
	agent.snapshot.Prompt = prompt
	agent.snapshot.Result = ""
	agent.snapshot.Error = ""
	agent.snapshot.Background = background
	agent.snapshot.CompletedAt = nil
	agent.done = make(chan struct{})

	runCtx, cancel := context.WithCancel(context.Background())
	agent.cancel = cancel

	snapshot := cloneAgentSnapshot(agent.snapshot)
	done := agent.done
	runSeq := agent.runSeq
	r.agentState.mu.Unlock()

	r.runManagedAgent(agent, prompt, done, runCtx, runSeq)

	if background {
		return snapshot, nil
	}
	return r.WaitAgent(ctx, id)
}

func (r *Runner) CancelAgent(ctx context.Context, id string, recursive bool) (AgentSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	targets, err := r.cancelTargets(id, recursive)
	if err != nil {
		return AgentSnapshot{}, err
	}

	type pendingCancel struct {
		id      string
		running bool
		cancel  context.CancelFunc
	}

	cancellations := make([]pendingCancel, 0, len(targets))
	r.agentState.mu.RLock()
	for _, targetID := range targets {
		agent, ok := r.agentState.agents[targetID]
		if !ok {
			r.agentState.mu.RUnlock()
			return AgentSnapshot{}, errors.New("agent not found")
		}
		cancellations = append(cancellations, pendingCancel{
			id:      targetID,
			running: agent.snapshot.Status == AgentRunning,
			cancel:  agent.cancel,
		})
	}
	r.agentState.mu.RUnlock()

	for _, item := range cancellations {
		if item.running && item.cancel != nil {
			item.cancel()
		}
	}

	var snapshot AgentSnapshot
	for _, targetID := range targets {
		current, err := r.WaitAgent(ctx, targetID)
		if err != nil {
			return AgentSnapshot{}, err
		}
		if targetID == id {
			snapshot = current
		}
	}
	return snapshot, nil
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
	snapshot := cloneAgentSnapshot(agent.snapshot)
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
		return cloneAgentSnapshot(r.agentState.agents[id].snapshot), nil
	}
}

func (r *Runner) ListAgents() []AgentSnapshot {
	r.agentState.mu.RLock()
	defer r.agentState.mu.RUnlock()

	snapshots := make([]AgentSnapshot, 0, len(r.agentState.agents))
	for _, agent := range r.agentState.agents {
		snapshots = append(snapshots, cloneAgentSnapshot(agent.snapshot))
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
			Snapshot:   cloneAgentSnapshot(agent.snapshot),
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

		snapshot := cloneAgentSnapshot(state.Snapshot)
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

func (r *Runner) runManagedAgent(agent *managedAgent, prompt string, done chan struct{}, runCtx context.Context, runSeq uint64) {
	go func() {
		defer close(done)

		result, runErr := agent.runner.runTurn(runCtx, &ExecutionContext{
			Runner:     agent.runner,
			Session:    agent.session,
			WorkingDir: agent.runner.config.WorkingDir,
			AgentID:    agent.snapshot.ID,
			AgentName:  agent.snapshot.Name,
			Depth:      agent.depth,
		}, prompt)

		finished := time.Now()

		r.agentState.mu.Lock()
		if agent.runSeq != runSeq {
			r.agentState.mu.Unlock()
			return
		}

		switch {
		case errors.Is(runErr, context.Canceled):
			agent.snapshot.Status = AgentCancelled
			agent.snapshot.Error = "agent run cancelled"
			agent.snapshot.Result = ""
		case runErr != nil:
			agent.snapshot.Status = AgentFailed
			agent.snapshot.Error = runErr.Error()
			agent.snapshot.Result = ""
		default:
			agent.snapshot.Status = AgentIdle
			agent.snapshot.Error = ""
			agent.snapshot.Result = result.FinalText
		}

		agent.snapshot.CompletedAt = &finished
		agent.cancel = nil
		snapshot := cloneAgentSnapshot(agent.snapshot)
		r.agentState.mu.Unlock()

		event := Event{
			Kind:      EventAgentCompleted,
			AgentID:   snapshot.ID,
			AgentName: snapshot.Name,
			Text:      snapshot.Result,
		}
		if snapshot.Status == AgentCancelled {
			event.Kind = EventAgentCancelled
			event.Text = snapshot.Error
		} else if snapshot.Status == AgentFailed {
			event.Text = snapshot.Error
		}
		r.emit(event)
	}()
}

func (r *Runner) cancelTargets(id string, recursive bool) ([]string, error) {
	r.agentState.mu.RLock()
	defer r.agentState.mu.RUnlock()

	if _, ok := r.agentState.agents[id]; !ok {
		return nil, errors.New("agent not found")
	}

	visited := make(map[string]struct{})
	targets := make([]string, 0, 4)
	var visit func(string) error
	visit = func(currentID string) error {
		if _, seen := visited[currentID]; seen {
			return nil
		}
		visited[currentID] = struct{}{}

		agent, ok := r.agentState.agents[currentID]
		if !ok {
			return errors.New("agent not found")
		}

		if recursive {
			for _, childID := range agent.snapshot.Children {
				if err := visit(childID); err != nil {
					return err
				}
			}
		}

		targets = append(targets, currentID)
		return nil
	}

	if err := visit(id); err != nil {
		return nil, err
	}
	return targets, nil
}

func cloneAgentSnapshot(snapshot AgentSnapshot) AgentSnapshot {
	if len(snapshot.Children) > 0 {
		snapshot.Children = append([]string(nil), snapshot.Children...)
	}
	return snapshot
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
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
