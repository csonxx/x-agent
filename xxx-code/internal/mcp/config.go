package mcp

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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
	raw := strings.TrimSpace(c.URL)
	if raw == "" {
		return "", fmt.Errorf("%s MCP server url cannot be empty", c.Transport())
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse MCP server url %q: %w", raw, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("MCP server url must be an absolute http or https URL: %s", raw)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.String(), nil
	default:
		return "", fmt.Errorf("MCP server url must use http or https: %s", raw)
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
