package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
)

type WorkflowStatus string

const (
	WorkflowRunning     WorkflowStatus = "running"
	WorkflowCompleted   WorkflowStatus = "completed"
	WorkflowInterrupted WorkflowStatus = "interrupted"
)

type WorkflowOptions struct {
	MaxParallel          int            `json:"max_parallel,omitempty"`
	ResourceLimits       map[string]int `json:"resource_limits,omitempty"`
	FailFast             bool           `json:"fail_fast,omitempty"`
	PreemptLowerPriority bool           `json:"preempt_lower_priority,omitempty"`
	TimeoutSeconds       int            `json:"timeout_seconds,omitempty"`
}

type WorkflowTaskState struct {
	Input            agentTaskInput        `json:"input"`
	Started          bool                  `json:"started,omitempty"`
	Done             bool                  `json:"done,omitempty"`
	Attempts         int                   `json:"attempts,omitempty"`
	Preemptions      int                   `json:"preemptions,omitempty"`
	PreemptRequested bool                  `json:"preempt_requested,omitempty"`
	AgentID          string                `json:"agent_id,omitempty"`
	Result           fanoutTaskResult      `json:"result"`
	Snapshot         *engine.AgentSnapshot `json:"snapshot,omitempty"`
}

type WorkflowSnapshot struct {
	ID            string              `json:"id"`
	ParentAgentID string              `json:"parent_agent_id,omitempty"`
	Status        WorkflowStatus      `json:"status"`
	Error         string              `json:"error,omitempty"`
	CreatedAt     time.Time           `json:"created_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
	CompletedAt   *time.Time          `json:"completed_at,omitempty"`
	Options       WorkflowOptions     `json:"options"`
	Tasks         []WorkflowTaskState `json:"tasks"`
}

type WorkflowSummary struct {
	ID            string         `json:"id"`
	ParentAgentID string         `json:"parent_agent_id,omitempty"`
	Status        WorkflowStatus `json:"status"`
	Error         string         `json:"error,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	CompletedAt   *time.Time     `json:"completed_at,omitempty"`
	TaskCount     int            `json:"task_count"`
	PendingTasks  int            `json:"pending_tasks"`
	RunningTasks  int            `json:"running_tasks"`
	FinishedTasks int            `json:"finished_tasks"`
}

type WorkflowManager struct {
	mu        sync.RWMutex
	workflows map[string]WorkflowSnapshot
	onChange  func()
}

func NewWorkflowManager() *WorkflowManager {
	return &WorkflowManager{
		workflows: make(map[string]WorkflowSnapshot),
	}
}

func (m *WorkflowManager) SetOnChange(fn func()) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.onChange = fn
	m.mu.Unlock()
}

func (m *WorkflowManager) CreateWorkflow(parentAgentID string, plan []*plannedFanoutTask, options WorkflowOptions) (WorkflowSnapshot, error) {
	if m == nil {
		return WorkflowSnapshot{}, errors.New("workflow manager is not configured")
	}
	id, err := newWorkflowID()
	if err != nil {
		return WorkflowSnapshot{}, err
	}

	now := time.Now().UTC()
	snapshot := WorkflowSnapshot{
		ID:            id,
		ParentAgentID: strings.TrimSpace(parentAgentID),
		Status:        WorkflowRunning,
		CreatedAt:     now,
		UpdatedAt:     now,
		Options:       cloneWorkflowOptions(options),
		Tasks:         snapshotWorkflowTasks(plan),
	}

	m.mu.Lock()
	m.workflows[id] = snapshot
	onChange := m.onChange
	m.mu.Unlock()

	if onChange != nil {
		onChange()
	}
	return cloneWorkflowSnapshot(snapshot), nil
}

func (m *WorkflowManager) BeginResume(id string) (WorkflowSnapshot, error) {
	if m == nil {
		return WorkflowSnapshot{}, errors.New("workflow manager is not configured")
	}

	m.mu.Lock()
	snapshot, ok := m.workflows[id]
	if !ok {
		m.mu.Unlock()
		return WorkflowSnapshot{}, errors.New("workflow not found")
	}
	if snapshot.Status == WorkflowRunning {
		m.mu.Unlock()
		return WorkflowSnapshot{}, errors.New("workflow is already running")
	}
	if snapshot.Status == WorkflowCompleted {
		m.mu.Unlock()
		return WorkflowSnapshot{}, errors.New("workflow is already completed")
	}

	snapshot.Status = WorkflowRunning
	snapshot.Error = ""
	snapshot.CompletedAt = nil
	snapshot.UpdatedAt = time.Now().UTC()
	m.workflows[id] = snapshot
	onChange := m.onChange
	m.mu.Unlock()

	if onChange != nil {
		onChange()
	}
	return cloneWorkflowSnapshot(snapshot), nil
}

func (m *WorkflowManager) UpdateWorkflow(id string, status WorkflowStatus, errMsg string, plan []*plannedFanoutTask, options WorkflowOptions) (WorkflowSnapshot, error) {
	if m == nil {
		return WorkflowSnapshot{}, errors.New("workflow manager is not configured")
	}

	m.mu.Lock()
	snapshot, ok := m.workflows[id]
	if !ok {
		m.mu.Unlock()
		return WorkflowSnapshot{}, errors.New("workflow not found")
	}
	snapshot.Status = status
	snapshot.Error = strings.TrimSpace(errMsg)
	snapshot.Options = cloneWorkflowOptions(options)
	if plan != nil {
		snapshot.Tasks = snapshotWorkflowTasks(plan)
	}
	snapshot.UpdatedAt = time.Now().UTC()
	if status == WorkflowCompleted {
		finished := snapshot.UpdatedAt
		snapshot.CompletedAt = &finished
	} else {
		snapshot.CompletedAt = nil
	}
	m.workflows[id] = snapshot
	onChange := m.onChange
	m.mu.Unlock()

	if onChange != nil {
		onChange()
	}
	return cloneWorkflowSnapshot(snapshot), nil
}

func (m *WorkflowManager) ListWorkflows() []WorkflowSummary {
	if m == nil {
		return nil
	}

	m.mu.RLock()
	snapshots := make([]WorkflowSnapshot, 0, len(m.workflows))
	for _, snapshot := range m.workflows {
		snapshots = append(snapshots, cloneWorkflowSnapshot(snapshot))
	}
	m.mu.RUnlock()

	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].CreatedAt.Equal(snapshots[j].CreatedAt) {
			return snapshots[i].ID < snapshots[j].ID
		}
		return snapshots[i].CreatedAt.Before(snapshots[j].CreatedAt)
	})

	summaries := make([]WorkflowSummary, 0, len(snapshots))
	for _, snapshot := range snapshots {
		summaries = append(summaries, summarizeWorkflow(snapshot))
	}
	return summaries
}

func (m *WorkflowManager) GetWorkflow(id string) (WorkflowSnapshot, bool) {
	if m == nil {
		return WorkflowSnapshot{}, false
	}
	m.mu.RLock()
	snapshot, ok := m.workflows[id]
	m.mu.RUnlock()
	if !ok {
		return WorkflowSnapshot{}, false
	}
	return cloneWorkflowSnapshot(snapshot), true
}

func (m *WorkflowManager) ExportWorkflows() []WorkflowSnapshot {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	snapshots := make([]WorkflowSnapshot, 0, len(m.workflows))
	for _, snapshot := range m.workflows {
		snapshots = append(snapshots, cloneWorkflowSnapshot(snapshot))
	}
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].CreatedAt.Equal(snapshots[j].CreatedAt) {
			return snapshots[i].ID < snapshots[j].ID
		}
		return snapshots[i].CreatedAt.Before(snapshots[j].CreatedAt)
	})
	return snapshots
}

func (m *WorkflowManager) ImportWorkflows(states []WorkflowSnapshot) error {
	if m == nil {
		return errors.New("workflow manager is not configured")
	}

	normalized := make(map[string]WorkflowSnapshot, len(states))
	for _, state := range states {
		snapshot, err := normalizeImportedWorkflow(state)
		if err != nil {
			return err
		}
		if _, exists := normalized[snapshot.ID]; exists {
			return fmt.Errorf("duplicate workflow id: %s", snapshot.ID)
		}
		normalized[snapshot.ID] = snapshot
	}

	m.mu.Lock()
	m.workflows = normalized
	m.mu.Unlock()
	return nil
}

func (m *WorkflowManager) ResumeWorkflow(ctx context.Context, id string, execCtx *engine.ExecutionContext, timeoutSeconds int) (WorkflowSnapshot, []fanoutTaskResult, []engine.AgentSnapshot, error) {
	if m == nil {
		return WorkflowSnapshot{}, nil, nil, errors.New("workflow manager is not configured")
	}
	if execCtx == nil || execCtx.Runner == nil {
		return WorkflowSnapshot{}, nil, nil, errors.New("execution context is missing a runner")
	}

	snapshot, err := m.BeginResume(id)
	if err != nil {
		return WorkflowSnapshot{}, nil, nil, err
	}
	effectiveOptions := cloneWorkflowOptions(snapshot.Options)
	if timeoutSeconds > 0 {
		effectiveOptions.TimeoutSeconds = timeoutSeconds
	}

	plan, options, err := planFromWorkflowSnapshot(snapshot, timeoutSeconds)
	if err != nil {
		interrupted, updateErr := m.UpdateWorkflow(id, WorkflowInterrupted, err.Error(), nil, effectiveOptions)
		if updateErr == nil {
			return interrupted, nil, nil, err
		}
		return WorkflowSnapshot{}, nil, nil, err
	}

	runCtx := ctx
	if timeoutSeconds > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
		defer cancel()
	}

	onPlanChange := func(current []*plannedFanoutTask) {
		_, _ = m.UpdateWorkflow(id, WorkflowRunning, "", current, effectiveOptions)
	}

	results, agentSnapshots, runErr := executeFanoutPlan(runCtx, execCtx, plan, options, onPlanChange)
	if runErr != nil {
		interrupted, updateErr := m.UpdateWorkflow(id, WorkflowInterrupted, runErr.Error(), plan, effectiveOptions)
		if updateErr == nil {
			return interrupted, nil, nil, runErr
		}
		return WorkflowSnapshot{}, nil, nil, runErr
	}

	finalSnapshot, err := m.UpdateWorkflow(id, WorkflowCompleted, "", plan, effectiveOptions)
	if err != nil {
		return WorkflowSnapshot{}, nil, nil, err
	}
	return finalSnapshot, results, agentSnapshots, nil
}

func normalizeImportedWorkflow(state WorkflowSnapshot) (WorkflowSnapshot, error) {
	if strings.TrimSpace(state.ID) == "" {
		return WorkflowSnapshot{}, errors.New("workflow is missing an id")
	}
	if len(state.Tasks) == 0 {
		return WorkflowSnapshot{}, fmt.Errorf("workflow %s has no tasks", state.ID)
	}

	now := time.Now().UTC()
	snapshot := cloneWorkflowSnapshot(state)
	if snapshot.CreatedAt.IsZero() {
		snapshot.CreatedAt = now
	}
	if snapshot.UpdatedAt.IsZero() {
		snapshot.UpdatedAt = snapshot.CreatedAt
	}
	if snapshot.Options.ResourceLimits != nil {
		snapshot.Options.ResourceLimits = cloneIntMap(snapshot.Options.ResourceLimits)
	}

	if _, _, err := planFromWorkflowSnapshot(snapshot, snapshot.Options.TimeoutSeconds); err != nil {
		return WorkflowSnapshot{}, fmt.Errorf("workflow %s: %w", snapshot.ID, err)
	}

	if snapshot.Status == "" || snapshot.Status == WorkflowRunning {
		snapshot.Status = WorkflowInterrupted
		if strings.TrimSpace(snapshot.Error) == "" {
			snapshot.Error = "workflow was in progress when the session was saved and can be resumed with workflow_resume"
		}
		snapshot.CompletedAt = nil
		snapshot.UpdatedAt = now
	}

	if snapshot.Status == WorkflowInterrupted {
		for i := range snapshot.Tasks {
			normalizeInterruptedWorkflowTask(&snapshot.Tasks[i])
		}
	}

	return snapshot, nil
}

func normalizeInterruptedWorkflowTask(task *WorkflowTaskState) {
	if task == nil {
		return
	}
	if task.Done {
		task.Result.Attempts = task.Attempts
		task.Result.Preemptions = task.Preemptions
		task.Result.AgentID = task.AgentID
		return
	}

	task.Started = false
	task.PreemptRequested = false
	task.AgentID = ""
	task.Snapshot = nil
	task.Result.ResolvedPrompt = ""
	task.Result.AgentID = ""
	task.Result.Status = ""
	task.Result.Result = ""
	task.Result.Error = ""
	task.Result.Attempts = task.Attempts
	task.Result.Preemptions = task.Preemptions
}

func planFromWorkflowSnapshot(snapshot WorkflowSnapshot, timeoutOverride int) ([]*plannedFanoutTask, fanoutExecutionOptions, error) {
	inputs := make([]agentTaskInput, 0, len(snapshot.Tasks))
	for _, task := range snapshot.Tasks {
		inputs = append(inputs, task.Input)
	}

	plan, _, err := buildFanoutPlan(inputs)
	if err != nil {
		return nil, fanoutExecutionOptions{}, err
	}
	if len(plan) != len(snapshot.Tasks) {
		return nil, fanoutExecutionOptions{}, errors.New("workflow task count does not match persisted plan")
	}

	for i := range plan {
		state := snapshot.Tasks[i]
		plan[i].started = state.Started
		plan[i].done = state.Done
		plan[i].attempts = state.Attempts
		plan[i].preemptions = state.Preemptions
		plan[i].preemptRequested = state.PreemptRequested
		plan[i].agentID = state.AgentID
		plan[i].result = cloneFanoutTaskResult(state.Result)
		if state.Snapshot != nil {
			cloned := cloneAgentSnapshotValue(*state.Snapshot)
			plan[i].snapshot = &cloned
		}
	}

	options := fanoutExecutionOptions{
		maxParallel:          snapshot.Options.MaxParallel,
		resourceLimits:       cloneIntMap(snapshot.Options.ResourceLimits),
		failFast:             snapshot.Options.FailFast,
		preemptLowerPriority: snapshot.Options.PreemptLowerPriority,
	}
	if timeoutOverride > 0 {
		snapshot.Options.TimeoutSeconds = timeoutOverride
	}
	return plan, options, nil
}

func summarizeWorkflow(snapshot WorkflowSnapshot) WorkflowSummary {
	summary := WorkflowSummary{
		ID:            snapshot.ID,
		ParentAgentID: snapshot.ParentAgentID,
		Status:        snapshot.Status,
		Error:         snapshot.Error,
		CreatedAt:     snapshot.CreatedAt,
		UpdatedAt:     snapshot.UpdatedAt,
		CompletedAt:   cloneTimePtr(snapshot.CompletedAt),
		TaskCount:     len(snapshot.Tasks),
	}
	for _, task := range snapshot.Tasks {
		switch {
		case task.Done:
			summary.FinishedTasks++
		case task.Started:
			summary.RunningTasks++
		default:
			summary.PendingTasks++
		}
	}
	return summary
}

func snapshotWorkflowTasks(plan []*plannedFanoutTask) []WorkflowTaskState {
	tasks := make([]WorkflowTaskState, 0, len(plan))
	for _, item := range plan {
		if item == nil {
			continue
		}
		task := WorkflowTaskState{
			Input:            cloneAgentTaskInput(item.task),
			Started:          item.started,
			Done:             item.done,
			Attempts:         item.attempts,
			Preemptions:      item.preemptions,
			PreemptRequested: item.preemptRequested,
			AgentID:          item.agentID,
			Result:           cloneFanoutTaskResult(item.result),
		}
		if item.snapshot != nil {
			snapshot := cloneAgentSnapshotValue(*item.snapshot)
			task.Snapshot = &snapshot
		}
		tasks = append(tasks, task)
	}
	return tasks
}

func cloneWorkflowSnapshot(snapshot WorkflowSnapshot) WorkflowSnapshot {
	cloned := WorkflowSnapshot{
		ID:            snapshot.ID,
		ParentAgentID: snapshot.ParentAgentID,
		Status:        snapshot.Status,
		Error:         snapshot.Error,
		CreatedAt:     snapshot.CreatedAt,
		UpdatedAt:     snapshot.UpdatedAt,
		CompletedAt:   cloneTimePtr(snapshot.CompletedAt),
		Options:       cloneWorkflowOptions(snapshot.Options),
	}
	if len(snapshot.Tasks) > 0 {
		cloned.Tasks = make([]WorkflowTaskState, 0, len(snapshot.Tasks))
		for _, task := range snapshot.Tasks {
			cloned.Tasks = append(cloned.Tasks, cloneWorkflowTaskState(task))
		}
	}
	return cloned
}

func cloneWorkflowTaskState(task WorkflowTaskState) WorkflowTaskState {
	cloned := WorkflowTaskState{
		Input:            cloneAgentTaskInput(task.Input),
		Started:          task.Started,
		Done:             task.Done,
		Attempts:         task.Attempts,
		Preemptions:      task.Preemptions,
		PreemptRequested: task.PreemptRequested,
		AgentID:          task.AgentID,
		Result:           cloneFanoutTaskResult(task.Result),
	}
	if task.Snapshot != nil {
		snapshot := cloneAgentSnapshotValue(*task.Snapshot)
		cloned.Snapshot = &snapshot
	}
	return cloned
}

func cloneWorkflowOptions(options WorkflowOptions) WorkflowOptions {
	return WorkflowOptions{
		MaxParallel:          options.MaxParallel,
		ResourceLimits:       cloneIntMap(options.ResourceLimits),
		FailFast:             options.FailFast,
		PreemptLowerPriority: options.PreemptLowerPriority,
		TimeoutSeconds:       options.TimeoutSeconds,
	}
}

func cloneIntMap(input map[string]int) map[string]int {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]int, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneFanoutTaskResult(result fanoutTaskResult) fanoutTaskResult {
	cloned := result
	if len(result.DependsOn) > 0 {
		cloned.DependsOn = append([]string(nil), result.DependsOn...)
	}
	return cloned
}

func cloneAgentTaskInput(input agentTaskInput) agentTaskInput {
	cloned := input
	if len(input.DependsOn) > 0 {
		cloned.DependsOn = append([]string(nil), input.DependsOn...)
	}
	return cloned
}

func cloneAgentSnapshotValue(snapshot engine.AgentSnapshot) engine.AgentSnapshot {
	cloned := snapshot
	if len(snapshot.Children) > 0 {
		cloned.Children = append([]string(nil), snapshot.Children...)
	}
	if snapshot.CompletedAt != nil {
		finished := *snapshot.CompletedAt
		cloned.CompletedAt = &finished
	}
	return cloned
}

func cloneTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func newWorkflowID() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "workflow_" + hex.EncodeToString(buf), nil
}

type WorkflowListTool struct {
	Manager *WorkflowManager
}

func (t *WorkflowListTool) Definition() engine.ToolDefinition {
	return engine.ToolDefinition{
		Name:        "workflow_list",
		Description: "List known orchestration workflows and their current resume status.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t *WorkflowListTool) Call(ctx context.Context, execCtx *engine.ExecutionContext, input json.RawMessage) (engine.ToolResult, error) {
	_ = ctx
	_ = execCtx
	if t.Manager == nil {
		return engine.ToolResult{}, errors.New("workflow persistence is not configured")
	}
	if len(input) > 0 && string(input) != "{}" && string(input) != "null" {
		var dummy map[string]any
		if err := json.Unmarshal(input, &dummy); err != nil {
			return engine.ToolResult{}, fmt.Errorf("invalid input: %w", err)
		}
	}
	return engine.ToolResult{
		Content: mustJSON(map[string]any{
			"workflows": t.Manager.ListWorkflows(),
		}),
	}, nil
}

type WorkflowGetTool struct {
	Manager *WorkflowManager
}

type workflowGetInput struct {
	WorkflowID string `json:"workflow_id"`
}

func (t *WorkflowGetTool) Definition() engine.ToolDefinition {
	return engine.ToolDefinition{
		Name:        "workflow_get",
		Description: "Return the full saved state for one workflow, including task-level progress.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workflow_id": map[string]any{
					"type":        "string",
					"description": "The workflow ID returned by agent_fanout or workflow_list.",
				},
			},
			"required": []string{"workflow_id"},
		},
	}
}

func (t *WorkflowGetTool) Call(ctx context.Context, execCtx *engine.ExecutionContext, input json.RawMessage) (engine.ToolResult, error) {
	_ = ctx
	_ = execCtx
	if t.Manager == nil {
		return engine.ToolResult{}, errors.New("workflow persistence is not configured")
	}

	var args workflowGetInput
	if err := json.Unmarshal(input, &args); err != nil {
		return engine.ToolResult{}, err
	}
	snapshot, ok := t.Manager.GetWorkflow(strings.TrimSpace(args.WorkflowID))
	if !ok {
		return engine.ToolResult{}, errors.New("workflow not found")
	}
	return engine.ToolResult{
		Content: mustJSON(map[string]any{
			"workflow": snapshot,
		}),
	}, nil
}

type WorkflowResumeTool struct {
	Manager *WorkflowManager
}

type workflowResumeInput struct {
	WorkflowID     string `json:"workflow_id"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

func (t *WorkflowResumeTool) Definition() engine.ToolDefinition {
	return engine.ToolDefinition{
		Name:        "workflow_resume",
		Description: "Resume an interrupted workflow from its last persisted task graph state.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workflow_id": map[string]any{
					"type":        "string",
					"description": "The interrupted workflow ID to resume.",
				},
				"timeout_seconds": map[string]any{
					"type":        "integer",
					"description": "Optional timeout for the resumed workflow run.",
				},
			},
			"required": []string{"workflow_id"},
		},
	}
}

func (t *WorkflowResumeTool) Call(ctx context.Context, execCtx *engine.ExecutionContext, input json.RawMessage) (engine.ToolResult, error) {
	if t.Manager == nil {
		return engine.ToolResult{}, errors.New("workflow persistence is not configured")
	}
	var args workflowResumeInput
	if err := json.Unmarshal(input, &args); err != nil {
		return engine.ToolResult{}, err
	}

	snapshot, results, agentSnapshots, err := t.Manager.ResumeWorkflow(ctx, strings.TrimSpace(args.WorkflowID), execCtx, args.TimeoutSeconds)
	if err != nil {
		return engine.ToolResult{}, err
	}
	return engine.ToolResult{
		Content: mustJSON(map[string]any{
			"workflow": snapshot,
			"agents":   agentSnapshots,
			"tasks":    results,
		}),
		IsError: hasFanoutTaskErrors(results),
	}, nil
}
