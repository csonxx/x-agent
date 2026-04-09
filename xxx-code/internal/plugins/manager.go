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

type ValidationReport struct {
	SourcePath   string            `json:"source_path,omitempty"`
	ManifestPath string            `json:"manifest_path,omitempty"`
	PluginName   string            `json:"plugin_name,omitempty"`
	Valid        bool              `json:"valid"`
	ToolCount    int               `json:"tool_count"`
	IssueCount   int               `json:"issue_count"`
	Issues       []ValidationIssue `json:"issues,omitempty"`
}

type ValidationIssue struct {
	Tool    string `json:"tool,omitempty"`
	Level   string `json:"level"`
	Message string `json:"message"`
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
	manager := &Manager{
		registry:  registry,
		options:   options,
		pluginDir: pluginDir,
	}
	manager.registerSupportTools()
	if ok {
		if err := manager.load(ctx, pluginDir); err != nil {
			_ = manager.Close()
			return nil, err
		}
	}
	return manager, nil
}

func (m *Manager) Validate(source string) ValidationReport {
	if m == nil {
		return ValidationReport{}
	}
	m.mu.RLock()
	workingDir := m.options.WorkingDir
	m.mu.RUnlock()
	return ValidateSource(workingDir, source)
}

func (m *Manager) Install(ctx context.Context, source string, force bool) error {
	if m == nil {
		return errors.New("plugins are not configured")
	}
	m.mu.RLock()
	workingDir := m.options.WorkingDir
	explicit := m.options.PluginDir
	m.mu.RUnlock()

	pluginDir, err := EnsurePluginDir(workingDir, explicit)
	if err != nil {
		return err
	}
	if _, err := InstallSource(workingDir, pluginDir, source, force); err != nil {
		return err
	}
	return m.Reload(ctx)
}

func (m *Manager) Remove(ctx context.Context, name string) error {
	if m == nil {
		return errors.New("plugins are not configured")
	}
	m.mu.RLock()
	workingDir := m.options.WorkingDir
	explicit := m.options.PluginDir
	statuses := append([]Status(nil), m.statuses...)
	m.mu.RUnlock()

	pluginDir, err := EnsurePluginDir(workingDir, explicit)
	if err != nil {
		return err
	}
	if err := RemoveInstalled(pluginDir, name, statuses); err != nil {
		return err
	}
	return m.Reload(ctx)
}

func ResolvePluginDir(workingDir, explicit string) (string, bool, error) {
	path, err := desiredPluginDir(workingDir, explicit)
	if err != nil {
		return "", false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return path, false, nil
		}
		return "", false, fmt.Errorf("stat plugin dir %q: %w", path, err)
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("plugin dir is not a directory: %s", path)
	}
	return path, true, nil
}

func EnsurePluginDir(workingDir, explicit string) (string, error) {
	path, err := desiredPluginDir(workingDir, explicit)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", fmt.Errorf("create plugin dir %q: %w", path, err)
	}
	return path, nil
}

func desiredPluginDir(workingDir, explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return expandPath(workingDir, explicit)
	}
	return filepath.Join(workingDir, ".xxx-code", "plugins"), nil
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
		m.pluginDir = pluginDir
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

func ValidateSource(workingDir, source string) ValidationReport {
	report := ValidationReport{}
	manifestPath, sourceRoot, err := resolveSource(workingDir, source)
	if err != nil {
		report.SourcePath = strings.TrimSpace(source)
		report.Issues = append(report.Issues, ValidationIssue{
			Level:   "error",
			Message: err.Error(),
		})
		report.IssueCount = len(report.Issues)
		return report
	}
	report.SourcePath = sourceRoot
	report.ManifestPath = manifestPath

	manifest, err := loadManifest(manifestPath)
	if err != nil {
		report.Issues = append(report.Issues, ValidationIssue{
			Level:   "error",
			Message: err.Error(),
		})
		report.IssueCount = len(report.Issues)
		return report
	}
	report.PluginName = manifest.Name
	report.ToolCount = len(manifest.Tools)

	if strings.TrimSpace(manifest.Name) == "" {
		report.Issues = append(report.Issues, ValidationIssue{
			Level:   "error",
			Message: "plugin name cannot be empty",
		})
	}
	if len(manifest.Tools) == 0 {
		report.Issues = append(report.Issues, ValidationIssue{
			Level:   "error",
			Message: "plugin must define at least one tool",
		})
	}
	seen := map[string]struct{}{}
	for _, tool := range manifest.Tools {
		validateToolSpec(&report, filepath.Dir(manifestPath), tool, seen)
	}
	report.IssueCount = len(report.Issues)
	report.Valid = true
	for _, issue := range report.Issues {
		if issue.Level == "error" {
			report.Valid = false
			return report
		}
	}
	return report
}

func validateToolSpec(report *ValidationReport, manifestDir string, tool CommandToolSpec, seen map[string]struct{}) {
	name := strings.TrimSpace(tool.Name)
	if name == "" {
		report.Issues = append(report.Issues, ValidationIssue{
			Level:   "error",
			Message: "plugin tool name cannot be empty",
		})
		return
	}
	if _, ok := seen[name]; ok {
		report.Issues = append(report.Issues, ValidationIssue{
			Tool:    name,
			Level:   "error",
			Message: "duplicate plugin tool name",
		})
	} else {
		seen[name] = struct{}{}
	}
	if strings.TrimSpace(tool.Command) == "" {
		report.Issues = append(report.Issues, ValidationIssue{
			Tool:    name,
			Level:   "error",
			Message: "plugin tool command cannot be empty",
		})
	}
	if strings.TrimSpace(tool.Description) == "" {
		report.Issues = append(report.Issues, ValidationIssue{
			Tool:    name,
			Level:   "warning",
			Message: "plugin tool description is empty",
		})
	}
	if strings.TrimSpace(tool.Timeout) != "" {
		if _, err := time.ParseDuration(strings.TrimSpace(tool.Timeout)); err != nil {
			report.Issues = append(report.Issues, ValidationIssue{
				Tool:    name,
				Level:   "error",
				Message: "invalid tool timeout: " + err.Error(),
			})
		}
	}
	command := strings.TrimSpace(tool.Command)
	if strings.Contains(command, string(filepath.Separator)) {
		resolved := filepath.Clean(filepath.Join(manifestDir, command))
		info, err := os.Stat(resolved)
		if err != nil {
			report.Issues = append(report.Issues, ValidationIssue{
				Tool:    name,
				Level:   "error",
				Message: "plugin command path not found: " + resolved,
			})
		} else if info.IsDir() {
			report.Issues = append(report.Issues, ValidationIssue{
				Tool:    name,
				Level:   "error",
				Message: "plugin command path is a directory: " + resolved,
			})
		}
	}
	if strings.TrimSpace(tool.Cwd) != "" {
		resolved := filepath.Clean(filepath.Join(manifestDir, strings.TrimSpace(tool.Cwd)))
		info, err := os.Stat(resolved)
		if err != nil {
			report.Issues = append(report.Issues, ValidationIssue{
				Tool:    name,
				Level:   "error",
				Message: "plugin cwd not found: " + resolved,
			})
		} else if !info.IsDir() {
			report.Issues = append(report.Issues, ValidationIssue{
				Tool:    name,
				Level:   "error",
				Message: "plugin cwd is not a directory: " + resolved,
			})
		}
	}
}

func InstallSource(workingDir, pluginDir, source string, force bool) (string, error) {
	manifestPath, sourceRoot, err := resolveSource(workingDir, source)
	if err != nil {
		return "", err
	}
	report := ValidateSource(workingDir, source)
	if !report.Valid {
		return "", fmt.Errorf("plugin validation failed: %s", validationIssueSummary(report))
	}
	manifest, err := loadManifest(manifestPath)
	if err != nil {
		return "", err
	}

	destDir := filepath.Join(pluginDir, normalizeName(manifest.Name))
	if _, err := os.Stat(destDir); err == nil {
		if !force {
			return "", fmt.Errorf("plugin already exists: %s", manifest.Name)
		}
		if err := os.RemoveAll(destDir); err != nil {
			return "", fmt.Errorf("remove existing plugin %s: %w", manifest.Name, err)
		}
	}
	if err := copyDir(sourceRoot, destDir); err != nil {
		return "", err
	}
	return manifest.Name, nil
}

func validationIssueSummary(report ValidationReport) string {
	if len(report.Issues) == 0 {
		return "unknown validation error"
	}
	parts := make([]string, 0, len(report.Issues))
	for _, issue := range report.Issues {
		message := strings.TrimSpace(issue.Message)
		if message == "" {
			continue
		}
		if tool := strings.TrimSpace(issue.Tool); tool != "" {
			message = tool + ": " + message
		}
		if level := strings.TrimSpace(issue.Level); level != "" {
			message = "[" + level + "] " + message
		}
		parts = append(parts, message)
		if len(parts) == 3 {
			break
		}
	}
	if len(parts) == 0 {
		return "unknown validation error"
	}
	if len(report.Issues) > len(parts) {
		return strings.Join(parts, "; ") + fmt.Sprintf(" (+%d more)", len(report.Issues)-len(parts))
	}
	return strings.Join(parts, "; ")
}

func RemoveInstalled(pluginDir, name string, statuses []Status) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("plugin name is required")
	}
	for _, status := range statuses {
		if status.Name == name && strings.TrimSpace(status.ManifestPath) != "" {
			target := filepath.Dir(status.ManifestPath)
			if err := os.RemoveAll(target); err != nil {
				return fmt.Errorf("remove plugin %s: %w", name, err)
			}
			return nil
		}
	}
	target := filepath.Join(pluginDir, normalizeName(name))
	if _, err := os.Stat(target); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("plugin not found: %s", name)
		}
		return err
	}
	return os.RemoveAll(target)
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

func resolveSource(workingDir, source string) (manifestPath string, sourceRoot string, err error) {
	path, err := expandPath(workingDir, source)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", "", fmt.Errorf("stat plugin source %q: %w", path, err)
	}
	if info.IsDir() {
		manifestPath, err = manifestInDir(path)
		if err != nil {
			return "", "", err
		}
		return manifestPath, path, nil
	}
	return path, filepath.Dir(path), nil
}

func manifestInDir(dir string) (string, error) {
	primary := filepath.Join(dir, "plugin.json")
	if info, err := os.Stat(primary); err == nil && !info.IsDir() {
		return primary, nil
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.plugin.json"))
	if err != nil {
		return "", err
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("multiple plugin manifests found in %s", dir)
	}
	return "", fmt.Errorf("no plugin manifest found in %s", dir)
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
	_ = m.registry.AddTool(&validateTool{manager: m})
	_ = m.registry.AddTool(&installTool{manager: m})
	_ = m.registry.AddTool(&removeTool{manager: m})
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

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
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
