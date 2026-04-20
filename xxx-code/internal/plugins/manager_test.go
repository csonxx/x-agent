package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
)

func TestStartLoadsPluginToolsAndSupportTools(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "echoer", "echo", "#!/bin/sh\ncat\n")

	registry := engine.NewRegistry()
	manager, err := Start(context.Background(), registry, Options{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if manager == nil {
		t.Fatal("expected plugin manager to be created")
	}
	defer func() {
		if err := manager.Close(); err != nil {
			t.Fatalf("close manager: %v", err)
		}
	}()

	if _, ok := registry.Get("list_plugins"); !ok {
		t.Fatal("expected list_plugins support tool")
	}
	if _, ok := registry.Get("reload_plugins"); !ok {
		t.Fatal("expected reload_plugins support tool")
	}
	if _, ok := registry.Get("validate_plugin"); !ok {
		t.Fatal("expected validate_plugin support tool")
	}
	if _, ok := registry.Get("install_plugin"); !ok {
		t.Fatal("expected install_plugin support tool")
	}
	if _, ok := registry.Get("remove_plugin"); !ok {
		t.Fatal("expected remove_plugin support tool")
	}

	tool, ok := registry.Get("plugin__echoer__echo")
	if !ok {
		t.Fatal("expected plugin tool to be registered")
	}
	input, _ := json.Marshal(map[string]any{"value": "hi"})
	result, err := tool.Call(context.Background(), nil, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected successful plugin result, got %+v", result)
	}
	if result.Content != `{"value":"hi"}` {
		t.Fatalf("unexpected plugin output: %q", result.Content)
	}
}

func TestReloadReplacesPluginTools(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "alpha", "echo", "#!/bin/sh\nprintf 'alpha'\n")

	registry := engine.NewRegistry()
	manager, err := Start(context.Background(), registry, Options{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if manager == nil {
		t.Fatal("expected plugin manager to be created")
	}
	defer func() {
		if err := manager.Close(); err != nil {
			t.Fatalf("close manager: %v", err)
		}
	}()

	if _, ok := registry.Get("plugin__alpha__echo"); !ok {
		t.Fatal("expected alpha plugin tool to exist")
	}

	if err := os.RemoveAll(filepath.Join(dir, ".xxx-code", "plugins", "alpha")); err != nil {
		t.Fatal(err)
	}
	writePlugin(t, dir, "beta", "echo", "#!/bin/sh\nprintf 'beta'\n")

	if err := manager.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("plugin__alpha__echo"); ok {
		t.Fatal("expected alpha plugin tool to be removed after reload")
	}
	if _, ok := registry.Get("plugin__beta__echo"); !ok {
		t.Fatal("expected beta plugin tool to be registered after reload")
	}
}

func TestValidateSourceReportsIssues(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "candidate")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "name": "broken",
  "tools": [{
    "name": "echo",
    "description": "",
    "command": "./missing.sh",
    "timeout": "later"
  }]
}`
	if err := os.WriteFile(filepath.Join(sourceDir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	report := ValidateSource(dir, sourceDir)
	if report.Valid {
		t.Fatalf("expected invalid report, got %+v", report)
	}
	if report.PluginName != "broken" {
		t.Fatalf("expected plugin name to be parsed, got %+v", report)
	}
	if report.IssueCount < 2 {
		t.Fatalf("expected multiple issues, got %+v", report)
	}
}

func TestValidateSourceAcceptsAbsoluteCommandPath(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "candidate")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Join(dir, "echo-tool.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "name": "absolute",
  "tools": [{
    "name": "echo",
    "description": "Echo plugin",
    "input_schema": {"type": "object"},
    "command": "` + scriptPath + `"
  }]
}`
	if err := os.WriteFile(filepath.Join(sourceDir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	report := ValidateSource(dir, sourceDir)
	if !report.Valid {
		t.Fatalf("expected absolute command path to validate, got %+v", report)
	}
}

func TestExamplePluginSourcesValidate(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(cwd, "..", ".."))
	matches, err := filepath.Glob(filepath.Join(root, "examples", "plugins", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("expected example plugin sources to exist")
	}

	for _, sourceDir := range matches {
		info, err := os.Stat(sourceDir)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() {
			continue
		}
		t.Run(filepath.Base(sourceDir), func(t *testing.T) {
			report := ValidateSource(root, sourceDir)
			if !report.Valid {
				t.Fatalf("expected example plugin to validate, got %+v", report)
			}
			if report.ToolCount == 0 {
				t.Fatalf("expected example plugin to expose at least one tool, got %+v", report)
			}
		})
	}
}

func TestDemoWorkspacePluginLoadsAndRuns(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(cwd, "..", ".."))
	workspace := filepath.Join(root, "examples", "demo-workspace")

	registry := engine.NewRegistry()
	manager, err := Start(context.Background(), registry, Options{WorkingDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if manager == nil {
		t.Fatal("expected plugin manager to be created for demo workspace")
	}
	defer func() {
		if err := manager.Close(); err != nil {
			t.Fatalf("close manager: %v", err)
		}
	}()

	tool, ok := registry.Get("plugin__demo_helpers__emit_markdown_note")
	if !ok {
		t.Fatal("expected demo workspace plugin tool to be registered")
	}

	input, _ := json.Marshal(map[string]any{
		"title":   "Demo Summary",
		"summary": "This note came from the demo plugin.",
		"bullets": []string{"plugin", "mcp", "workflow"},
	})
	result, err := tool.Call(context.Background(), &engine.ExecutionContext{WorkingDir: workspace}, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected demo plugin result to succeed, got %+v", result)
	}
	if !strings.Contains(result.Content, "# Demo Summary") || !strings.Contains(result.Content, "- plugin") {
		t.Fatalf("unexpected demo plugin output: %q", result.Content)
	}
}

func TestInstallAndRemovePlugin(t *testing.T) {
	dir := t.TempDir()
	sourceDir := writePluginSource(t, filepath.Join(dir, "candidates"), "echoer", "echo", "#!/bin/sh\ncat\n")

	registry := engine.NewRegistry()
	manager, err := Start(context.Background(), registry, Options{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := manager.Close(); err != nil {
			t.Fatalf("close manager: %v", err)
		}
	}()

	if manager.PluginDir() == "" {
		t.Fatal("expected plugin dir to be resolved even before install")
	}
	if manager.PluginCount() != 0 {
		t.Fatalf("expected no loaded plugins before install, got %d", manager.PluginCount())
	}

	if err := manager.Install(context.Background(), sourceDir, false); err != nil {
		t.Fatal(err)
	}
	if manager.PluginCount() != 1 || manager.ToolCount() != 1 {
		t.Fatalf("expected installed plugin summary, got dir=%q plugins=%d tools=%d", manager.PluginDir(), manager.PluginCount(), manager.ToolCount())
	}
	if _, ok := registry.Get("plugin__echoer__echo"); !ok {
		t.Fatal("expected installed plugin tool to be registered")
	}
	if _, err := os.Stat(filepath.Join(manager.PluginDir(), "echoer", "plugin.json")); err != nil {
		t.Fatalf("expected installed plugin manifest in plugin dir: %v", err)
	}

	if err := manager.Remove(context.Background(), "echoer"); err != nil {
		t.Fatal(err)
	}
	if manager.PluginCount() != 0 || manager.ToolCount() != 0 {
		t.Fatalf("expected plugin summary to be empty after removal, got plugins=%d tools=%d", manager.PluginCount(), manager.ToolCount())
	}
	if _, ok := registry.Get("plugin__echoer__echo"); ok {
		t.Fatal("expected plugin tool to be removed after uninstall")
	}
	if _, err := os.Stat(filepath.Join(manager.PluginDir(), "echoer")); !os.IsNotExist(err) {
		t.Fatalf("expected plugin dir to be removed, got err=%v", err)
	}
}

func TestSupportToolsManagePluginLifecycle(t *testing.T) {
	dir := t.TempDir()
	sourceDir := writePluginSource(t, filepath.Join(dir, "candidates"), "echoer", "echo", "#!/bin/sh\ncat\n")

	registry := engine.NewRegistry()
	manager, err := Start(context.Background(), registry, Options{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := manager.Close(); err != nil {
			t.Fatalf("close manager: %v", err)
		}
	}()

	listTool, ok := registry.Get("list_plugins")
	if !ok {
		t.Fatal("expected list_plugins tool to be registered")
	}
	listResult, err := listTool.Call(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listResult.Content, `"plugin_count": 0`) {
		t.Fatalf("expected empty plugin summary, got %s", listResult.Content)
	}

	validateTool, ok := registry.Get("validate_plugin")
	if !ok {
		t.Fatal("expected validate_plugin tool to be registered")
	}
	validateInput, _ := json.Marshal(map[string]any{"source": sourceDir})
	validateResult, err := validateTool.Call(context.Background(), nil, validateInput)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(validateResult.Content, `"plugin_name": "echoer"`) {
		t.Fatalf("expected validation output to mention plugin, got %s", validateResult.Content)
	}

	installTool, ok := registry.Get("install_plugin")
	if !ok {
		t.Fatal("expected install_plugin tool to be registered")
	}
	installInput, _ := json.Marshal(map[string]any{"source": sourceDir})
	installResult, err := installTool.Call(context.Background(), nil, installInput)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(installResult.Content, `"plugin_count": 1`) {
		t.Fatalf("expected installed plugin summary, got %s", installResult.Content)
	}

	reloadTool, ok := registry.Get("reload_plugins")
	if !ok {
		t.Fatal("expected reload_plugins tool to be registered")
	}
	reloadResult, err := reloadTool.Call(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reloadResult.Content, `"echoer"`) {
		t.Fatalf("expected reload summary to include plugin status, got %s", reloadResult.Content)
	}

	removeTool, ok := registry.Get("remove_plugin")
	if !ok {
		t.Fatal("expected remove_plugin tool to be registered")
	}
	removeInput, _ := json.Marshal(map[string]any{"name": "echoer"})
	removeResult, err := removeTool.Call(context.Background(), nil, removeInput)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(removeResult.Content, `"plugin_count": 0`) {
		t.Fatalf("expected empty plugin summary after removal, got %s", removeResult.Content)
	}
}

func writePlugin(t *testing.T, workingDir, pluginName, toolName, script string) {
	t.Helper()
	pluginDir := writePluginSource(t, filepath.Join(workingDir, ".xxx-code", "plugins"), pluginName, toolName, script)
	if pluginDir == "" {
		t.Fatal("expected plugin dir to be created")
	}
}

func writePluginSource(t *testing.T, rootDir, pluginName, toolName, script string) string {
	t.Helper()
	pluginDir := filepath.Join(rootDir, pluginName)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(pluginDir, "tool.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "name": "` + pluginName + `",
  "tools": [{
    "name": "` + toolName + `",
    "description": "Echo plugin",
    "input_schema": {"type": "object"},
    "command": "./tool.sh"
  }]
	}`
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return pluginDir
}
