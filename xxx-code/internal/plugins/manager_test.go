package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
