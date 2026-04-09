package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
)

const (
	StatusLoaded = "loaded"
	StatusFailed = "failed"
)

type Options struct {
	WorkingDir string
	PluginDir  string
}

type Manifest struct {
	Name    string            `json:"name"`
	Version string            `json:"version,omitempty"`
	Tools   []CommandToolSpec `json:"tools"`
}

type CommandToolSpec struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	InputSchema map[string]any    `json:"input_schema,omitempty"`
	Command     string            `json:"command"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	Timeout     string            `json:"timeout,omitempty"`
}

type Status struct {
	Name         string   `json:"name"`
	Version      string   `json:"version,omitempty"`
	ManifestPath string   `json:"manifest_path,omitempty"`
	Status       string   `json:"status"`
	ToolNames    []string `json:"tool_names,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type Manager struct {
	mu           sync.RWMutex
	registry     *engine.Registry
	options      Options
	pluginDir    string
	statuses     []Status
	dynamicTools []string
}

type commandTool struct {
	fullName    string
	pluginName  string
	description string
	inputSchema map[string]any
	command     string
	args        []string
	env         map[string]string
	cwd         string
	timeout     time.Duration
}

func Start(ctx context.Context, registry *engine.Registry, options Options) (*Manager, error) {
	if registry == nil {
		return nil, errors.New("plugin manager requires a tool registry")
	}

	pluginDir, ok, err := ResolvePluginDir(options.WorkingDir, options.PluginDir)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	manager := &Manager{
		registry:  registry,
		options:   options,
		pluginDir: pluginDir,
	}
	manager.registerSupportTools()
	if err := manager.load(ctx, pluginDir); err != nil {
		_ = manager.Close()
		return nil, err
	}
	return manager, nil
}

func ResolvePluginDir(workingDir, explicit string) (string, bool, error) {
	if strings.TrimSpace(explicit) != "" {
		path, err := expandPath(workingDir, explicit)
		if err != nil {
			return "", false, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", false, fmt.Errorf("stat plugin dir %q: %w", path, err)
		}
		if !info.IsDir() {
			return "", false, fmt.Errorf("plugin dir is not a directory: %s", path)
		}
		return path, true, nil
	}

	path := filepath.Join(workingDir, ".xxx-code", "plugins")
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("stat plugin dir %q: %w", path, err)
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("plugin dir is not a directory: %s", path)
	}
	return path, true, nil
}

func expandPath(base, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	return filepath.Join(base, value), nil
}

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, name := range m.dynamicTools {
		if m.registry != nil {
			m.registry.RemoveTool(name)
		}
	}
	m.dynamicTools = nil
	m.statuses = nil
	return nil
}

func (m *Manager) Reload(ctx context.Context) error {
	if m == nil {
		return errors.New("plugins are not configured")
	}
	pluginDir, ok, err := ResolvePluginDir(m.options.WorkingDir, m.options.PluginDir)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, name := range m.dynamicTools {
		if m.registry != nil {
			m.registry.RemoveTool(name)
		}
	}
	m.dynamicTools = nil
	m.statuses = nil
	if !ok {
		m.pluginDir = ""
		return nil
	}
	m.pluginDir = pluginDir
	return m.loadLocked(ctx, pluginDir)
}

func (m *Manager) Statuses() []Status {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	statuses := make([]Status, 0, len(m.statuses))
	for _, status := range m.statuses {
		copyStatus := status
		copyStatus.ToolNames = append([]string(nil), status.ToolNames...)
		copyStatus.Warnings = append([]string(nil), status.Warnings...)
		statuses = append(statuses, copyStatus)
	}
	return statuses
}

func (m *Manager) PluginDir() string {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pluginDir
}

func (m *Manager) PluginCount() int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.statuses)
}

func (m *Manager) ToolCount() int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	count := 0
	for _, status := range m.statuses {
		count += len(status.ToolNames)
	}
	return count
}

func (m *Manager) load(ctx context.Context, pluginDir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadLocked(ctx, pluginDir)
}

func (m *Manager) loadLocked(ctx context.Context, pluginDir string) error {
	m.pluginDir = pluginDir
	manifestPaths, err := discoverManifestPaths(pluginDir)
	if err != nil {
		return err
	}
	for _, manifestPath := range manifestPaths {
		status := Status{
			ManifestPath: manifestPath,
			Status:       StatusFailed,
		}
		manifest, err := loadManifest(manifestPath)
		if err != nil {
			status.Name = inferPluginName(manifestPath)
			status.Error = err.Error()
			m.statuses = append(m.statuses, status)
			continue
		}
		status.Name = manifest.Name
		status.Version = manifest.Version

		if strings.TrimSpace(manifest.Name) == "" {
			status.Error = "plugin name cannot be empty"
			m.statuses = append(m.statuses, status)
			continue
		}
		if len(manifest.Tools) == 0 {
			status.Error = "plugin must define at least one tool"
			m.statuses = append(m.statuses, status)
			continue
		}

		for _, spec := range manifest.Tools {
			tool, warning, err := newCommandTool(manifest, manifestPath, spec)
			if warning != "" {
				status.Warnings = append(status.Warnings, warning)
			}
			if err != nil {
				status.Warnings = append(status.Warnings, err.Error())
				continue
			}
			if err := m.registry.AddTool(tool); err != nil {
				status.Warnings = append(status.Warnings, err.Error())
				continue
			}
			status.ToolNames = append(status.ToolNames, tool.Definition().Name)
			m.dynamicTools = append(m.dynamicTools, tool.Definition().Name)
		}

		if len(status.ToolNames) == 0 {
			if len(status.Warnings) == 0 {
				status.Error = "plugin did not register any tools"
			} else {
				status.Error = "plugin tools failed to load"
			}
			m.statuses = append(m.statuses, status)
			continue
		}
		status.Status = StatusLoaded
		m.statuses = append(m.statuses, status)
	}
	return nil
}

func discoverManifestPaths(pluginDir string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(pluginDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name := strings.ToLower(entry.Name())
		if name == "plugin.json" || strings.HasSuffix(name, ".plugin.json") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func loadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read plugin manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse plugin manifest: %w", err)
	}
	return manifest, nil
}

func inferPluginName(path string) string {
	base := filepath.Base(filepath.Dir(path))
	if base == "." || base == string(filepath.Separator) {
		return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return base
}

func newCommandTool(manifest Manifest, manifestPath string, spec CommandToolSpec) (*commandTool, string, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return nil, "", errors.New("plugin tool name cannot be empty")
	}
	if strings.TrimSpace(spec.Command) == "" {
		return nil, "", fmt.Errorf("plugin tool %s command cannot be empty", spec.Name)
	}

	command := strings.TrimSpace(spec.Command)
	manifestDir := filepath.Dir(manifestPath)
	if strings.Contains(command, string(filepath.Separator)) {
		command = filepath.Clean(filepath.Join(manifestDir, command))
	}
	toolCwd := ""
	if strings.TrimSpace(spec.Cwd) != "" {
		toolCwd = filepath.Clean(filepath.Join(manifestDir, strings.TrimSpace(spec.Cwd)))
	}

	timeout := time.Duration(0)
	if strings.TrimSpace(spec.Timeout) != "" {
		parsed, err := time.ParseDuration(strings.TrimSpace(spec.Timeout))
		if err != nil {
			return nil, "", fmt.Errorf("plugin tool %s timeout: %w", spec.Name, err)
		}
		timeout = parsed
	}

	fullName := fmt.Sprintf("plugin__%s__%s", normalizeName(manifest.Name), normalizeName(spec.Name))
	tool := &commandTool{
		fullName:    fullName,
		pluginName:  manifest.Name,
		description: spec.Description,
		inputSchema: spec.InputSchema,
		command:     command,
		args:        append([]string(nil), spec.Args...),
		env:         copyStringMap(spec.Env),
		cwd:         toolCwd,
		timeout:     timeout,
	}
	warning := ""
	if strings.TrimSpace(spec.Description) == "" {
		warning = fmt.Sprintf("plugin tool %s has no description", spec.Name)
	}
	return tool, warning, nil
}

func (t *commandTool) Definition() engine.ToolDefinition {
	return engine.ToolDefinition{
		Name:        t.fullName,
		Description: firstNonEmpty(t.description, "Plugin tool"),
		InputSchema: t.inputSchema,
	}
}

func (t *commandTool) Call(ctx context.Context, execCtx *engine.ExecutionContext, input json.RawMessage) (engine.ToolResult, error) {
	callCtx := ctx
	cancel := func() {}
	if t.timeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, t.timeout)
	}
	defer cancel()

	command := exec.CommandContext(callCtx, t.command, t.args...)
	if t.cwd != "" {
		command.Dir = t.cwd
	} else if execCtx != nil && strings.TrimSpace(execCtx.WorkingDir) != "" {
		command.Dir = execCtx.WorkingDir
	}

	command.Env = append(os.Environ(),
		"XXX_CODE_PLUGIN_NAME="+t.pluginName,
		"XXX_CODE_PLUGIN_TOOL="+t.fullName,
		"XXX_CODE_WORKING_DIR="+firstNonEmpty(command.Dir, cwdOrEmpty(execCtx)),
	)
	command.Env = mergeEnv(command.Env, t.env)
	command.Stdin = bytes.NewReader(orEmptyObject(input))

	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	err := command.Run()
	stdoutText := strings.TrimSpace(stdout.String())
	stderrText := strings.TrimSpace(stderr.String())
	if err != nil {
		message := firstNonEmpty(stderrText, stdoutText, err.Error())
		return engine.ToolResult{Content: message, IsError: true}, nil
	}

	if result, ok := decodeStructuredResult(stdout.Bytes()); ok {
		return result, nil
	}
	return engine.ToolResult{Content: stdoutText}, nil
}

func (m *Manager) registerSupportTools() {
	if m == nil || m.registry == nil {
		return
	}
	_ = m.registry.AddTool(&listTool{manager: m})
	_ = m.registry.AddTool(&reloadTool{manager: m})
}

func normalizeName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	result := strings.Trim(b.String(), "_")
	if result == "" {
		return "plugin"
	}
	return result
}

func copyStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func mergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	values := make(map[string]string, len(base)+len(overrides))
	for _, item := range base {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		values[key] = value
	}
	for key, value := range overrides {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

func decodeStructuredResult(raw []byte) (engine.ToolResult, bool) {
	var payload struct {
		Content any  `json:"content"`
		IsError bool `json:"is_error"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return engine.ToolResult{}, false
	}
	if payload.Content == nil && !payload.IsError {
		return engine.ToolResult{}, false
	}
	switch value := payload.Content.(type) {
	case string:
		return engine.ToolResult{Content: value, IsError: payload.IsError}, true
	default:
		rendered, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return engine.ToolResult{Content: fmt.Sprint(value), IsError: payload.IsError}, true
		}
		return engine.ToolResult{Content: string(rendered), IsError: payload.IsError}, true
	}
}

func orEmptyObject(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	return raw
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func cwdOrEmpty(execCtx *engine.ExecutionContext) string {
	if execCtx == nil {
		return ""
	}
	return execCtx.WorkingDir
}
