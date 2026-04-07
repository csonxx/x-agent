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
- Use agent_spawn only when a sub-task is clearly separable and benefits from parallel or isolated execution.
- If you spawn a background agent, use agent_wait or agent_list to integrate its result before you finish.
- Be explicit about verification. If you did not run a check, say so.
- Keep final user-facing answers concise and practical.`

type Config struct {
	APIKey       string
	BaseURL      string
	Version      string
	Model        string
	MaxTurns     int
	MaxTokens    int
	WorkingDir   string
	SessionFile  string
	Resume       bool
	Print        bool
	Verbose      bool
	SystemPrompt string
	ToolTimeout  time.Duration
	Prompt       string
}

func Load() (Config, error) {
	cfg := Config{}

	flag.StringVar(&cfg.Model, "model", firstNonEmpty(os.Getenv("XXX_CODE_MODEL"), "claude-sonnet-4-5"), "Anthropic model to use")
	flag.StringVar(&cfg.BaseURL, "base-url", firstNonEmpty(os.Getenv("ANTHROPIC_BASE_URL"), "https://api.anthropic.com"), "Anthropic API base URL")
	flag.StringVar(&cfg.Version, "anthropic-version", firstNonEmpty(os.Getenv("ANTHROPIC_VERSION"), "2023-06-01"), "Anthropic API version header")
	flag.IntVar(&cfg.MaxTurns, "max-turns", 12, "Maximum agentic turns per user prompt")
	flag.IntVar(&cfg.MaxTokens, "max-tokens", 16384, "Max output tokens per model request")
	flag.BoolVar(&cfg.Print, "print", false, "Run once and exit")
	flag.BoolVar(&cfg.Verbose, "verbose", false, "Print tool and agent lifecycle events")
	flag.BoolVar(&cfg.Resume, "resume", false, "Resume the main session and known agents from the session file")
	flag.DurationVar(&cfg.ToolTimeout, "tool-timeout", 2*time.Minute, "Per-tool execution timeout")

	systemPromptFile := flag.String("system-prompt-file", "", "Read the system prompt from a file")
	cwdFlag := flag.String("cwd", "", "Working directory")
	sessionFileFlag := flag.String("session-file", "", "Path to the persisted session file")

	flag.Parse()

	cfg.APIKey = strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	if cfg.APIKey == "" {
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
