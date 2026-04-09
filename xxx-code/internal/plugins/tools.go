package plugins

import (
	"context"
	"encoding/json"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
)

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
	return engine.ToolResult{Content: mustJSON(map[string]any{
		"plugin_dir":   t.manager.PluginDir(),
		"plugin_count": t.manager.PluginCount(),
		"tool_count":   t.manager.ToolCount(),
		"statuses":     t.manager.Statuses(),
	})}, nil
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
	return engine.ToolResult{Content: mustJSON(map[string]any{
		"plugin_dir":   t.manager.PluginDir(),
		"plugin_count": t.manager.PluginCount(),
		"tool_count":   t.manager.ToolCount(),
		"statuses":     t.manager.Statuses(),
	})}, nil
}

func mustJSON(value any) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}
