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

func TestLoadArgsParsesDaemonGovernanceOptions(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"ANTHROPIC_API_KEY":                   "test-key",
		"XXX_CODE_DAEMON_ALLOW_MODES":         "sessions_read,turns",
		"XXX_CODE_DAEMON_DENY_SESSION_PREFIX": "blocked-",
		"XXX_CODE_DAEMON_RATE_LIMIT_BURST":    "12",
	}
	cfg, err := LoadArgs([]string{
		"--daemon-token-file", "secrets/daemon.token",
		"--daemon-audit-file", "logs/audit.jsonl",
		"--daemon-deny-modes", "mcp,audit",
		"--daemon-allow-session-prefix", "team-",
		"--remote-token-file", "secrets/remote.token",
		"--daemon-rate-limit-per-minute", "30",
	}, lookupFromMap(env), dir)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.DaemonAuditFile != filepath.Join(dir, "logs", "audit.jsonl") {
		t.Fatalf("unexpected daemon audit file: %q", cfg.DaemonAuditFile)
	}
	if cfg.DaemonTokenFile != filepath.Join(dir, "secrets", "daemon.token") {
		t.Fatalf("unexpected daemon token file: %q", cfg.DaemonTokenFile)
	}
	if len(cfg.DaemonAllowModes) != 2 || cfg.DaemonAllowModes[0] != "sessions_read" || cfg.DaemonAllowModes[1] != "turns" {
		t.Fatalf("unexpected daemon allow modes: %+v", cfg.DaemonAllowModes)
	}
	if len(cfg.DaemonDenyModes) != 2 || cfg.DaemonDenyModes[0] != "mcp" || cfg.DaemonDenyModes[1] != "audit" {
		t.Fatalf("unexpected daemon deny modes: %+v", cfg.DaemonDenyModes)
	}
	if len(cfg.DaemonAllowSessionPrefixes) != 1 || cfg.DaemonAllowSessionPrefixes[0] != "team-" {
		t.Fatalf("unexpected daemon allow session prefixes: %+v", cfg.DaemonAllowSessionPrefixes)
	}
	if len(cfg.DaemonDenySessionPrefixes) != 1 || cfg.DaemonDenySessionPrefixes[0] != "blocked-" {
		t.Fatalf("unexpected daemon deny session prefixes: %+v", cfg.DaemonDenySessionPrefixes)
	}
	if cfg.RemoteTokenFile != filepath.Join(dir, "secrets", "remote.token") {
		t.Fatalf("unexpected remote token file: %q", cfg.RemoteTokenFile)
	}
	if cfg.DaemonRateLimitPerMinute != 30 || cfg.DaemonRateLimitBurst != 12 {
		t.Fatalf("unexpected daemon rate limit config: per_minute=%d burst=%d", cfg.DaemonRateLimitPerMinute, cfg.DaemonRateLimitBurst)
	}
}

func TestLoadArgsSupportsOpenAIProviderEnv(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"XXX_CODE_PROVIDER": "openai",
		"OPENAI_API_KEY":    "openai-key",
		"OPENAI_BASE_URL":   "https://example.openai.test/v1",
	}

	cfg, err := LoadArgs([]string{"--model", "gpt-4.1"}, lookupFromMap(env), dir)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Provider != "openai" {
		t.Fatalf("unexpected provider: %q", cfg.Provider)
	}
	if cfg.APIKey != "openai-key" {
		t.Fatalf("unexpected api key: %q", cfg.APIKey)
	}
	if cfg.BaseURL != "https://example.openai.test/v1" {
		t.Fatalf("unexpected base url: %q", cfg.BaseURL)
	}
}

func TestLoadArgsSupportsAzureOpenAIProviderEnv(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"XXX_CODE_PROVIDER":     "azure-openai",
		"AZURE_OPENAI_API_KEY":  "azure-key",
		"AZURE_OPENAI_BASE_URL": "https://example-resource.openai.azure.com",
	}

	cfg, err := LoadArgs([]string{"--model", "deployment-name"}, lookupFromMap(env), dir)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Provider != "azure-openai" {
		t.Fatalf("unexpected provider: %q", cfg.Provider)
	}
	if cfg.APIKey != "azure-key" {
		t.Fatalf("unexpected api key: %q", cfg.APIKey)
	}
	if cfg.BaseURL != "https://example-resource.openai.azure.com" {
		t.Fatalf("unexpected base url: %q", cfg.BaseURL)
	}
}

func TestLoadArgsRejectsUnknownProvider(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadArgs([]string{"--provider", "mystery"}, lookupFromMap(map[string]string{
		"XXX_CODE_API_KEY": "test-key",
	}), dir)
	if err == nil {
		t.Fatal("expected unknown provider to fail")
	}
}

func TestLoadArgsParsesHookEventFile(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"ANTHROPIC_API_KEY":        "test-key",
		"XXX_CODE_HOOK_EVENT_FILE": ".xxx-code/hooks/events.jsonl",
	}

	cfg, err := LoadArgs([]string{
		"--hook-event-file", "logs/override.jsonl",
	}, lookupFromMap(env), dir)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.HookEventFile != filepath.Join(dir, "logs", "override.jsonl") {
		t.Fatalf("unexpected hook event file: %q", cfg.HookEventFile)
	}
}

func TestLoadArgsParsesPluginDir(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadArgs([]string{
		"--plugin-dir", "runtime/plugins",
	}, lookupFromMap(map[string]string{
		"ANTHROPIC_API_KEY": "test-key",
	}), dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PluginDir != filepath.Join(dir, "runtime", "plugins") {
		t.Fatalf("unexpected plugin dir: %q", cfg.PluginDir)
	}
}

func lookupFromMap(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
