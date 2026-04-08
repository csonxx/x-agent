package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/caowenhua/x-agent/xxx-code/internal/diag"
)

func TestLoadArgsAppliesConfigEnvAndFlagsInOrder(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".xxx-code")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.json")
	configBody := `{
  "model": "file-model",
  "max_turns": 5,
  "allow_read": ["docs"],
  "log_level": "error",
  "log_file": ".xxx-code/diag.log",
  "system_prompt": "from config"
}`
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{
		"ANTHROPIC_API_KEY":  "test-key",
		"XXX_CODE_MODEL":     "env-model",
		"XXX_CODE_LOG_LEVEL": "info",
	}
	cfg, err := LoadArgs([]string{
		"--model", "flag-model",
		"--debug",
		"--session-file", "sessions/main.json",
	}, lookupFromMap(env), dir)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Model != "flag-model" {
		t.Fatalf("expected flag value to win, got %q", cfg.Model)
	}
	if cfg.MaxTurns != 5 {
		t.Fatalf("expected config file value to apply, got %d", cfg.MaxTurns)
	}
	if cfg.LogLevel != diag.LevelDebug {
		t.Fatalf("expected debug flag to override env and file log level, got %s", cfg.LogLevel)
	}
	if cfg.ConfigFile != configPath {
		t.Fatalf("expected auto-discovered config path %q, got %q", configPath, cfg.ConfigFile)
	}
	if cfg.SessionFile != filepath.Join(dir, "sessions", "main.json") {
		t.Fatalf("unexpected session file: %q", cfg.SessionFile)
	}
	if cfg.LogFile != filepath.Join(dir, ".xxx-code", "diag.log") {
		t.Fatalf("unexpected log file path: %q", cfg.LogFile)
	}
	if cfg.SystemPrompt != "from config" {
		t.Fatalf("expected config file system prompt, got %q", cfg.SystemPrompt)
	}
	if len(cfg.ReadRoots) != 2 || cfg.ReadRoots[0] != dir || cfg.ReadRoots[1] != filepath.Join(dir, "docs") {
		t.Fatalf("unexpected read roots: %+v", cfg.ReadRoots)
	}
}

func TestLoadArgsVersionModesDoNotRequireAPIKey(t *testing.T) {
	dir := t.TempDir()

	cfg, err := LoadArgs([]string{"--version"}, lookupFromMap(nil), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ShowVersion {
		t.Fatal("expected --version to enable version mode")
	}

	cfg, err = LoadArgs([]string{"version"}, lookupFromMap(nil), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ShowVersion {
		t.Fatal("expected version subcommand to enable version mode")
	}
}

func lookupFromMap(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
