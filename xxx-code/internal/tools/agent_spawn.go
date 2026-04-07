package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
)

type AgentSpawnTool struct{}

type agentSpawnInput struct {
	Name           string `json:"name,omitempty"`
	Prompt         string `json:"prompt"`
	Background     bool   `json:"background,omitempty"`
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
				"background": true,
			}),
		}, nil
	}

	if snapshot.Status == engine.AgentFailed {
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

	if snapshot.Status == engine.AgentFailed {
		return engine.ToolResult{
			Content: mustJSON(snapshot),
			IsError: true,
		}, nil
	}
	return engine.ToolResult{Content: mustJSON(snapshot)}, nil
}

type AgentWaitTool struct{}

type agentWaitInput struct {
	AgentID        string `json:"agent_id"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

func (t *AgentWaitTool) Definition() engine.ToolDefinition {
	return engine.ToolDefinition{
		Name:        "agent_wait",
		Description: "Wait for a sub-agent's current task to finish and return its latest snapshot.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent_id": map[string]any{
					"type":        "string",
					"description": "The ID returned by agent_spawn.",
				},
				"timeout_seconds": map[string]any{
					"type":        "integer",
					"description": "Optional timeout in seconds.",
				},
			},
			"required": []string{"agent_id"},
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

	snapshot, err := execCtx.Runner.WaitAgent(waitCtx, args.AgentID)
	if err != nil {
		return engine.ToolResult{}, err
	}
	if snapshot.Status == engine.AgentFailed {
		return engine.ToolResult{
			Content: mustJSON(snapshot),
			IsError: true,
		}, nil
	}
	return engine.ToolResult{Content: mustJSON(snapshot)}, nil
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
