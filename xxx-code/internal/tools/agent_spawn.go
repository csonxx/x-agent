package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
)

type AgentSpawnTool struct{}

type agentSpawnInput struct {
	Name           string `json:"name,omitempty"`
	Prompt         string `json:"prompt"`
	Background     bool   `json:"background,omitempty"`
	Priority       int    `json:"priority,omitempty"`
	Model          string `json:"model,omitempty"`
	MaxTurns       int    `json:"max_turns,omitempty"`
	InheritHistory bool   `json:"inherit_history,omitempty"`
	WorkingDir     string `json:"working_dir,omitempty"`
}

func (t *AgentSpawnTool) Definition() engine.ToolDefinition {
	return engine.ToolDefinition{
		Name:        "agent_spawn",
		Description: "Spawn a sub-agent. Use background=true to let it run asynchronously, then inspect it with agent_list, agent_wait, or continue it with agent_send.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Optional short name for the sub-agent.",
				},
				"prompt": map[string]any{
					"type":        "string",
					"description": "The task for the sub-agent.",
				},
				"background": map[string]any{
					"type":        "boolean",
					"description": "Run asynchronously if true.",
				},
				"priority": map[string]any{
					"type":        "integer",
					"description": "Optional scheduling priority. Higher values run first when agents are queued.",
				},
				"model": map[string]any{
					"type":        "string",
					"description": "Optional model override.",
				},
				"max_turns": map[string]any{
					"type":        "integer",
					"description": "Optional max turn override.",
				},
				"inherit_history": map[string]any{
					"type":        "boolean",
					"description": "If true, clone the current conversation into the child agent.",
				},
				"working_dir": map[string]any{
					"type":        "string",
					"description": "Optional working directory for the sub-agent.",
				},
			},
			"required": []string{"prompt"},
		},
	}
}

func (t *AgentSpawnTool) Call(ctx context.Context, execCtx *engine.ExecutionContext, input json.RawMessage) (engine.ToolResult, error) {
	_ = ctx

	var args agentSpawnInput
	if err := json.Unmarshal(input, &args); err != nil {
		return engine.ToolResult{}, err
	}

	snapshot, err := execCtx.Runner.SpawnAgent(execCtx, engine.SpawnRequest{
		Name:           args.Name,
		Prompt:         args.Prompt,
		Background:     args.Background,
		Priority:       args.Priority,
		Model:          args.Model,
		MaxTurns:       args.MaxTurns,
		InheritHistory: args.InheritHistory,
		WorkingDir:     args.WorkingDir,
	})
	if err != nil {
		return engine.ToolResult{}, err
	}

	if args.Background {
		return engine.ToolResult{
			Content: mustJSON(map[string]any{
				"agent_id":   snapshot.ID,
				"name":       snapshot.Name,
				"status":     snapshot.Status,
				"priority":   snapshot.Priority,
				"background": true,
			}),
		}, nil
	}

	if isTerminalAgentError(snapshot.Status) {
		return engine.ToolResult{
			Content: mustJSON(map[string]any{
				"agent_id": snapshot.ID,
				"name":     snapshot.Name,
				"status":   snapshot.Status,
				"error":    snapshot.Error,
			}),
			IsError: true,
		}, nil
	}

	return engine.ToolResult{
		Content: mustJSON(map[string]any{
			"agent_id": snapshot.ID,
			"name":     snapshot.Name,
			"status":   snapshot.Status,
			"result":   snapshot.Result,
		}),
	}, nil
}

type AgentSendTool struct{}

type agentSendInput struct {
	AgentID    string `json:"agent_id"`
	Prompt     string `json:"prompt"`
	Background bool   `json:"background,omitempty"`
}

func (t *AgentSendTool) Definition() engine.ToolDefinition {
	return engine.ToolDefinition{
		Name:        "agent_send",
		Description: "Continue a previously spawned sub-agent with a new prompt while preserving its private session history.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent_id": map[string]any{
					"type":        "string",
					"description": "The ID returned by agent_spawn.",
				},
				"prompt": map[string]any{
					"type":        "string",
					"description": "The next task for the existing sub-agent.",
				},
				"background": map[string]any{
					"type":        "boolean",
					"description": "Run asynchronously if true.",
				},
			},
			"required": []string{"agent_id", "prompt"},
		},
	}
}

func (t *AgentSendTool) Call(ctx context.Context, execCtx *engine.ExecutionContext, input json.RawMessage) (engine.ToolResult, error) {
	var args agentSendInput
	if err := json.Unmarshal(input, &args); err != nil {
		return engine.ToolResult{}, err
	}

	snapshot, err := execCtx.Runner.SendAgent(ctx, args.AgentID, args.Prompt, args.Background)
	if err != nil {
		return engine.ToolResult{}, err
	}

	if args.Background {
		return engine.ToolResult{
			Content: mustJSON(map[string]any{
				"agent_id":   snapshot.ID,
				"name":       snapshot.Name,
				"status":     snapshot.Status,
				"background": true,
			}),
		}, nil
	}

	if isTerminalAgentError(snapshot.Status) {
		return engine.ToolResult{
			Content: mustJSON(snapshot),
			IsError: true,
		}, nil
	}
	return engine.ToolResult{Content: mustJSON(snapshot)}, nil
}

type agentTaskInput struct {
	Name           string   `json:"name,omitempty"`
	Prompt         string   `json:"prompt"`
	DependsOn      []string `json:"depends_on,omitempty"`
	Priority       int      `json:"priority,omitempty"`
	Model          string   `json:"model,omitempty"`
	MaxTurns       int      `json:"max_turns,omitempty"`
	InheritHistory bool     `json:"inherit_history,omitempty"`
	WorkingDir     string   `json:"working_dir,omitempty"`
}

type AgentFanoutTool struct{}

type agentFanoutInput struct {
	Tasks          []agentTaskInput `json:"tasks"`
	Wait           bool             `json:"wait,omitempty"`
	MaxParallel    int              `json:"max_parallel,omitempty"`
	FailFast       bool             `json:"fail_fast,omitempty"`
	TimeoutSeconds int              `json:"timeout_seconds,omitempty"`
}

func (t *AgentFanoutTool) Definition() engine.ToolDefinition {
	return engine.ToolDefinition{
		Name:        "agent_fanout",
		Description: "Spawn a batch of sub-agents. Use wait=true to fan out first and then wait for the whole batch to finish.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tasks": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name": map[string]any{
								"type":        "string",
								"description": "Optional short name for the sub-agent.",
							},
							"prompt": map[string]any{
								"type":        "string",
								"description": "Task for the sub-agent.",
							},
							"depends_on": map[string]any{
								"type":        "array",
								"items":       map[string]any{"type": "string"},
								"description": "Optional dependency list by task name. Requires wait=true.",
							},
							"priority": map[string]any{
								"type":        "integer",
								"description": "Optional scheduling priority. Higher values run first when agents are queued.",
							},
							"model": map[string]any{
								"type":        "string",
								"description": "Optional model override.",
							},
							"max_turns": map[string]any{
								"type":        "integer",
								"description": "Optional max turn override.",
							},
							"inherit_history": map[string]any{
								"type":        "boolean",
								"description": "Clone the current conversation into the child agent if true.",
							},
							"working_dir": map[string]any{
								"type":        "string",
								"description": "Optional working directory for the sub-agent.",
							},
						},
						"required": []string{"prompt"},
					},
					"description": "Batch of sub-agent tasks to spawn.",
				},
				"wait": map[string]any{
					"type":        "boolean",
					"description": "Wait for the whole batch if true.",
				},
				"max_parallel": map[string]any{
					"type":        "integer",
					"description": "Optional workflow-local concurrency cap used when wait=true.",
				},
				"fail_fast": map[string]any{
					"type":        "boolean",
					"description": "Cancel active workflow tasks and skip pending ones when any task fails. Only applies when wait=true.",
				},
				"timeout_seconds": map[string]any{
					"type":        "integer",
					"description": "Optional timeout for the whole batch wait.",
				},
			},
			"required": []string{"tasks"},
		},
	}
}

func (t *AgentFanoutTool) Call(ctx context.Context, execCtx *engine.ExecutionContext, input json.RawMessage) (engine.ToolResult, error) {
	var args agentFanoutInput
	if err := json.Unmarshal(input, &args); err != nil {
		return engine.ToolResult{}, err
	}
	if len(args.Tasks) == 0 {
		return engine.ToolResult{}, fmt.Errorf("tasks must not be empty")
	}

	plan, hasDependencies, err := buildFanoutPlan(args.Tasks)
	if err != nil {
		return engine.ToolResult{}, err
	}
	if hasDependencies && !args.Wait {
		return engine.ToolResult{}, fmt.Errorf("depends_on requires wait=true so xxx-code can orchestrate the workflow")
	}
	workflowMode := hasDependencies || args.MaxParallel > 0 || args.FailFast
	if workflowMode && !args.Wait {
		return engine.ToolResult{}, fmt.Errorf("workflow controls require wait=true")
	}
	if workflowMode {
		waitCtx := ctx
		if args.TimeoutSeconds > 0 {
			var cancel context.CancelFunc
			waitCtx, cancel = context.WithTimeout(ctx, time.Duration(args.TimeoutSeconds)*time.Second)
			defer cancel()
		}

		results, snapshots, err := executeFanoutPlan(waitCtx, execCtx, plan, fanoutExecutionOptions{
			maxParallel: args.MaxParallel,
			failFast:    args.FailFast,
		})
		if err != nil {
			return engine.ToolResult{}, err
		}
		return engine.ToolResult{
			Content: mustJSON(map[string]any{
				"agents": snapshots,
				"tasks":  results,
			}),
			IsError: hasFanoutTaskErrors(results),
		}, nil
	}

	spawned := make([]engine.AgentSnapshot, 0, len(args.Tasks))
	ids := make([]string, 0, len(args.Tasks))
	for _, task := range args.Tasks {
		snapshot, err := execCtx.Runner.SpawnAgent(execCtx, engine.SpawnRequest{
			Name:           task.Name,
			Prompt:         task.Prompt,
			Background:     true,
			Priority:       task.Priority,
			Model:          task.Model,
			MaxTurns:       task.MaxTurns,
			InheritHistory: task.InheritHistory,
			WorkingDir:     task.WorkingDir,
		})
		if err != nil {
			return engine.ToolResult{}, err
		}
		spawned = append(spawned, snapshot)
		ids = append(ids, snapshot.ID)
	}

	if !args.Wait {
		return engine.ToolResult{
			Content: mustJSON(map[string]any{
				"agents": spawned,
			}),
		}, nil
	}

	waitCtx := ctx
	if args.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, time.Duration(args.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	snapshots, err := execCtx.Runner.WaitAgents(waitCtx, ids)
	if err != nil {
		return engine.ToolResult{}, err
	}
	return engine.ToolResult{
		Content: mustJSON(map[string]any{
			"agents": snapshots,
		}),
		IsError: hasAgentErrors(snapshots),
	}, nil
}

type AgentCancelTool struct{}

type agentCancelInput struct {
	AgentID   string `json:"agent_id"`
	Recursive bool   `json:"recursive,omitempty"`
}

func (t *AgentCancelTool) Definition() engine.ToolDefinition {
	return engine.ToolDefinition{
		Name:        "agent_cancel",
		Description: "Cancel a running sub-agent. Use recursive=true to cancel its descendant agents as well.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent_id": map[string]any{
					"type":        "string",
					"description": "The ID of the running agent.",
				},
				"recursive": map[string]any{
					"type":        "boolean",
					"description": "Cancel descendant agents too when true.",
				},
			},
			"required": []string{"agent_id"},
		},
	}
}

func (t *AgentCancelTool) Call(ctx context.Context, execCtx *engine.ExecutionContext, input json.RawMessage) (engine.ToolResult, error) {
	var args agentCancelInput
	if err := json.Unmarshal(input, &args); err != nil {
		return engine.ToolResult{}, err
	}

	snapshot, err := execCtx.Runner.CancelAgent(ctx, args.AgentID, args.Recursive)
	if err != nil {
		return engine.ToolResult{}, err
	}
	return engine.ToolResult{Content: mustJSON(snapshot)}, nil
}

type AgentWaitTool struct{}

type agentWaitInput struct {
	AgentID        string   `json:"agent_id,omitempty"`
	AgentIDs       []string `json:"agent_ids,omitempty"`
	All            bool     `json:"all,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
}

func (t *AgentWaitTool) Definition() engine.ToolDefinition {
	return engine.ToolDefinition{
		Name:        "agent_wait",
		Description: "Wait for one or more sub-agents to finish. Use agent_id for one agent, agent_ids for many, or all=true for every known agent.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent_id": map[string]any{
					"type":        "string",
					"description": "The ID returned by agent_spawn.",
				},
				"agent_ids": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional list of agent IDs to wait for.",
				},
				"all": map[string]any{
					"type":        "boolean",
					"description": "Wait for every known agent if true.",
				},
				"timeout_seconds": map[string]any{
					"type":        "integer",
					"description": "Optional timeout in seconds.",
				},
			},
		},
	}
}

func (t *AgentWaitTool) Call(ctx context.Context, execCtx *engine.ExecutionContext, input json.RawMessage) (engine.ToolResult, error) {
	var args agentWaitInput
	if err := json.Unmarshal(input, &args); err != nil {
		return engine.ToolResult{}, err
	}
	waitCtx := ctx
	if args.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, time.Duration(args.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	if strings.TrimSpace(args.AgentID) == "" && len(args.AgentIDs) == 0 && !args.All {
		return engine.ToolResult{}, fmt.Errorf("one of agent_id, agent_ids, or all=true is required")
	}

	if strings.TrimSpace(args.AgentID) != "" && len(args.AgentIDs) == 0 && !args.All {
		snapshot, err := execCtx.Runner.WaitAgent(waitCtx, args.AgentID)
		if err != nil {
			return engine.ToolResult{}, err
		}
		if isTerminalAgentError(snapshot.Status) {
			return engine.ToolResult{
				Content: mustJSON(snapshot),
				IsError: true,
			}, nil
		}
		return engine.ToolResult{Content: mustJSON(snapshot)}, nil
	}

	ids := args.AgentIDs
	if strings.TrimSpace(args.AgentID) != "" {
		ids = append([]string{args.AgentID}, ids...)
	}
	if args.All {
		ids = nil
	}

	snapshots, err := execCtx.Runner.WaitAgents(waitCtx, ids)
	if err != nil {
		return engine.ToolResult{}, err
	}
	return engine.ToolResult{
		Content: mustJSON(map[string]any{
			"agents": snapshots,
		}),
		IsError: hasAgentErrors(snapshots),
	}, nil
}

type AgentListTool struct{}

func (t *AgentListTool) Definition() engine.ToolDefinition {
	return engine.ToolDefinition{
		Name:        "agent_list",
		Description: "List all spawned agents and their current status.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t *AgentListTool) Call(ctx context.Context, execCtx *engine.ExecutionContext, input json.RawMessage) (engine.ToolResult, error) {
	_ = ctx
	if len(input) > 0 && string(input) != "{}" && string(input) != "null" {
		var dummy map[string]any
		if err := json.Unmarshal(input, &dummy); err != nil {
			return engine.ToolResult{}, fmt.Errorf("invalid input: %w", err)
		}
	}
	return engine.ToolResult{
		Content: mustJSON(map[string]any{
			"agents": execCtx.Runner.ListAgents(),
		}),
	}, nil
}

func isTerminalAgentError(status engine.AgentStatus) bool {
	return status == engine.AgentFailed || status == engine.AgentCancelled
}

func hasAgentErrors(snapshots []engine.AgentSnapshot) bool {
	for _, snapshot := range snapshots {
		if isTerminalAgentError(snapshot.Status) {
			return true
		}
	}
	return false
}

type fanoutTaskResult struct {
	Name           string   `json:"name"`
	Prompt         string   `json:"prompt"`
	ResolvedPrompt string   `json:"resolved_prompt,omitempty"`
	DependsOn      []string `json:"depends_on,omitempty"`
	Priority       int      `json:"priority,omitempty"`
	AgentID        string   `json:"agent_id,omitempty"`
	Status         string   `json:"status"`
	Result         string   `json:"result,omitempty"`
	Error          string   `json:"error,omitempty"`
}

type plannedFanoutTask struct {
	index      int
	task       agentTaskInput
	key        string
	dependsOn  []string
	depIndexes []int
	promptRefs []fanoutPromptReference
	started    bool
	done       bool
	agentID    string
	snapshot   *engine.AgentSnapshot
	result     fanoutTaskResult
}

type fanoutWaitResult struct {
	index    int
	agentID  string
	snapshot engine.AgentSnapshot
	err      error
}

type fanoutExecutionOptions struct {
	maxParallel int
	failFast    bool
}

type fanoutPromptReference struct {
	raw      string
	taskName string
	field    string
}

var fanoutPromptPattern = regexp.MustCompile(`\{\{\s*tasks\.([A-Za-z0-9_-]+)\.(result|status|error|agent_id)\s*\}\}`)

func buildFanoutPlan(tasks []agentTaskInput) ([]*plannedFanoutTask, bool, error) {
	plan := make([]*plannedFanoutTask, 0, len(tasks))
	nameIndex := make(map[string]int, len(tasks))
	hasDependencies := false

	for i, task := range tasks {
		name := strings.TrimSpace(task.Name)
		if name != "" {
			if _, exists := nameIndex[name]; exists {
				return nil, false, fmt.Errorf("duplicate task name in agent_fanout: %s", name)
			}
			nameIndex[name] = i
		}

		displayName := name
		if displayName == "" {
			displayName = fmt.Sprintf("task_%d", i+1)
		}
		normalizedDeps := normalizeTaskDependencies(task.DependsOn)
		if len(normalizedDeps) > 0 {
			hasDependencies = true
		}
		promptRefs := parseFanoutPromptReferences(task.Prompt)
		plan = append(plan, &plannedFanoutTask{
			index:      i,
			task:       task,
			key:        displayName,
			dependsOn:  normalizedDeps,
			promptRefs: promptRefs,
			result: fanoutTaskResult{
				Name:      displayName,
				Prompt:    task.Prompt,
				DependsOn: append([]string(nil), normalizedDeps...),
				Priority:  task.Priority,
			},
		})
	}

	for _, item := range plan {
		item.depIndexes = make([]int, 0, len(item.dependsOn))
		for _, dep := range item.dependsOn {
			index, ok := nameIndex[dep]
			if !ok {
				return nil, false, fmt.Errorf("task %s depends on unknown task %s", item.key, dep)
			}
			item.depIndexes = append(item.depIndexes, index)
		}
		if name := strings.TrimSpace(item.task.Name); name != "" && containsString(item.dependsOn, name) {
			return nil, false, fmt.Errorf("task %s cannot depend on itself", item.key)
		}
		if err := validateFanoutPromptReferences(item, nameIndex); err != nil {
			return nil, false, err
		}
	}

	if err := validateFanoutPlanCycles(plan); err != nil {
		return nil, false, err
	}
	return plan, hasDependencies, nil
}

func validateFanoutPlanCycles(plan []*plannedFanoutTask) error {
	const (
		visitNew = iota
		visitActive
		visitDone
	)

	state := make([]int, len(plan))
	var visit func(int) error
	visit = func(index int) error {
		switch state[index] {
		case visitActive:
			return fmt.Errorf("dependency cycle detected at task %s", plan[index].key)
		case visitDone:
			return nil
		}
		state[index] = visitActive
		for _, depIndex := range plan[index].depIndexes {
			if err := visit(depIndex); err != nil {
				return err
			}
		}
		state[index] = visitDone
		return nil
	}

	for index := range plan {
		if err := visit(index); err != nil {
			return err
		}
	}
	return nil
}

func executeFanoutPlan(ctx context.Context, execCtx *engine.ExecutionContext, plan []*plannedFanoutTask, options fanoutExecutionOptions) ([]fanoutTaskResult, []engine.AgentSnapshot, error) {
	active := make(map[string]int, len(plan))
	resultsCh := make(chan fanoutWaitResult, len(plan))
	failFastTriggered := false

	triggerFailFast := func(taskName, status string) {
		if !options.failFast || failFastTriggered {
			return
		}
		failFastTriggered = true
		cancelFanoutAgents(execCtx, active)
		markPendingFanoutTasksSkipped(plan, fmt.Sprintf("skipped because fail_fast triggered by task %s with status %s", taskName, status))
	}

	launchReady := func() error {
		if failFastTriggered {
			return nil
		}
		for _, item := range plan {
			if options.maxParallel > 0 && len(active) >= options.maxParallel {
				return nil
			}
			if item.started || item.done {
				continue
			}

			if failedDep, depStatus, ok := firstFailedDependency(item, plan); ok {
				item.done = true
				item.result.Status = "skipped"
				item.result.Error = fmt.Sprintf("skipped because dependency %s finished with status %s", failedDep, depStatus)
				continue
			}
			if !dependenciesSatisfied(item, plan) {
				continue
			}
			prompt, err := renderFanoutPrompt(item, plan)
			if err != nil {
				item.done = true
				item.result.Status = string(engine.AgentFailed)
				item.result.Error = err.Error()
				triggerFailFast(item.key, item.result.Status)
				continue
			}
			if prompt != item.task.Prompt {
				item.result.ResolvedPrompt = prompt
			}

			snapshot, err := execCtx.Runner.SpawnAgent(execCtx, engine.SpawnRequest{
				Name:           item.task.Name,
				Prompt:         prompt,
				Background:     true,
				Priority:       item.task.Priority,
				Model:          item.task.Model,
				MaxTurns:       item.task.MaxTurns,
				InheritHistory: item.task.InheritHistory,
				WorkingDir:     item.task.WorkingDir,
			})
			if err != nil {
				item.done = true
				item.result.Status = string(engine.AgentFailed)
				item.result.Error = err.Error()
				continue
			}

			item.started = true
			item.agentID = snapshot.ID
			item.result.AgentID = snapshot.ID
			active[snapshot.ID] = item.index

			go func(index int, agentID string) {
				snapshot, err := execCtx.Runner.WaitAgent(ctx, agentID)
				resultsCh <- fanoutWaitResult{
					index:    index,
					agentID:  agentID,
					snapshot: snapshot,
					err:      err,
				}
			}(item.index, snapshot.ID)
		}
		return nil
	}

	if err := launchReady(); err != nil {
		return nil, nil, err
	}

	for completedCount(plan) < len(plan) {
		if len(active) == 0 {
			if err := launchReady(); err != nil {
				return nil, nil, err
			}
			if len(active) == 0 {
				if completedCount(plan) == len(plan) {
					break
				}
				return nil, nil, fmt.Errorf("agent_fanout workflow made no progress; check dependencies")
			}
		}

		select {
		case <-ctx.Done():
			cancelFanoutAgents(execCtx, active)
			return nil, nil, ctx.Err()
		case item := <-resultsCh:
			delete(active, item.agentID)
			if item.err != nil {
				cancelFanoutAgents(execCtx, active)
				return nil, nil, item.err
			}

			current := plan[item.index]
			current.done = true
			current.snapshot = &item.snapshot
			current.result.AgentID = item.snapshot.ID
			current.result.Status = string(item.snapshot.Status)
			current.result.Result = item.snapshot.Result
			current.result.Error = item.snapshot.Error
			if current.result.Status != string(engine.AgentIdle) {
				triggerFailFast(current.key, current.result.Status)
			}

			if err := launchReady(); err != nil {
				cancelFanoutAgents(execCtx, active)
				return nil, nil, err
			}
		}
	}

	results := make([]fanoutTaskResult, 0, len(plan))
	snapshots := make([]engine.AgentSnapshot, 0, len(plan))
	for _, item := range plan {
		results = append(results, item.result)
		if item.snapshot != nil {
			snapshots = append(snapshots, *item.snapshot)
		}
	}
	return results, snapshots, nil
}

func completedCount(plan []*plannedFanoutTask) int {
	count := 0
	for _, item := range plan {
		if item.done {
			count++
		}
	}
	return count
}

func dependenciesSatisfied(item *plannedFanoutTask, plan []*plannedFanoutTask) bool {
	for _, depIndex := range item.depIndexes {
		dependency := plan[depIndex]
		if !dependency.done {
			return false
		}
		if dependency.result.Status != string(engine.AgentIdle) {
			return false
		}
	}
	return true
}

func firstFailedDependency(item *plannedFanoutTask, plan []*plannedFanoutTask) (string, string, bool) {
	for depPos, depIndex := range item.depIndexes {
		dependency := plan[depIndex]
		if !dependency.done {
			continue
		}
		if dependency.result.Status == string(engine.AgentIdle) {
			continue
		}
		return item.dependsOn[depPos], dependency.result.Status, true
	}
	return "", "", false
}

func cancelFanoutAgents(execCtx *engine.ExecutionContext, active map[string]int) {
	for agentID := range active {
		_, _ = execCtx.Runner.CancelAgent(context.Background(), agentID, true)
	}
}

func markPendingFanoutTasksSkipped(plan []*plannedFanoutTask, reason string) {
	for _, item := range plan {
		if item.started || item.done {
			continue
		}
		item.done = true
		item.result.Status = "skipped"
		item.result.Error = reason
	}
}

func hasFanoutTaskErrors(results []fanoutTaskResult) bool {
	for _, item := range results {
		if item.Status != string(engine.AgentIdle) {
			return true
		}
	}
	return false
}

func parseFanoutPromptReferences(prompt string) []fanoutPromptReference {
	matches := fanoutPromptPattern.FindAllStringSubmatch(prompt, -1)
	if len(matches) == 0 {
		return nil
	}

	references := make([]fanoutPromptReference, 0, len(matches))
	for _, match := range matches {
		if len(match) != 3 {
			continue
		}
		references = append(references, fanoutPromptReference{
			raw:      match[0],
			taskName: match[1],
			field:    match[2],
		})
	}
	return references
}

func validateFanoutPromptReferences(item *plannedFanoutTask, nameIndex map[string]int) error {
	if item == nil {
		return nil
	}
	taskName := strings.TrimSpace(item.task.Name)
	for _, ref := range item.promptRefs {
		if ref.taskName == taskName && taskName != "" {
			return fmt.Errorf("task %s cannot reference itself in prompt", item.key)
		}
		if _, ok := nameIndex[ref.taskName]; !ok {
			return fmt.Errorf("task %s prompt references unknown task %s", item.key, ref.taskName)
		}
		if !containsString(item.dependsOn, ref.taskName) {
			return fmt.Errorf("task %s prompt references %s but does not declare depends_on", item.key, ref.taskName)
		}
	}
	return nil
}

func renderFanoutPrompt(item *plannedFanoutTask, plan []*plannedFanoutTask) (string, error) {
	if item == nil || len(item.promptRefs) == 0 {
		return item.task.Prompt, nil
	}

	rendered := item.task.Prompt
	for _, ref := range item.promptRefs {
		dependency, ok := findFanoutTaskByName(plan, ref.taskName)
		if !ok {
			return "", fmt.Errorf("task %s prompt references unknown task %s", item.key, ref.taskName)
		}
		if !dependency.done {
			return "", fmt.Errorf("task %s prompt reference %s is not ready yet", item.key, ref.taskName)
		}

		value := fanoutPromptFieldValue(dependency.result, ref.field)
		rendered = strings.ReplaceAll(rendered, ref.raw, value)
	}
	return rendered, nil
}

func findFanoutTaskByName(plan []*plannedFanoutTask, name string) (*plannedFanoutTask, bool) {
	for _, item := range plan {
		if strings.TrimSpace(item.task.Name) == name {
			return item, true
		}
	}
	return nil, false
}

func fanoutPromptFieldValue(result fanoutTaskResult, field string) string {
	switch field {
	case "result":
		return result.Result
	case "status":
		return result.Status
	case "error":
		return result.Error
	case "agent_id":
		return result.AgentID
	default:
		return ""
	}
}

func normalizeTaskDependencies(values []string) []string {
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
