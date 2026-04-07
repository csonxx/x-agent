package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type EventKind string

const (
	EventAssistantText  EventKind = "assistant_text"
	EventToolCall       EventKind = "tool_call"
	EventToolResult     EventKind = "tool_result"
	EventAgentSpawned   EventKind = "agent_spawned"
	EventAgentCompleted EventKind = "agent_completed"
)

type Event struct {
	Kind      EventKind
	AgentID   string
	AgentName string
	ToolName  string
	Text      string
}

type RunnerConfig struct {
	Model         string
	SystemPrompt  string
	MaxTokens     int
	MaxTurns      int
	Temperature   float64
	WorkingDir    string
	ToolTimeout   time.Duration
	MaxAgentDepth int
	EventHandler  func(Event)
}

type Runner struct {
	provider Provider
	registry *Registry
	config   RunnerConfig

	agentState *agentState
}

type RunResult struct {
	FinalText string
	Usage     Usage
	Messages  []Message
}

func NewRunner(provider Provider, registry *Registry, config RunnerConfig) *Runner {
	if config.MaxTurns <= 0 {
		config.MaxTurns = 12
	}
	if config.MaxTokens <= 0 {
		config.MaxTokens = 16_384
	}
	if config.ToolTimeout <= 0 {
		config.ToolTimeout = 2 * time.Minute
	}
	if config.MaxAgentDepth <= 0 {
		config.MaxAgentDepth = 3
	}
	return &Runner{
		provider: provider,
		registry: registry,
		config:   config,
		agentState: &agentState{
			agents: make(map[string]*managedAgent),
		},
	}
}

func (r *Runner) RunTurn(ctx context.Context, session *Session, prompt string) (RunResult, error) {
	exec := &ExecutionContext{
		Runner:     r,
		Session:    session,
		WorkingDir: r.config.WorkingDir,
	}
	return r.runTurn(ctx, exec, prompt)
}

func (r *Runner) runTurn(ctx context.Context, exec *ExecutionContext, prompt string) (RunResult, error) {
	if strings.TrimSpace(prompt) == "" {
		return RunResult{}, errors.New("prompt is empty")
	}

	exec.Session.Append(NewTextMessage(RoleUser, prompt))

	var total Usage
	var finalText string

	for turn := 0; turn < r.config.MaxTurns; turn++ {
		response, err := r.provider.CreateMessage(ctx, CompletionRequest{
			Model:       r.config.Model,
			System:      r.config.SystemPrompt,
			MaxTokens:   r.config.MaxTokens,
			Messages:    exec.Session.Snapshot(),
			Tools:       r.registry.Definitions(),
			Temperature: r.config.Temperature,
		})
		if err != nil {
			return RunResult{}, err
		}

		total.InputTokens += response.Usage.InputTokens
		total.OutputTokens += response.Usage.OutputTokens

		exec.Session.Append(response.Message)

		assistantText := response.Message.Text()
		if assistantText != "" {
			finalText = assistantText
			r.emit(Event{
				Kind:      EventAssistantText,
				AgentID:   exec.AgentID,
				AgentName: exec.AgentName,
				Text:      assistantText,
			})
		}

		toolUses := collectToolUses(response.Message)
		if len(toolUses) == 0 {
			return RunResult{
				FinalText: finalText,
				Usage:     total,
				Messages:  exec.Session.Snapshot(),
			}, nil
		}

		for _, toolBlock := range toolUses {
			tool, ok := r.registry.Get(toolBlock.Name)
			if !ok {
				exec.Session.Append(Message{
					Role: RoleUser,
					Content: []Block{
						{
							Type:      BlockToolResult,
							ToolUseID: toolBlock.ID,
							Result:    "unknown tool: " + toolBlock.Name,
							IsError:   true,
						},
					},
				})
				continue
			}

			r.emit(Event{
				Kind:      EventToolCall,
				AgentID:   exec.AgentID,
				AgentName: exec.AgentName,
				ToolName:  toolBlock.Name,
				Text:      formatToolInput(toolBlock.Input),
			})

			toolCtx, cancel := context.WithTimeout(ctx, r.config.ToolTimeout)
			result, callErr := tool.Call(toolCtx, exec, toolBlock.Input)
			cancel()

			if callErr != nil {
				result = ToolResult{
					Content: callErr.Error(),
					IsError: true,
				}
			}
			result.Content = truncate(result.Content, 120_000)

			r.emit(Event{
				Kind:      EventToolResult,
				AgentID:   exec.AgentID,
				AgentName: exec.AgentName,
				ToolName:  toolBlock.Name,
				Text:      result.Content,
			})

			exec.Session.Append(Message{
				Role: RoleUser,
				Content: []Block{
					{
						Type:      BlockToolResult,
						ToolUseID: toolBlock.ID,
						Result:    result.Content,
						IsError:   result.IsError,
					},
				},
			})
		}
	}

	return RunResult{
		FinalText: finalText,
		Usage:     total,
		Messages:  exec.Session.Snapshot(),
	}, fmt.Errorf("max turns reached without a final answer")
}

func (r *Runner) emit(event Event) {
	if r.config.EventHandler != nil {
		r.config.EventHandler(event)
	}
}

func collectToolUses(message Message) []Block {
	blocks := make([]Block, 0)
	for _, block := range message.Content {
		if block.Type == BlockToolUse {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func formatToolInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	pretty, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(pretty)
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "\n\n[truncated]"
}
