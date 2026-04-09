package mcp

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Config struct {
	Servers map[string]ServerConfig `json:"mcpServers"`
}

type ServerConfig struct {
	Type          string            `json:"type,omitempty"`
	TransportType string            `json:"transport,omitempty"`
	URL           string            `json:"url,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	Command       string            `json:"command,omitempty"`
	Args          []string          `json:"args,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Cwd           string            `json:"cwd,omitempty"`
}

type Options struct {
	WorkingDir string
	ConfigFile string
}

type ValidationReport struct {
	ConfigPath  string            `json:"config_path,omitempty"`
	Present     bool              `json:"present"`
	Valid       bool              `json:"valid"`
	ServerCount int               `json:"server_count"`
	IssueCount  int               `json:"issue_count"`
	Issues      []ValidationIssue `json:"issues,omitempty"`
}

type ValidationIssue struct {
	Server  string `json:"server,omitempty"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

func ResolveConfigPath(workingDir, explicit string) (string, bool, error) {
	if strings.TrimSpace(explicit) != "" {
		path, err := expandPath(workingDir, explicit)
		if err != nil {
			return "", false, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", false, fmt.Errorf("stat mcp config %q: %w", path, err)
		}
		if info.IsDir() {
			return "", false, fmt.Errorf("mcp config path is a directory: %s", path)
		}
		return path, true, nil
	}

	path := filepath.Join(workingDir, ".mcp.json")
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("stat mcp config %q: %w", path, err)
	}
	if info.IsDir() {
		return "", false, fmt.Errorf("mcp config path is a directory: %s", path)
	}
	return path, true, nil
}

func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read mcp config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse mcp config: %w", err)
	}
	if cfg.Servers == nil {
		cfg.Servers = map[string]ServerConfig{}
	}
	return cfg, nil
}

func ValidateOptions(options Options) ValidationReport {
	report := ValidationReport{}
	configPath, ok, err := ResolveConfigPath(options.WorkingDir, options.ConfigFile)
	if err != nil {
		report.ConfigPath = strings.TrimSpace(options.ConfigFile)
		report.Issues = append(report.Issues, ValidationIssue{
			Level:   "error",
			Message: err.Error(),
		})
		report.IssueCount = len(report.Issues)
		return report
	}
	report.ConfigPath = configPath
	report.Present = ok
	if !ok {
		report.Issues = append(report.Issues, ValidationIssue{
			Level:   "error",
			Message: "mcp config file not found",
		})
		report.IssueCount = len(report.Issues)
		return report
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		report.Issues = append(report.Issues, ValidationIssue{
			Level:   "error",
			Message: err.Error(),
		})
		report.IssueCount = len(report.Issues)
		return report
	}

	report.ServerCount = len(cfg.Servers)
	if len(cfg.Servers) == 0 {
		report.Issues = append(report.Issues, ValidationIssue{
			Level:   "warning",
			Message: "mcp config contains no servers",
		})
	}

	names := make([]string, 0, len(cfg.Servers))
	for name := range cfg.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		serverCfg := cfg.Servers[name]
		validateServerConfig(&report, options.WorkingDir, name, serverCfg)
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

func (c ServerConfig) Transport() string {
	value := strings.TrimSpace(c.TransportType)
	if value == "" {
		value = strings.TrimSpace(c.Type)
	}
	if value == "" {
		return "stdio"
	}
	value = strings.ToLower(value)
	switch value {
	case "streamable-http", "streamable_http", "streamablehttp":
		return "http"
	case "websocket":
		return "ws"
	default:
		return value
	}
}

func (c ServerConfig) CommandDir(workingDir string) (string, error) {
	if strings.TrimSpace(c.Cwd) == "" {
		return workingDir, nil
	}
	return expandPath(workingDir, c.Cwd)
}

func (c ServerConfig) Endpoint() (string, error) {
	return c.EndpointForTransport(c.Transport())
}

func (c ServerConfig) EndpointForTransport(transport string) (string, error) {
	raw := strings.TrimSpace(c.URL)
	if raw == "" {
		return "", fmt.Errorf("%s MCP server url cannot be empty", transport)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse MCP server url %q: %w", raw, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("MCP server url must be absolute: %s", raw)
	}

	switch transport {
	case "http", "sse":
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https":
			return parsed.String(), nil
		default:
			return "", fmt.Errorf("MCP server url must use http or https: %s", raw)
		}
	case "ws":
		switch strings.ToLower(parsed.Scheme) {
		case "ws", "wss":
			return parsed.String(), nil
		default:
			return "", fmt.Errorf("MCP websocket url must use ws or wss: %s", raw)
		}
	default:
		return parsed.String(), nil
	}
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

func validateServerConfig(report *ValidationReport, workingDir, name string, cfg ServerConfig) {
	server := strings.TrimSpace(name)
	if server == "" {
		report.Issues = append(report.Issues, ValidationIssue{
			Level:   "error",
			Message: "server name cannot be empty",
		})
		return
	}

	transport := cfg.Transport()
	switch transport {
	case "stdio":
		if strings.TrimSpace(cfg.Command) == "" {
			report.Issues = append(report.Issues, ValidationIssue{
				Server:  server,
				Level:   "error",
				Message: "stdio MCP server command cannot be empty",
			})
		}
		if _, err := cfg.CommandDir(workingDir); err != nil {
			report.Issues = append(report.Issues, ValidationIssue{
				Server:  server,
				Level:   "error",
				Message: err.Error(),
			})
		}
	case "http", "sse", "ws":
		if _, err := cfg.EndpointForTransport(transport); err != nil {
			report.Issues = append(report.Issues, ValidationIssue{
				Server:  server,
				Level:   "error",
				Message: err.Error(),
			})
		}
	default:
		report.Issues = append(report.Issues, ValidationIssue{
			Server:  server,
			Level:   "error",
			Message: "unsupported MCP transport: " + transport,
		})
	}

	for headerName := range cfg.Headers {
		if strings.TrimSpace(headerName) == "" {
			report.Issues = append(report.Issues, ValidationIssue{
				Server:  server,
				Level:   "error",
				Message: "header names cannot be empty",
			})
		}
	}
	for envName := range cfg.Env {
		if strings.TrimSpace(envName) == "" {
			report.Issues = append(report.Issues, ValidationIssue{
				Server:  server,
				Level:   "error",
				Message: "environment variable names cannot be empty",
			})
		}
	}
}
