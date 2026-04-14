package config

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/diag"
	"gopkg.in/yaml.v3"
)

const defaultSystemPrompt = `You are xxx-code, a Go-built coding agent inspired by Claude Code.

Your job is to help with software engineering tasks using the available tools.

Guidelines:
- Read files before changing them when the task depends on existing code.
- Prefer the smallest correct change over speculative refactors.
- Use bash for shell tasks, read_file/write_file/edit_file for direct file work, glob/grep for search.
- MCP tools may be available with names like mcp__server__tool; use them when they clearly fit the task.
- Use agent_spawn only when a sub-task is clearly separable and benefits from parallel or isolated execution.
- If you spawn a background agent, use agent_wait or agent_list to integrate its result before you finish.
- Be explicit about verification. If you did not run a check, say so.
- Keep final user-facing answers concise and practical.`

type Config struct {
	Provider                   string
	APIKey                     string
	BaseURL                    string
	Version                    string
	Model                      string
	MaxTurns                   int
	MaxTokens                  int
	MaxParallelAgents          int
	ContextBudget              int
	CompactKeep                int
	Daemon                     bool
	DaemonListenAddr           string
	DaemonToken                string
	DaemonTokenFile            string
	DaemonDir                  string
	DaemonAuditFile            string
	DaemonAllowModes           []string
	DaemonDenyModes            []string
	DaemonAllowSessionPrefixes []string
	DaemonDenySessionPrefixes  []string
	DaemonRateLimitPerMinute   int
	DaemonRateLimitBurst       int
	RemoteURL                  string
	RemoteToken                string
	RemoteTokenFile            string
	RemoteSession              string
	RemoteList                 bool
	WorkingDir                 string
	SessionFile                string
	MCPConfigFile              string
	PluginDir                  string
	ReadRoots                  []string
	WriteRoots                 []string
	AllowedTools               []string
	BlockedTools               []string
	BashAllowPrefixes          []string
	BashDenyPrefixes           []string
	ReadOnly                   bool
	BashEnabled                bool
	HookBeforeTool             string
	HookAfterTool              string
	HookAfterTurn              string
	HookAgentEvent             string
	HookEventFile              string
	HookTimeout                time.Duration
	Resume                     bool
	Print                      bool
	TUI                        bool
	Stream                     bool
	Verbose                    bool
	LogLevel                   diag.Level
	LogFile                    string
	ConfigFile                 string
	ShowVersion                bool
	SystemPrompt               string
	ToolTimeout                time.Duration
	Prompt                     string
}

type fileConfig struct {
	Provider                   *string  `json:"provider,omitempty"`
	APIKey                     *string  `json:"api_key,omitempty"`
	BaseURL                    *string  `json:"base_url,omitempty"`
	APIVersion                 *string  `json:"api_version,omitempty"`
	AnthropicVersion           *string  `json:"anthropic_version,omitempty"`
	Model                      *string  `json:"model,omitempty"`
	MaxTurns                   *int     `json:"max_turns,omitempty"`
	MaxTokens                  *int     `json:"max_tokens,omitempty"`
	MaxParallelAgents          *int     `json:"max_parallel_agents,omitempty"`
	ContextBudget              *int     `json:"context_budget,omitempty"`
	CompactKeep                *int     `json:"compact_keep,omitempty"`
	Daemon                     *bool    `json:"daemon,omitempty"`
	Listen                     *string  `json:"listen,omitempty"`
	DaemonToken                *string  `json:"daemon_token,omitempty"`
	DaemonTokenFile            *string  `json:"daemon_token_file,omitempty"`
	DaemonDir                  *string  `json:"daemon_dir,omitempty"`
	DaemonAuditFile            *string  `json:"daemon_audit_file,omitempty"`
	DaemonAllowModes           []string `json:"daemon_allow_modes,omitempty"`
	DaemonDenyModes            []string `json:"daemon_deny_modes,omitempty"`
	DaemonAllowSessionPrefixes []string `json:"daemon_allow_session_prefix,omitempty"`
	DaemonDenySessionPrefixes  []string `json:"daemon_deny_session_prefix,omitempty"`
	DaemonRateLimitPerMinute   *int     `json:"daemon_rate_limit_per_minute,omitempty"`
	DaemonRateLimitBurst       *int     `json:"daemon_rate_limit_burst,omitempty"`
	RemoteURL                  *string  `json:"remote_url,omitempty"`
	RemoteToken                *string  `json:"remote_token,omitempty"`
	RemoteTokenFile            *string  `json:"remote_token_file,omitempty"`
	RemoteSession              *string  `json:"remote_session,omitempty"`
	RemoteList                 *bool    `json:"remote_list_sessions,omitempty"`
	WorkingDir                 *string  `json:"cwd,omitempty"`
	SessionFile                *string  `json:"session_file,omitempty"`
	MCPConfigFile              *string  `json:"mcp_config,omitempty"`
	PluginDir                  *string  `json:"plugin_dir,omitempty"`
	AllowRead                  []string `json:"allow_read,omitempty"`
	AllowWrite                 []string `json:"allow_write,omitempty"`
	AllowTools                 []string `json:"allow_tools,omitempty"`
	DenyTools                  []string `json:"deny_tools,omitempty"`
	AllowBashPrefix            []string `json:"allow_bash_prefix,omitempty"`
	DenyBashPrefix             []string `json:"deny_bash_prefix,omitempty"`
	ReadOnly                   *bool    `json:"read_only,omitempty"`
	BashEnabled                *bool    `json:"bash,omitempty"`
	HookBeforeTool             *string  `json:"hook_before_tool,omitempty"`
	HookAfterTool              *string  `json:"hook_after_tool,omitempty"`
	HookAfterTurn              *string  `json:"hook_after_turn,omitempty"`
	HookAgentEvent             *string  `json:"hook_agent_event,omitempty"`
	HookEventFile              *string  `json:"hook_event_file,omitempty"`
	HookTimeout                *string  `json:"hook_timeout,omitempty"`
	ToolTimeout                *string  `json:"tool_timeout,omitempty"`
	Resume                     *bool    `json:"resume,omitempty"`
	Print                      *bool    `json:"print,omitempty"`
	TUI                        *bool    `json:"tui,omitempty"`
	Stream                     *bool    `json:"stream,omitempty"`
	Verbose                    *bool    `json:"verbose,omitempty"`
	Debug                      *bool    `json:"debug,omitempty"`
	LogLevel                   *string  `json:"log_level,omitempty"`
	LogFile                    *string  `json:"log_file,omitempty"`
	SystemPrompt               *string  `json:"system_prompt,omitempty"`
	SystemPromptFile           *string  `json:"system_prompt_file,omitempty"`
	Prompt                     *string  `json:"prompt,omitempty"`
}

type rawOptions struct {
	workingDir                 string
	sessionFile                string
	daemonTokenFile            string
	daemonDir                  string
	daemonAuditFile            string
	remoteTokenFile            string
	mcpConfigFile              string
	pluginDir                  string
	systemPromptFile           string
	readRoots                  []string
	writeRoots                 []string
	allowedTools               []string
	blockedTools               []string
	bashAllow                  []string
	bashDeny                   []string
	daemonAllowModes           []string
	daemonDenyModes            []string
	daemonAllowSessionPrefixes []string
	daemonDenySessionPrefixes  []string
	hookEventFile              string
}

type HelpError struct {
	Usage string
}

func (e *HelpError) Error() string {
	return "help requested"
}

func LoadArgs(args []string, lookup func(string) (string, bool), currentWD string) (Config, error) {
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	currentWD = filepath.Clean(currentWD)

	cfg := defaultConfig()
	raw := rawOptions{
		workingDir: currentWD,
	}

	if versionMode(args) {
		cfg.ShowVersion = true
		return cfg, nil
	}

	bootstrapWD := currentWD
	if cwdValue, ok := findStringFlag(args, "cwd"); ok && strings.TrimSpace(cwdValue) != "" {
		bootstrapWD = resolvePath(currentWD, cwdValue)
	}

	configPath, err := resolveConfigPath(args, lookup, bootstrapWD)
	if err != nil {
		return Config{}, err
	}
	if configPath != "" {
		fileCfg, err := loadFileConfig(configPath)
		if err != nil {
			return Config{}, err
		}
		cfg.ConfigFile = configPath
		applyFileConfig(&cfg, &raw, fileCfg, filepath.Dir(configPath))
	}
	providerAfterFile := normalizeProviderName(cfg.Provider)

	if err := applyEnvConfig(&cfg, &raw, lookup); err != nil {
		return Config{}, err
	}
	providerAfterEnv := normalizeProviderName(cfg.Provider)

	fs := flag.NewFlagSet("xxx-code", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	logLevelValue := cfg.LogLevel.String()
	debugDefault := cfg.LogLevel == diag.LevelDebug

	fs.StringVar(&cfg.Provider, "provider", cfg.Provider, "Model provider to use: anthropic, openai, gpt, azure-openai, gemini, minimax, or glm")
	fs.StringVar(&cfg.APIKey, "api-key", cfg.APIKey, "API key for the selected provider")
	fs.StringVar(&cfg.Model, "model", cfg.Model, "Model or deployment name to use")
	fs.StringVar(&cfg.BaseURL, "base-url", cfg.BaseURL, "Base URL for the selected provider API")
	fs.StringVar(&cfg.Version, "anthropic-version", cfg.Version, "Anthropic API version header")
	fs.IntVar(&cfg.MaxTurns, "max-turns", cfg.MaxTurns, "Maximum agentic turns per user prompt")
	fs.IntVar(&cfg.MaxTokens, "max-tokens", cfg.MaxTokens, "Max output tokens per model request")
	fs.IntVar(&cfg.MaxParallelAgents, "max-parallel-agents", cfg.MaxParallelAgents, "Maximum number of sub-agents that can run concurrently")
	fs.IntVar(&cfg.ContextBudget, "context-budget", cfg.ContextBudget, "Approximate context token budget before automatic compaction; set 0 to disable")
	fs.IntVar(&cfg.CompactKeep, "compact-keep", cfg.CompactKeep, "How many latest messages to keep verbatim during automatic compaction")
	fs.BoolVar(&cfg.Daemon, "daemon", cfg.Daemon, "Run xxx-code as a persistent HTTP daemon")
	fs.StringVar(&cfg.DaemonListenAddr, "listen", cfg.DaemonListenAddr, "Listen address for daemon mode")
	fs.StringVar(&cfg.DaemonToken, "daemon-token", cfg.DaemonToken, "Optional bearer token required by the daemon for /v1/* requests")
	fs.StringVar(&cfg.RemoteToken, "remote-token", cfg.RemoteToken, "Bearer token to send when connecting to a protected daemon")
	fs.IntVar(&cfg.DaemonRateLimitPerMinute, "daemon-rate-limit-per-minute", cfg.DaemonRateLimitPerMinute, "Optional per-client request rate limit for daemon /v1/* requests")
	fs.IntVar(&cfg.DaemonRateLimitBurst, "daemon-rate-limit-burst", cfg.DaemonRateLimitBurst, "Burst capacity for the daemon per-client rate limiter")
	fs.StringVar(&cfg.RemoteURL, "remote-url", cfg.RemoteURL, "Daemon base URL to use as a remote bridge")
	fs.StringVar(&cfg.RemoteSession, "remote-session", cfg.RemoteSession, "Remote daemon session ID to open or create")
	fs.BoolVar(&cfg.RemoteList, "remote-list-sessions", cfg.RemoteList, "List daemon sessions instead of running a local session")
	fs.BoolVar(&cfg.ReadOnly, "read-only", cfg.ReadOnly, "Best-effort block file writes, including write_file/edit_file and bash commands that look like writes")
	fs.BoolVar(&cfg.BashEnabled, "bash", cfg.BashEnabled, "Enable or disable the bash tool")
	fs.BoolVar(&cfg.Print, "print", cfg.Print, "Run once and exit")
	fs.BoolVar(&cfg.TUI, "tui", cfg.TUI, "Run an interactive terminal UI instead of the line-oriented REPL")
	fs.BoolVar(&cfg.Stream, "stream", cfg.Stream, "Stream assistant text as it is generated when the provider supports it")
	fs.BoolVar(&cfg.Verbose, "verbose", cfg.Verbose, "Print tool and agent lifecycle events")
	fs.BoolVar(&cfg.Resume, "resume", cfg.Resume, "Resume the main session and known agents from the session file")
	fs.DurationVar(&cfg.ToolTimeout, "tool-timeout", cfg.ToolTimeout, "Per-tool execution timeout")
	fs.DurationVar(&cfg.HookTimeout, "hook-timeout", cfg.HookTimeout, "Timeout for each configured hook command")
	fs.StringVar(&logLevelValue, "log-level", logLevelValue, "Log level for diagnostics: error, info, or debug")
	fs.BoolVar(&debugDefault, "debug", debugDefault, "Shortcut for --log-level=debug")
	fs.BoolVar(&cfg.ShowVersion, "version", false, "Print build version information and exit")

	configFileFlag := fs.String("config", cfg.ConfigFile, "Path to a YAML or JSON config file; defaults to .xxx-code/config.yaml when present")
	systemPromptFileFlag := fs.String("system-prompt-file", raw.systemPromptFile, "Read the system prompt from a file")
	cwdFlag := fs.String("cwd", raw.workingDir, "Working directory")
	sessionFileFlag := fs.String("session-file", raw.sessionFile, "Path to the persisted session file")
	daemonTokenFileFlag := fs.String("daemon-token-file", raw.daemonTokenFile, "Path to a bearer token file for daemon auth; supports rotation without restart")
	daemonDirFlag := fs.String("daemon-dir", raw.daemonDir, "Directory for daemon-managed remote sessions")
	daemonAuditFileFlag := fs.String("daemon-audit-file", raw.daemonAuditFile, "Path to the daemon JSONL audit log; defaults to <daemon-dir>/audit.jsonl")
	remoteTokenFileFlag := fs.String("remote-token-file", raw.remoteTokenFile, "Path to a bearer token file used by the remote bridge; reread on each request")
	mcpConfigFlag := fs.String("mcp-config", raw.mcpConfigFile, "Path to an MCP config file; defaults to .mcp.json in the working directory when present")
	pluginDirFlag := fs.String("plugin-dir", raw.pluginDir, "Path to a plugin directory; defaults to .xxx-code/plugins in the working directory when present")
	readRootsFlag := fs.String("allow-read", joinCSV(raw.readRoots), "Comma-separated read roots; the working directory is always included")
	writeRootsFlag := fs.String("allow-write", joinCSV(raw.writeRoots), "Comma-separated write roots; the working directory is always included unless --read-only is set")
	allowToolsFlag := fs.String("allow-tools", joinCSV(raw.allowedTools), "Comma-separated tool allowlist; when set, only these tools may run")
	denyToolsFlag := fs.String("deny-tools", joinCSV(raw.blockedTools), "Comma-separated tool denylist")
	allowBashPrefixFlag := fs.String("allow-bash-prefix", joinCSV(raw.bashAllow), "Comma-separated allowed bash command prefixes")
	denyBashPrefixFlag := fs.String("deny-bash-prefix", joinCSV(raw.bashDeny), "Comma-separated blocked bash command prefixes")
	daemonAllowModesFlag := fs.String("daemon-allow-modes", joinCSV(raw.daemonAllowModes), "Comma-separated daemon API mode allowlist: sessions_read,sessions_write,turns,introspection,plugins,mcp,agents,workflows,audit,save")
	daemonDenyModesFlag := fs.String("daemon-deny-modes", joinCSV(raw.daemonDenyModes), "Comma-separated daemon API mode denylist")
	daemonAllowSessionPrefixFlag := fs.String("daemon-allow-session-prefix", joinCSV(raw.daemonAllowSessionPrefixes), "Comma-separated session ID prefixes that the daemon may access")
	daemonDenySessionPrefixFlag := fs.String("daemon-deny-session-prefix", joinCSV(raw.daemonDenySessionPrefixes), "Comma-separated session ID prefixes that the daemon must reject")
	hookBeforeToolFlag := fs.String("hook-before-tool", cfg.HookBeforeTool, "Shell command to run before each tool call; non-zero exit blocks the tool")
	hookAfterToolFlag := fs.String("hook-after-tool", cfg.HookAfterTool, "Shell command to run after each tool call")
	hookAfterTurnFlag := fs.String("hook-after-turn", cfg.HookAfterTurn, "Shell command to run after each turn")
	hookAgentEventFlag := fs.String("hook-agent-event", cfg.HookAgentEvent, "Shell command to run for agent lifecycle events")
	hookEventFileFlag := fs.String("hook-event-file", raw.hookEventFile, "Append hook events as JSONL to this file")
	logFileFlag := fs.String("log-file", cfg.LogFile, "Append diagnostic logs and stderr output to this file")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cfg, &HelpError{Usage: buildFlagUsage(fs)}
		}
		return Config{}, err
	}

	visited := visitedFlags(fs)
	if cfg.ShowVersion {
		return cfg, nil
	}

	if visited["log-level"] {
		level, err := diag.ParseLevel(logLevelValue)
		if err != nil {
			return Config{}, err
		}
		cfg.LogLevel = level
	}
	if visited["debug"] {
		if debugDefault {
			cfg.LogLevel = diag.LevelDebug
		} else if !visited["log-level"] {
			cfg.LogLevel = diag.LevelInfo
		}
	}

	providerChangedByHigherPrecedence := providerAfterEnv != providerAfterFile
	if visited["provider"] && normalizeProviderName(cfg.Provider) != providerAfterEnv {
		providerChangedByHigherPrecedence = true
	}
	if providerChangedByHigherPrecedence {
		if !visited["api-key"] && !hasNonEmptyEnv(lookup, "XXX_CODE_API_KEY") {
			cfg.APIKey = ""
		}
		if !visited["base-url"] && !hasNonEmptyEnv(lookup, "XXX_CODE_BASE_URL") {
			cfg.BaseURL = ""
		}
	}
	applySelectedProviderEnvConfig(&cfg, lookup, providerEnvOptions{
		AllowAPIKey:  !visited["api-key"] && !hasNonEmptyEnv(lookup, "XXX_CODE_API_KEY"),
		AllowBaseURL: !visited["base-url"] && !hasNonEmptyEnv(lookup, "XXX_CODE_BASE_URL"),
		AllowVersion: !visited["anthropic-version"] && !hasNonEmptyEnv(lookup, "XXX_CODE_API_VERSION"),
	})

	cfg.WorkingDir = filepath.Clean(resolvePath(currentWD, *cwdFlag))
	cfg.SessionFile = defaultSessionFile(cfg.WorkingDir, *sessionFileFlag)
	cfg.DaemonTokenFile = resolveOptionalPath(cfg.WorkingDir, *daemonTokenFileFlag)
	cfg.DaemonDir = defaultDaemonDir(cfg.WorkingDir, *daemonDirFlag)
	cfg.DaemonAuditFile = defaultDaemonAuditFile(cfg.WorkingDir, cfg.DaemonDir, *daemonAuditFileFlag)
	cfg.RemoteTokenFile = resolveOptionalPath(cfg.WorkingDir, *remoteTokenFileFlag)
	cfg.LogFile = resolveOptionalPath(cfg.WorkingDir, *logFileFlag)
	cfg.ConfigFile = strings.TrimSpace(*configFileFlag)
	if cfg.ConfigFile != "" {
		cfg.ConfigFile = resolvePath(currentWD, cfg.ConfigFile)
	}
	cfg.MCPConfigFile = resolveOptionalPath(cfg.WorkingDir, *mcpConfigFlag)
	cfg.PluginDir = resolveOptionalPath(cfg.WorkingDir, *pluginDirFlag)
	cfg.ReadRoots = appendUniquePaths([]string{cfg.WorkingDir}, parseRoots(cfg.WorkingDir, *readRootsFlag)...)
	cfg.WriteRoots = appendUniquePaths([]string{cfg.WorkingDir}, parseRoots(cfg.WorkingDir, *writeRootsFlag)...)
	cfg.AllowedTools = parseCSV(*allowToolsFlag)
	cfg.BlockedTools = parseCSV(*denyToolsFlag)
	cfg.BashAllowPrefixes = parseCSV(*allowBashPrefixFlag)
	cfg.BashDenyPrefixes = parseCSV(*denyBashPrefixFlag)
	cfg.DaemonAllowModes = parseCSV(*daemonAllowModesFlag)
	cfg.DaemonDenyModes = parseCSV(*daemonDenyModesFlag)
	cfg.DaemonAllowSessionPrefixes = parseCSV(*daemonAllowSessionPrefixFlag)
	cfg.DaemonDenySessionPrefixes = parseCSV(*daemonDenySessionPrefixFlag)
	cfg.HookBeforeTool = strings.TrimSpace(*hookBeforeToolFlag)
	cfg.HookAfterTool = strings.TrimSpace(*hookAfterToolFlag)
	cfg.HookAfterTurn = strings.TrimSpace(*hookAfterTurnFlag)
	cfg.HookAgentEvent = strings.TrimSpace(*hookAgentEventFlag)
	cfg.HookEventFile = resolveOptionalPath(cfg.WorkingDir, *hookEventFileFlag)

	if strings.TrimSpace(cfg.SystemPrompt) == "" {
		cfg.SystemPrompt = defaultSystemPrompt
	}
	if systemPromptFile := resolveOptionalPath(cfg.WorkingDir, *systemPromptFileFlag); systemPromptFile != "" {
		data, err := os.ReadFile(systemPromptFile)
		if err != nil {
			return Config{}, err
		}
		cfg.SystemPrompt = string(data)
	}

	if prompt := strings.TrimSpace(strings.Join(fs.Args(), " ")); prompt != "" {
		cfg.Prompt = prompt
		cfg.Print = true
	}
	cfg.Prompt = strings.TrimSpace(cfg.Prompt)
	if cfg.Prompt != "" {
		cfg.Print = true
	}

	if err := validateProviderConfig(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func defaultConfig() Config {
	return Config{
		Provider:             "anthropic",
		Version:              "2023-06-01",
		Model:                "claude-sonnet-4-5",
		MaxTurns:             12,
		MaxTokens:            16384,
		MaxParallelAgents:    4,
		ContextBudget:        120000,
		CompactKeep:          12,
		DaemonListenAddr:     "127.0.0.1:7331",
		DaemonRateLimitBurst: 10,
		BashEnabled:          true,
		Stream:               true,
		HookTimeout:          30 * time.Second,
		ToolTimeout:          2 * time.Minute,
		SystemPrompt:         defaultSystemPrompt,
		LogLevel:             diag.LevelInfo,
	}
}

func applyFileConfig(cfg *Config, raw *rawOptions, file fileConfig, configDir string) {
	applyString(&cfg.Provider, file.Provider)
	applyString(&cfg.APIKey, file.APIKey)
	applyString(&cfg.BaseURL, file.BaseURL)
	applyString(&cfg.Version, file.APIVersion)
	applyString(&cfg.Version, file.AnthropicVersion)
	applyString(&cfg.Model, file.Model)
	applyInt(&cfg.MaxTurns, file.MaxTurns)
	applyInt(&cfg.MaxTokens, file.MaxTokens)
	applyInt(&cfg.MaxParallelAgents, file.MaxParallelAgents)
	applyInt(&cfg.ContextBudget, file.ContextBudget)
	applyInt(&cfg.CompactKeep, file.CompactKeep)
	applyBool(&cfg.Daemon, file.Daemon)
	applyString(&cfg.DaemonListenAddr, file.Listen)
	applyString(&cfg.DaemonToken, file.DaemonToken)
	applyString(&cfg.RemoteToken, file.RemoteToken)
	applyInt(&cfg.DaemonRateLimitPerMinute, file.DaemonRateLimitPerMinute)
	applyInt(&cfg.DaemonRateLimitBurst, file.DaemonRateLimitBurst)
	applyString(&cfg.RemoteURL, file.RemoteURL)
	applyString(&cfg.RemoteSession, file.RemoteSession)
	applyBool(&cfg.RemoteList, file.RemoteList)
	applyBool(&cfg.ReadOnly, file.ReadOnly)
	applyBool(&cfg.BashEnabled, file.BashEnabled)
	applyBool(&cfg.Resume, file.Resume)
	applyBool(&cfg.Print, file.Print)
	applyBool(&cfg.TUI, file.TUI)
	applyBool(&cfg.Stream, file.Stream)
	applyBool(&cfg.Verbose, file.Verbose)
	applyString(&cfg.SystemPrompt, file.SystemPrompt)
	applyString(&cfg.Prompt, file.Prompt)
	applyString(&cfg.HookBeforeTool, file.HookBeforeTool)
	applyString(&cfg.HookAfterTool, file.HookAfterTool)
	applyString(&cfg.HookAfterTurn, file.HookAfterTurn)
	applyString(&cfg.HookAgentEvent, file.HookAgentEvent)
	applyString(&cfg.LogFile, file.LogFile)
	if file.WorkingDir != nil {
		raw.workingDir = resolvePath(configDir, *file.WorkingDir)
	}
	if file.SessionFile != nil {
		raw.sessionFile = strings.TrimSpace(*file.SessionFile)
	}
	if file.DaemonTokenFile != nil {
		raw.daemonTokenFile = strings.TrimSpace(*file.DaemonTokenFile)
	}
	if file.DaemonDir != nil {
		raw.daemonDir = strings.TrimSpace(*file.DaemonDir)
	}
	if file.DaemonAuditFile != nil {
		raw.daemonAuditFile = strings.TrimSpace(*file.DaemonAuditFile)
	}
	if file.RemoteTokenFile != nil {
		raw.remoteTokenFile = strings.TrimSpace(*file.RemoteTokenFile)
	}
	if file.HookEventFile != nil {
		raw.hookEventFile = strings.TrimSpace(*file.HookEventFile)
	}
	if file.MCPConfigFile != nil {
		raw.mcpConfigFile = strings.TrimSpace(*file.MCPConfigFile)
	}
	if file.PluginDir != nil {
		raw.pluginDir = strings.TrimSpace(*file.PluginDir)
	}
	if file.SystemPromptFile != nil {
		raw.systemPromptFile = strings.TrimSpace(*file.SystemPromptFile)
	}
	if file.AllowRead != nil {
		raw.readRoots = append([]string(nil), file.AllowRead...)
	}
	if file.AllowWrite != nil {
		raw.writeRoots = append([]string(nil), file.AllowWrite...)
	}
	if file.AllowTools != nil {
		raw.allowedTools = append([]string(nil), file.AllowTools...)
	}
	if file.DenyTools != nil {
		raw.blockedTools = append([]string(nil), file.DenyTools...)
	}
	if file.DaemonAllowModes != nil {
		raw.daemonAllowModes = append([]string(nil), file.DaemonAllowModes...)
	}
	if file.DaemonDenyModes != nil {
		raw.daemonDenyModes = append([]string(nil), file.DaemonDenyModes...)
	}
	if file.DaemonAllowSessionPrefixes != nil {
		raw.daemonAllowSessionPrefixes = append([]string(nil), file.DaemonAllowSessionPrefixes...)
	}
	if file.DaemonDenySessionPrefixes != nil {
		raw.daemonDenySessionPrefixes = append([]string(nil), file.DaemonDenySessionPrefixes...)
	}
	if file.AllowBashPrefix != nil {
		raw.bashAllow = append([]string(nil), file.AllowBashPrefix...)
	}
	if file.DenyBashPrefix != nil {
		raw.bashDeny = append([]string(nil), file.DenyBashPrefix...)
	}
	if file.HookTimeout != nil {
		if duration, err := time.ParseDuration(strings.TrimSpace(*file.HookTimeout)); err == nil {
			cfg.HookTimeout = duration
		}
	}
	if file.ToolTimeout != nil {
		if duration, err := time.ParseDuration(strings.TrimSpace(*file.ToolTimeout)); err == nil {
			cfg.ToolTimeout = duration
		}
	}
	if file.LogLevel != nil {
		if level, err := diag.ParseLevel(*file.LogLevel); err == nil {
			cfg.LogLevel = level
		}
	}
	if file.Debug != nil && *file.Debug {
		cfg.LogLevel = diag.LevelDebug
	}
}

func applyEnvConfig(cfg *Config, raw *rawOptions, lookup func(string) (string, bool)) error {
	if value, ok := lookup("XXX_CODE_PROVIDER"); ok {
		cfg.Provider = strings.TrimSpace(value)
	}
	if value, ok := lookup("XXX_CODE_API_KEY"); ok {
		cfg.APIKey = strings.TrimSpace(value)
	}
	if value, ok := lookup("XXX_CODE_BASE_URL"); ok {
		cfg.BaseURL = strings.TrimSpace(value)
	}
	if value, ok := lookup("XXX_CODE_API_VERSION"); ok {
		cfg.Version = strings.TrimSpace(value)
	}
	if value, ok := lookup("XXX_CODE_MODEL"); ok {
		cfg.Model = strings.TrimSpace(value)
	}
	if value, ok := lookup("XXX_CODE_LISTEN"); ok {
		cfg.DaemonListenAddr = strings.TrimSpace(value)
	}
	if value, ok := lookup("XXX_CODE_DAEMON_TOKEN"); ok {
		cfg.DaemonToken = strings.TrimSpace(value)
	}
	if value, ok := lookup("XXX_CODE_DAEMON_TOKEN_FILE"); ok {
		raw.daemonTokenFile = strings.TrimSpace(value)
	}
	if value, ok := lookup("XXX_CODE_REMOTE_URL"); ok {
		cfg.RemoteURL = strings.TrimSpace(value)
	}
	if value, ok := lookup("XXX_CODE_REMOTE_TOKEN"); ok {
		cfg.RemoteToken = strings.TrimSpace(value)
	}
	if value, ok := lookup("XXX_CODE_REMOTE_TOKEN_FILE"); ok {
		raw.remoteTokenFile = strings.TrimSpace(value)
	}
	if value, ok := lookup("XXX_CODE_REMOTE_SESSION"); ok {
		cfg.RemoteSession = strings.TrimSpace(value)
	}
	if value, ok := lookup("XXX_CODE_MCP_CONFIG"); ok {
		raw.mcpConfigFile = strings.TrimSpace(value)
	}
	if value, ok := lookup("XXX_CODE_PLUGIN_DIR"); ok {
		raw.pluginDir = strings.TrimSpace(value)
	}
	if value, ok := lookup("XXX_CODE_SESSION_FILE"); ok {
		raw.sessionFile = strings.TrimSpace(value)
	}
	if value, ok := lookup("XXX_CODE_DAEMON_DIR"); ok {
		raw.daemonDir = strings.TrimSpace(value)
	}
	if value, ok := lookup("XXX_CODE_DAEMON_AUDIT_FILE"); ok {
		raw.daemonAuditFile = strings.TrimSpace(value)
	}
	if value, ok := lookup("XXX_CODE_DAEMON_ALLOW_MODES"); ok {
		raw.daemonAllowModes = parseCSV(value)
	}
	if value, ok := lookup("XXX_CODE_DAEMON_DENY_MODES"); ok {
		raw.daemonDenyModes = parseCSV(value)
	}
	if value, ok := lookup("XXX_CODE_DAEMON_ALLOW_SESSION_PREFIX"); ok {
		raw.daemonAllowSessionPrefixes = parseCSV(value)
	}
	if value, ok := lookup("XXX_CODE_DAEMON_DENY_SESSION_PREFIX"); ok {
		raw.daemonDenySessionPrefixes = parseCSV(value)
	}
	if value, ok := lookup("XXX_CODE_DAEMON_RATE_LIMIT_PER_MINUTE"); ok {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid XXX_CODE_DAEMON_RATE_LIMIT_PER_MINUTE value: %w", err)
		}
		cfg.DaemonRateLimitPerMinute = parsed
	}
	if value, ok := lookup("XXX_CODE_DAEMON_RATE_LIMIT_BURST"); ok {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid XXX_CODE_DAEMON_RATE_LIMIT_BURST value: %w", err)
		}
		cfg.DaemonRateLimitBurst = parsed
	}
	if value, ok := lookup("XXX_CODE_SYSTEM_PROMPT_FILE"); ok {
		raw.systemPromptFile = strings.TrimSpace(value)
	}
	if value, ok := lookup("XXX_CODE_HOOK_EVENT_FILE"); ok {
		raw.hookEventFile = strings.TrimSpace(value)
	}
	if value, ok := lookup("XXX_CODE_LOG_FILE"); ok {
		cfg.LogFile = strings.TrimSpace(value)
	}
	if value, ok := lookup("XXX_CODE_LOG_LEVEL"); ok {
		level, err := diag.ParseLevel(value)
		if err != nil {
			return err
		}
		cfg.LogLevel = level
	}
	if value, ok := lookup("XXX_CODE_DEBUG"); ok {
		enabled, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid XXX_CODE_DEBUG value: %w", err)
		}
		if enabled {
			cfg.LogLevel = diag.LevelDebug
		} else {
			cfg.LogLevel = diag.LevelInfo
		}
	}
	return nil
}

type providerEnvOptions struct {
	AllowAPIKey  bool
	AllowBaseURL bool
	AllowVersion bool
}

func applySelectedProviderEnvConfig(cfg *Config, lookup func(string) (string, bool), options providerEnvOptions) {
	setIfAllowed := func(target *string, key string, allow bool) {
		if !allow {
			return
		}
		if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
			*target = strings.TrimSpace(value)
		}
	}

	switch normalizeProviderName(cfg.Provider) {
	case "", "anthropic":
		setIfAllowed(&cfg.APIKey, "ANTHROPIC_API_KEY", options.AllowAPIKey)
		setIfAllowed(&cfg.BaseURL, "ANTHROPIC_BASE_URL", options.AllowBaseURL)
		setIfAllowed(&cfg.Version, "ANTHROPIC_VERSION", options.AllowVersion)
	case "openai":
		setIfAllowed(&cfg.APIKey, "OPENAI_API_KEY", options.AllowAPIKey)
		setIfAllowed(&cfg.BaseURL, "OPENAI_BASE_URL", options.AllowBaseURL)
	case "azure-openai":
		if options.AllowAPIKey {
			if value, ok := lookup("AZURE_OPENAI_API_KEY"); ok && strings.TrimSpace(value) != "" {
				cfg.APIKey = strings.TrimSpace(value)
			} else {
				setIfAllowed(&cfg.APIKey, "OPENAI_API_KEY", true)
			}
		}
		if options.AllowBaseURL {
			if value, ok := lookup("AZURE_OPENAI_BASE_URL"); ok && strings.TrimSpace(value) != "" {
				cfg.BaseURL = strings.TrimSpace(value)
			} else {
				setIfAllowed(&cfg.BaseURL, "OPENAI_BASE_URL", true)
			}
		}
	case "gemini":
		setIfAllowed(&cfg.APIKey, "GEMINI_API_KEY", options.AllowAPIKey)
		setIfAllowed(&cfg.BaseURL, "GEMINI_BASE_URL", options.AllowBaseURL)
	case "minimax":
		setIfAllowed(&cfg.APIKey, "MINIMAX_API_KEY", options.AllowAPIKey)
		setIfAllowed(&cfg.BaseURL, "MINIMAX_BASE_URL", options.AllowBaseURL)
	case "glm":
		if options.AllowAPIKey {
			switch {
			case hasNonEmptyEnv(lookup, "GLM_API_KEY"):
				setIfAllowed(&cfg.APIKey, "GLM_API_KEY", true)
			case hasNonEmptyEnv(lookup, "ZHIPUAI_API_KEY"):
				setIfAllowed(&cfg.APIKey, "ZHIPUAI_API_KEY", true)
			case hasNonEmptyEnv(lookup, "BIGMODEL_API_KEY"):
				setIfAllowed(&cfg.APIKey, "BIGMODEL_API_KEY", true)
			default:
				setIfAllowed(&cfg.APIKey, "ZAI_API_KEY", true)
			}
		}
		setIfAllowed(&cfg.BaseURL, "GLM_BASE_URL", options.AllowBaseURL)
	}
}

func validateProviderConfig(cfg Config) error {
	if !(cfg.Daemon || strings.TrimSpace(cfg.RemoteURL) == "") {
		return nil
	}

	provider := normalizeProviderName(cfg.Provider)
	switch provider {
	case "", "anthropic":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return fmt.Errorf("ANTHROPIC_API_KEY or XXX_CODE_API_KEY is required for provider anthropic")
		}
		return nil
	case "openai":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return fmt.Errorf("OPENAI_API_KEY or XXX_CODE_API_KEY is required for provider openai/gpt")
		}
		return nil
	case "azure-openai":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return fmt.Errorf("AZURE_OPENAI_API_KEY, OPENAI_API_KEY, or XXX_CODE_API_KEY is required for provider azure-openai")
		}
		if strings.TrimSpace(cfg.BaseURL) == "" {
			return fmt.Errorf("AZURE_OPENAI_BASE_URL, OPENAI_BASE_URL, or XXX_CODE_BASE_URL is required for provider azure-openai")
		}
		return nil
	case "gemini":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return fmt.Errorf("GEMINI_API_KEY or XXX_CODE_API_KEY is required for provider gemini")
		}
		return nil
	case "minimax":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return fmt.Errorf("MINIMAX_API_KEY or XXX_CODE_API_KEY is required for provider minimax")
		}
		return nil
	case "glm":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return fmt.Errorf("GLM_API_KEY, ZHIPUAI_API_KEY, BIGMODEL_API_KEY, ZAI_API_KEY, or XXX_CODE_API_KEY is required for provider glm")
		}
		return nil
	default:
		return fmt.Errorf("unsupported provider %q; expected anthropic, openai, gpt, azure-openai, gemini, minimax, or glm", cfg.Provider)
	}
}

func normalizeProviderName(raw string) string {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", "anthropic":
		return "anthropic"
	case "openai", "gpt", "chatgpt":
		return "openai"
	case "azure", "azure_openai", "azure-openai":
		return "azure-openai"
	case "gemini", "google":
		return "gemini"
	case "minimax", "mini-max", "mini_max":
		return "minimax"
	case "glm", "zhipu", "z-ai", "zai":
		return "glm"
	default:
		return strings.TrimSpace(strings.ToLower(raw))
	}
}

func resolveConfigPath(args []string, lookup func(string) (string, bool), workingDir string) (string, error) {
	if explicit, ok := findStringFlag(args, "config"); ok {
		explicit = strings.TrimSpace(explicit)
		if explicit == "" {
			return "", nil
		}
		path := resolvePath(workingDir, explicit)
		if _, err := os.Stat(path); err != nil {
			return "", err
		}
		return path, nil
	}
	if value, ok := lookup("XXX_CODE_CONFIG"); ok && strings.TrimSpace(value) != "" {
		path := resolvePath(workingDir, value)
		if _, err := os.Stat(path); err != nil {
			return "", err
		}
		return path, nil
	}
	for _, name := range []string{"config.yaml", "config.yml", "config.json"} {
		path := filepath.Join(workingDir, ".xxx-code", name)
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", err
		}
		return path, nil
	}
	return "", nil
}

func loadFileConfig(path string) (fileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return fileConfig{}, err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return decodeYAMLFileConfig(path, data)
	case ".json":
		return decodeJSONFileConfig(path, data)
	default:
		if cfg, err := decodeYAMLFileConfig(path, data); err == nil {
			return cfg, nil
		}
		return decodeJSONFileConfig(path, data)
	}
}

func decodeJSONFileConfig(path string, data []byte) (fileConfig, error) {
	var cfg fileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fileConfig{}, fmt.Errorf("parse config file %s: %w", path, err)
	}
	return cfg, nil
}

func decodeYAMLFileConfig(path string, data []byte) (fileConfig, error) {
	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fileConfig{}, fmt.Errorf("parse config file %s: %w", path, err)
	}
	normalized := normalizeYAMLValue(raw)
	payload, err := json.Marshal(normalized)
	if err != nil {
		return fileConfig{}, fmt.Errorf("normalize config file %s: %w", path, err)
	}
	var cfg fileConfig
	if err := json.Unmarshal(payload, &cfg); err != nil {
		return fileConfig{}, fmt.Errorf("decode config file %s: %w", path, err)
	}
	return cfg, nil
}

func normalizeYAMLValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized[key] = normalizeYAMLValue(item)
		}
		return normalized
	case map[any]any:
		normalized := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized[fmt.Sprint(key)] = normalizeYAMLValue(item)
		}
		return normalized
	case []any:
		normalized := make([]any, 0, len(typed))
		for _, item := range typed {
			normalized = append(normalized, normalizeYAMLValue(item))
		}
		return normalized
	default:
		return value
	}
}

func visitedFlags(fs *flag.FlagSet) map[string]bool {
	visited := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		visited[f.Name] = true
	})
	return visited
}

func versionMode(args []string) bool {
	for i, arg := range args {
		if arg == "--version" || arg == "-version" {
			return true
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return i == 0 && strings.TrimSpace(arg) == "version"
	}
	return false
}

func findStringFlag(args []string, name string) (string, bool) {
	flagName := "--" + strings.TrimSpace(name)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == flagName && i+1 < len(args) {
			return args[i+1], true
		}
		if strings.HasPrefix(arg, flagName+"=") {
			return strings.TrimPrefix(arg, flagName+"="), true
		}
	}
	return "", false
}

func applyString(target *string, value *string) {
	if value != nil {
		*target = strings.TrimSpace(*value)
	}
}

func applyInt(target *int, value *int) {
	if value != nil {
		*target = *value
	}
}

func applyBool(target *bool, value *bool) {
	if value != nil {
		*target = *value
	}
}

func defaultSessionFile(workingDir, raw string) string {
	if strings.TrimSpace(raw) == "" {
		return filepath.Join(workingDir, ".xxx-code", "session.json")
	}
	return resolvePath(workingDir, raw)
}

func defaultDaemonDir(workingDir, raw string) string {
	if strings.TrimSpace(raw) == "" {
		return filepath.Join(workingDir, ".xxx-code", "daemon")
	}
	return resolvePath(workingDir, raw)
}

func defaultDaemonAuditFile(workingDir, daemonDir, raw string) string {
	if strings.TrimSpace(raw) == "" {
		if strings.TrimSpace(daemonDir) == "" {
			return ""
		}
		return filepath.Join(daemonDir, "audit.jsonl")
	}
	return resolvePath(workingDir, raw)
}

func resolveOptionalPath(base, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return resolvePath(base, raw)
}

func resolvePath(base, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw)
	}
	return filepath.Clean(filepath.Join(base, raw))
}

func parseRoots(base, raw string) []string {
	parts := strings.Split(raw, ",")
	roots := make([]string, 0, len(parts))
	for _, part := range parts {
		path := resolvePath(base, part)
		if path == "" {
			continue
		}
		roots = append(roots, path)
	}
	return roots
}

func parseCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		values = append(values, part)
	}
	return values
}

func joinCSV(values []string) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		parts = append(parts, value)
	}
	return strings.Join(parts, ",")
}

func hasNonEmptyEnv(lookup func(string) (string, bool), key string) bool {
	value, ok := lookup(key)
	return ok && strings.TrimSpace(value) != ""
}

func buildFlagUsage(fs *flag.FlagSet) string {
	var builder strings.Builder
	builder.WriteString("Usage: xxx-code [flags] [prompt]\n\n")
	builder.WriteString("Flags:\n")
	fs.SetOutput(&builder)
	fs.PrintDefaults()
	fs.SetOutput(io.Discard)
	return builder.String()
}

func appendUniquePaths(base []string, extra ...string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra))
	result := make([]string, 0, len(base)+len(extra))
	for _, value := range append(base, extra...) {
		value = filepath.Clean(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
