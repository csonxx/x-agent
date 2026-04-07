package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Servers map[string]ServerConfig `json:"mcpServers"`
}

type ServerConfig struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
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
	if strings.TrimSpace(c.Type) == "" {
		return "stdio"
	}
	return strings.TrimSpace(c.Type)
}

func (c ServerConfig) CommandDir(workingDir string) (string, error) {
	if strings.TrimSpace(c.Cwd) == "" {
		return workingDir, nil
	}
	return expandPath(workingDir, c.Cwd)
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
