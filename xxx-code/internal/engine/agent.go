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
	AgentQueued    AgentStatus = "queued"
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
	Priority    int         `json:"priority,omitempty"`
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
	Priority       int
	Model          string
	MaxTurns       int
	WorkingDir     string
	InheritHistory bool
}

type agentState struct {
	mu           sync.RWMutex
	agents       map[string]*managedAgent
	maxParallel  int
	running      int
	nextQueueSeq uint64
}

type managedAgent struct {
	snapshot AgentSnapshot
	session  *Session
	runner   *Runner
	depth    int
	done     chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	runSeq   uint64
	queueSeq uint64
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

func (s *agentState) reserveSlotLocked() bool {
	if s == nil {
		return true
	}
	if s.maxParallel <= 0 || s.running < s.maxParallel {
		s.running++
		return true
	}
	return false
}

func (s *agentState) releaseSlotLocked() {
	if s == nil || s.running <= 0 {
		return
	}
	s.running--
}

func (s *agentState) enqueueLocked(agent *managedAgent) {
	if s == nil || agent == nil {
		return
	}
	agent.queueSeq = s.nextQueueSeq
	s.nextQueueSeq++
}

func (s *agentState) nextQueuedLocked() *managedAgent {
	if s == nil || !s.reserveSlotLocked() {
		return nil
	}

	var next *managedAgent
	for _, agent := range s.agents {
		if agent.snapshot.Status != AgentQueued {
			continue
		}
		if next == nil || shouldRunBefore(agent, next) {
			next = agent
		}
	}
	if next == nil {
		s.releaseSlotLocked()
		return nil
	}

	next.snapshot.Status = AgentRunning
	next.queueSeq = 0
	return next
}

func shouldRunBefore(candidate, current *managedAgent) bool {
	if candidate == nil {
		return false
	}
	if current == nil {
		return true
	}
	if candidate.snapshot.Priority != current.snapshot.Priority {
		return candidate.snapshot.Priority > current.snapshot.Priority
	}
	if candidate.queueSeq != current.queueSeq {
		return candidate.queueSeq < current.queueSeq
	}
	return candidate.snapshot.StartedAt.Before(current.snapshot.StartedAt)
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
			Status:     AgentQueued,
			Priority:   request.Priority,
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
	agent.ctx = runCtx
	agent.cancel = cancel

	r.agentState.mu.Lock()
	if r.agentState.reserveSlotLocked() {
		agent.snapshot.Status = AgentRunning
	} else {
		r.agentState.enqueueLocked(agent)
	}
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
	if snapshot.Status == AgentRunning {
		r.runManagedAgent(agent, request.Prompt, agent.done, runCtx, agent.runSeq)
	}

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
	if agent.snapshot.Status == AgentRunning || agent.snapshot.Status == AgentQueued {
		r.agentState.mu.Unlock()
		return AgentSnapshot{}, errors.New("agent is already in progress")
	}

	agent.runSeq++
	agent.snapshot.Status = AgentQueued
	agent.snapshot.Prompt = prompt
	agent.snapshot.Result = ""
	agent.snapshot.Error = ""
	agent.snapshot.Background = background
	agent.snapshot.CompletedAt = nil
	agent.done = make(chan struct{})
	agent.queueSeq = 0

	runCtx, cancel := context.WithCancel(context.Background())
	agent.ctx = runCtx
	agent.cancel = cancel
	if r.agentState.reserveSlotLocked() {
		agent.snapshot.Status = AgentRunning
	} else {
		r.agentState.enqueueLocked(agent)
	}

	snapshot := cloneAgentSnapshot(agent.snapshot)
	done := agent.done
	runSeq := agent.runSeq
	r.agentState.mu.Unlock()

	if snapshot.Status == AgentRunning {
		r.runManagedAgent(agent, prompt, done, runCtx, runSeq)
	}

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
		id     string
		active bool
		cancel context.CancelFunc
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
			id:     targetID,
			active: agent.snapshot.Status == AgentRunning,
			cancel: agent.cancel,
		})
	}
	r.agentState.mu.RUnlock()

	for _, item := range cancellations {
		if err := r.cancelQueuedAgent(item.id); err != nil {
			return AgentSnapshot{}, err
		}
		if item.active && item.cancel != nil {
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

	if snapshot.Status != AgentRunning && snapshot.Status != AgentQueued {
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

func (r *Runner) WaitAgents(ctx context.Context, ids []string) ([]AgentSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	targetIDs := normalizeAgentIDs(ids)
	if len(targetIDs) == 0 {
		snapshots := r.ListAgents()
		targetIDs = make([]string, 0, len(snapshots))
		for _, snapshot := range snapshots {
			targetIDs = append(targetIDs, snapshot.ID)
		}
	}

	results := make([]AgentSnapshot, 0, len(targetIDs))
	for _, id := range targetIDs {
		snapshot, err := r.WaitAgent(ctx, id)
		if err != nil {
			return nil, err
		}
		results = append(results, snapshot)
	}
	return results, nil
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
		if snapshot.Status == AgentRunning || snapshot.Status == AgentQueued {
			snapshot.Status = AgentFailed
			if strings.TrimSpace(snapshot.Error) == "" {
				snapshot.Error = "agent was in progress when the session was saved and must be restarted"
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
		r.finishManagedAgent(agent, runSeq, result, runErr)
	}()
}

func (r *Runner) finishManagedAgent(agent *managedAgent, runSeq uint64, result RunResult, runErr error) {
	finished := time.Now()
	var nextAgent *managedAgent
	var nextDone chan struct{}
	var nextCtx context.Context
	var nextPrompt string
	var nextRunSeq uint64

	r.agentState.mu.Lock()
	r.agentState.releaseSlotLocked()
	if agent.runSeq != runSeq {
		nextAgent = r.agentState.nextQueuedLocked()
		if nextAgent != nil {
			nextDone = nextAgent.done
			nextCtx = nextAgent.ctx
			nextPrompt = nextAgent.snapshot.Prompt
			nextRunSeq = nextAgent.runSeq
		}
		r.agentState.mu.Unlock()
		if nextAgent != nil {
			r.runManagedAgent(nextAgent, nextPrompt, nextDone, nextCtx, nextRunSeq)
		}
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
	agent.queueSeq = 0
	agent.ctx = nil
	agent.cancel = nil
	snapshot := cloneAgentSnapshot(agent.snapshot)
	nextAgent = r.agentState.nextQueuedLocked()
	if nextAgent != nil {
		nextDone = nextAgent.done
		nextCtx = nextAgent.ctx
		nextPrompt = nextAgent.snapshot.Prompt
		nextRunSeq = nextAgent.runSeq
	}
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
	if nextAgent != nil {
		r.runManagedAgent(nextAgent, nextPrompt, nextDone, nextCtx, nextRunSeq)
	}
}

func (r *Runner) cancelQueuedAgent(id string) error {
	finished := time.Now()

	r.agentState.mu.Lock()
	agent, ok := r.agentState.agents[id]
	if !ok {
		r.agentState.mu.Unlock()
		return errors.New("agent not found")
	}
	if agent.snapshot.Status != AgentQueued {
		r.agentState.mu.Unlock()
		return nil
	}

	agent.snapshot.Status = AgentCancelled
	agent.snapshot.Error = "agent run cancelled"
	agent.snapshot.Result = ""
	agent.snapshot.CompletedAt = &finished
	agent.queueSeq = 0
	agent.ctx = nil
	if agent.cancel != nil {
		agent.cancel()
	}
	agent.cancel = nil
	done := agent.done
	snapshot := cloneAgentSnapshot(agent.snapshot)
	r.agentState.mu.Unlock()

	close(done)
	r.emit(Event{
		Kind:      EventAgentCancelled,
		AgentID:   snapshot.ID,
		AgentName: snapshot.Name,
		Text:      snapshot.Error,
	})
	return nil
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

func normalizeAgentIDs(ids []string) []string {
	normalized := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	return normalized
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
