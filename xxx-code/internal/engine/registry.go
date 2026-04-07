package engine

import (
	"sort"
	"sync"
)

type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry(tools ...Tool) *Registry {
	registry := &Registry{
		tools: make(map[string]Tool, len(tools)),
	}
	for _, tool := range tools {
		_ = registry.AddTool(tool)
	}
	return registry
}

func (r *Registry) AddTool(tool Tool) error {
	if tool == nil {
		return nil
	}
	def := tool.Definition()
	if def.Name == "" {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[def.Name]; exists {
		return &DuplicateToolError{Name: def.Name}
	}
	r.tools[def.Name] = tool
	return nil
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

func (r *Registry) Definitions() []ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)

	defs := make([]ToolDefinition, 0, len(names))
	for _, name := range names {
		defs = append(defs, r.tools[name].Definition())
	}
	return defs
}

type DuplicateToolError struct {
	Name string
}

func (e *DuplicateToolError) Error() string {
	return "tool already registered: " + e.Name
}
