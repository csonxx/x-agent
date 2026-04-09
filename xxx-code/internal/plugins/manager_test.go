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

func writePlugin(t *testing.T, workingDir, pluginName, toolName, script string) {
	t.Helper()
	pluginDir := filepath.Join(workingDir, ".xxx-code", "plugins", pluginName)
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
}
