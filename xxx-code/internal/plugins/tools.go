package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
)

type pluginSummary struct {
	PluginDir   string   `json:"plugin_dir,omitempty"`
	PluginCount int      `json:"plugin_count"`
	ToolCount   int      `json:"tool_count"`
	Statuses    []Status `json:"statuses"`
}

type listTool struct {
	manager *Manager
}

func (t *listTool) Definition() engine.ToolDefinition {
	return engine.ToolDefinition{
		Name:        "list_plugins",
		Description: "List loaded runtime plugins and their bridged tools.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t *listTool) Call(ctx context.Context, exec *engine.ExecutionContext, input json.RawMessage) (engine.ToolResult, error) {
	_ = ctx
	_ = exec
	_ = input
	return engine.ToolResult{Content: mustJSON(currentSummary(t.manager))}, nil
}

type reloadTool struct {
	manager *Manager
}

func (t *reloadTool) Definition() engine.ToolDefinition {
	return engine.ToolDefinition{
		Name:        "reload_plugins",
		Description: "Reload plugin manifests from the configured plugin directory.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t *reloadTool) Call(ctx context.Context, exec *engine.ExecutionContext, input json.RawMessage) (engine.ToolResult, error) {
	_ = exec
	_ = input
	if err := t.manager.Reload(ctx); err != nil {
		return engine.ToolResult{}, err
	}
	return engine.ToolResult{Content: mustJSON(currentSummary(t.manager))}, nil
}

type validateTool struct {
	manager *Manager
}

func (t *validateTool) Definition() engine.ToolDefinition {
	return engine.ToolDefinition{
		Name:        "validate_plugin",
		Description: "Validate a runtime plugin manifest or plugin directory before installing it.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source": map[string]any{
					"type":        "string",
					"description": "Path to a plugin directory or plugin manifest file.",
				},
			},
			"required": []string{"source"},
		},
	}
}

func (t *validateTool) Call(ctx context.Context, exec *engine.ExecutionContext, input json.RawMessage) (engine.ToolResult, error) {
	_ = ctx
	_ = exec
	var payload struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(orEmptyObject(input), &payload); err != nil {
		return engine.ToolResult{}, err
	}
	if strings.TrimSpace(payload.Source) == "" {
		return engine.ToolResult{}, errors.New("plugin source is required")
	}
	return engine.ToolResult{Content: mustJSON(t.manager.Validate(payload.Source))}, nil
}

type installTool struct {
	manager *Manager
}

func (t *installTool) Definition() engine.ToolDefinition {
	return engine.ToolDefinition{
		Name:        "install_plugin",
		Description: "Install a runtime plugin from a local manifest file or plugin directory into the configured plugin directory.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source": map[string]any{
					"type":        "string",
					"description": "Path to a plugin directory or plugin manifest file.",
				},
				"force": map[string]any{
					"type":        "boolean",
					"description": "Replace an existing plugin with the same name when true.",
				},
			},
			"required": []string{"source"},
		},
	}
}

func (t *installTool) Call(ctx context.Context, exec *engine.ExecutionContext, input json.RawMessage) (engine.ToolResult, error) {
	_ = exec
	var payload struct {
		Source string `json:"source"`
		Force  bool   `json:"force,omitempty"`
	}
	if err := json.Unmarshal(orEmptyObject(input), &payload); err != nil {
		return engine.ToolResult{}, err
	}
	if strings.TrimSpace(payload.Source) == "" {
		return engine.ToolResult{}, errors.New("plugin source is required")
	}
	if err := t.manager.Install(ctx, payload.Source, payload.Force); err != nil {
		return engine.ToolResult{}, err
	}
	return engine.ToolResult{Content: mustJSON(currentSummary(t.manager))}, nil
}

type removeTool struct {
	manager *Manager
}

func (t *removeTool) Definition() engine.ToolDefinition {
	return engine.ToolDefinition{
		Name:        "remove_plugin",
		Description: "Remove an installed runtime plugin from the configured plugin directory.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Installed plugin name to remove.",
				},
			},
			"required": []string{"name"},
		},
	}
}

func (t *removeTool) Call(ctx context.Context, exec *engine.ExecutionContext, input json.RawMessage) (engine.ToolResult, error) {
	_ = ctx
	_ = exec
	var payload struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(orEmptyObject(input), &payload); err != nil {
		return engine.ToolResult{}, err
	}
	if strings.TrimSpace(payload.Name) == "" {
		return engine.ToolResult{}, errors.New("plugin name is required")
	}
	if err := t.manager.Remove(ctx, payload.Name); err != nil {
		return engine.ToolResult{}, err
	}
	return engine.ToolResult{Content: mustJSON(currentSummary(t.manager))}, nil
}

func currentSummary(manager *Manager) pluginSummary {
	if manager == nil {
		return pluginSummary{}
	}
	return pluginSummary{
		PluginDir:   manager.PluginDir(),
		PluginCount: manager.PluginCount(),
		ToolCount:   manager.ToolCount(),
		Statuses:    manager.Statuses(),
	}
}

func mustJSON(value any) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}
