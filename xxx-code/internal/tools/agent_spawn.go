package tools

import (
	"context"
	"encoding/json"
	"fmt"
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
	Name           string `json:"name,omitempty"`
	Prompt         string `json:"prompt"`
	Priority       int    `json:"priority,omitempty"`
	Model          string `json:"model,omitempty"`
	MaxTurns       int    `json:"max_turns,omitempty"`
	InheritHistory bool   `json:"inherit_history,omitempty"`
	WorkingDir     string `json:"working_dir,omitempty"`
}

type AgentFanoutTool struct{}

type agentFanoutInput struct {
	Tasks          []agentTaskInput `json:"tasks"`
	Wait           bool             `json:"wait,omitempty"`
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
