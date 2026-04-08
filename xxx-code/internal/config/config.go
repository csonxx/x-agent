package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	APIKey            string
	BaseURL           string
	Version           string
	Model             string
	MaxTurns          int
	MaxTokens         int
	MaxParallelAgents int
	ContextBudget     int
	CompactKeep       int
	Daemon            bool
	DaemonListenAddr  string
	DaemonToken       string
	DaemonDir         string
	RemoteURL         string
	RemoteToken       string
	RemoteSession     string
	RemoteList        bool
	WorkingDir        string
	SessionFile       string
	MCPConfigFile     string
	ReadRoots         []string
	WriteRoots        []string
	AllowedTools      []string
	BlockedTools      []string
	BashAllowPrefixes []string
	BashDenyPrefixes  []string
	ReadOnly          bool
	BashEnabled       bool
	HookBeforeTool    string
	HookAfterTool     string
	HookAfterTurn     string
	HookAgentEvent    string
	HookTimeout       time.Duration
	Resume            bool
	Print             bool
	TUI               bool
	Stream            bool
	Verbose           bool
	SystemPrompt      string
	ToolTimeout       time.Duration
	Prompt            string
}

func Load() (Config, error) {
	cfg := Config{}

	flag.StringVar(&cfg.Model, "model", firstNonEmpty(os.Getenv("XXX_CODE_MODEL"), "claude-sonnet-4-5"), "Anthropic model to use")
	flag.StringVar(&cfg.BaseURL, "base-url", firstNonEmpty(os.Getenv("ANTHROPIC_BASE_URL"), "https://api.anthropic.com"), "Anthropic API base URL")
	flag.StringVar(&cfg.Version, "anthropic-version", firstNonEmpty(os.Getenv("ANTHROPIC_VERSION"), "2023-06-01"), "Anthropic API version header")
	flag.IntVar(&cfg.MaxTurns, "max-turns", 12, "Maximum agentic turns per user prompt")
	flag.IntVar(&cfg.MaxTokens, "max-tokens", 16384, "Max output tokens per model request")
	flag.IntVar(&cfg.MaxParallelAgents, "max-parallel-agents", 4, "Maximum number of sub-agents that can run concurrently")
	flag.IntVar(&cfg.ContextBudget, "context-budget", 120000, "Approximate context token budget before automatic compaction; set 0 to disable")
	flag.IntVar(&cfg.CompactKeep, "compact-keep", 12, "How many latest messages to keep verbatim during automatic compaction")
	flag.BoolVar(&cfg.Daemon, "daemon", false, "Run xxx-code as a persistent HTTP daemon")
	flag.StringVar(&cfg.DaemonListenAddr, "listen", firstNonEmpty(os.Getenv("XXX_CODE_LISTEN"), "127.0.0.1:7331"), "Listen address for daemon mode")
	flag.StringVar(&cfg.DaemonToken, "daemon-token", strings.TrimSpace(os.Getenv("XXX_CODE_DAEMON_TOKEN")), "Optional bearer token required by the daemon for /v1/* requests")
	flag.StringVar(&cfg.RemoteURL, "remote-url", strings.TrimSpace(os.Getenv("XXX_CODE_REMOTE_URL")), "Daemon base URL to use as a remote bridge")
	flag.StringVar(&cfg.RemoteToken, "remote-token", strings.TrimSpace(os.Getenv("XXX_CODE_REMOTE_TOKEN")), "Bearer token to send when connecting to a protected daemon")
	flag.StringVar(&cfg.RemoteSession, "remote-session", "", "Remote daemon session ID to open or create")
	flag.BoolVar(&cfg.RemoteList, "remote-list-sessions", false, "List daemon sessions instead of running a local session")
	flag.BoolVar(&cfg.ReadOnly, "read-only", false, "Disable write_file and edit_file tool writes")
	flag.BoolVar(&cfg.BashEnabled, "bash", true, "Enable or disable the bash tool")
	flag.BoolVar(&cfg.Print, "print", false, "Run once and exit")
	flag.BoolVar(&cfg.TUI, "tui", false, "Run an interactive terminal UI instead of the line-oriented REPL")
	flag.BoolVar(&cfg.Stream, "stream", true, "Stream assistant text as it is generated when the provider supports it")
	flag.BoolVar(&cfg.Verbose, "verbose", false, "Print tool and agent lifecycle events")
	flag.BoolVar(&cfg.Resume, "resume", false, "Resume the main session and known agents from the session file")
	flag.DurationVar(&cfg.ToolTimeout, "tool-timeout", 2*time.Minute, "Per-tool execution timeout")
	flag.DurationVar(&cfg.HookTimeout, "hook-timeout", 30*time.Second, "Timeout for each configured hook command")

	systemPromptFile := flag.String("system-prompt-file", "", "Read the system prompt from a file")
	cwdFlag := flag.String("cwd", "", "Working directory")
	sessionFileFlag := flag.String("session-file", "", "Path to the persisted session file")
	daemonDirFlag := flag.String("daemon-dir", "", "Directory for daemon-managed remote sessions")
	mcpConfigFlag := flag.String("mcp-config", strings.TrimSpace(os.Getenv("XXX_CODE_MCP_CONFIG")), "Path to an MCP config file; defaults to .mcp.json in the working directory when present")
	readRootsFlag := flag.String("allow-read", "", "Comma-separated read roots; the working directory is always included")
	writeRootsFlag := flag.String("allow-write", "", "Comma-separated write roots; the working directory is always included unless --read-only is set")
	allowToolsFlag := flag.String("allow-tools", "", "Comma-separated tool allowlist; when set, only these tools may run")
	denyToolsFlag := flag.String("deny-tools", "", "Comma-separated tool denylist")
	allowBashPrefixFlag := flag.String("allow-bash-prefix", "", "Comma-separated allowed bash command prefixes")
	denyBashPrefixFlag := flag.String("deny-bash-prefix", "", "Comma-separated blocked bash command prefixes")
	hookBeforeToolFlag := flag.String("hook-before-tool", "", "Shell command to run before each tool call; non-zero exit blocks the tool")
	hookAfterToolFlag := flag.String("hook-after-tool", "", "Shell command to run after each tool call")
	hookAfterTurnFlag := flag.String("hook-after-turn", "", "Shell command to run after each turn")
	hookAgentEventFlag := flag.String("hook-agent-event", "", "Shell command to run for agent lifecycle events")

	flag.Parse()

	cfg.APIKey = strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	if cfg.APIKey == "" && (cfg.Daemon || strings.TrimSpace(cfg.RemoteURL) == "") {
		return Config{}, fmt.Errorf("ANTHROPIC_API_KEY is required")
	}

	if *cwdFlag != "" {
		cfg.WorkingDir = *cwdFlag
	} else {
		wd, err := os.Getwd()
		if err != nil {
			return Config{}, err
		}
		cfg.WorkingDir = wd
	}
	cfg.WorkingDir = filepath.Clean(cfg.WorkingDir)

	if *sessionFileFlag != "" {
		if filepath.IsAbs(*sessionFileFlag) {
			cfg.SessionFile = filepath.Clean(*sessionFileFlag)
		} else {
			cfg.SessionFile = filepath.Join(cfg.WorkingDir, *sessionFileFlag)
		}
	} else {
		cfg.SessionFile = filepath.Join(cfg.WorkingDir, ".xxx-code", "session.json")
	}
	if strings.TrimSpace(*daemonDirFlag) != "" {
		cfg.DaemonDir = resolvePath(cfg.WorkingDir, *daemonDirFlag)
	} else {
		cfg.DaemonDir = filepath.Join(cfg.WorkingDir, ".xxx-code", "daemon")
	}

	if strings.TrimSpace(*mcpConfigFlag) != "" {
		cfg.MCPConfigFile = resolvePath(cfg.WorkingDir, *mcpConfigFlag)
	}

	cfg.ReadRoots = append([]string{cfg.WorkingDir}, parseRoots(cfg.WorkingDir, *readRootsFlag)...)
	cfg.WriteRoots = append([]string{cfg.WorkingDir}, parseRoots(cfg.WorkingDir, *writeRootsFlag)...)
	cfg.AllowedTools = parseCSV(*allowToolsFlag)
	cfg.BlockedTools = parseCSV(*denyToolsFlag)
	cfg.BashAllowPrefixes = parseCSV(*allowBashPrefixFlag)
	cfg.BashDenyPrefixes = parseCSV(*denyBashPrefixFlag)
	cfg.HookBeforeTool = strings.TrimSpace(*hookBeforeToolFlag)
	cfg.HookAfterTool = strings.TrimSpace(*hookAfterToolFlag)
	cfg.HookAfterTurn = strings.TrimSpace(*hookAfterTurnFlag)
	cfg.HookAgentEvent = strings.TrimSpace(*hookAgentEventFlag)

	cfg.SystemPrompt = defaultSystemPrompt
	if *systemPromptFile != "" {
		data, err := os.ReadFile(*systemPromptFile)
		if err != nil {
			return Config{}, err
		}
		cfg.SystemPrompt = string(data)
	}

	cfg.Prompt = strings.TrimSpace(strings.Join(flag.Args(), " "))
	if cfg.Prompt != "" {
		cfg.Print = true
	}

	return cfg, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func resolvePath(base, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw)
	}
	return filepath.Join(base, raw)
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
