package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
)

type Config struct {
	BeforeTool string
	AfterTool  string
	AfterTurn  string
	AgentEvent string
	EventFile  string
}

type ScriptManager struct {
	cfg Config
}

func NewScriptManager(cfg Config) *ScriptManager {
	return &ScriptManager{cfg: cfg}
}

func (m *ScriptManager) HandleHook(ctx context.Context, event engine.HookEvent) error {
	if m == nil {
		return nil
	}

	command := m.commandFor(event.Kind)
	if command == "" {
		return nil
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	cmd := exec.CommandContext(ctx, shell, "-lc", command)
	if event.WorkingDir != "" {
		cmd.Dir = event.WorkingDir
	}
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Env = append(os.Environ(),
		"XXX_CODE_HOOK_KIND="+string(event.Kind),
		"XXX_CODE_AGENT_ID="+event.AgentID,
		"XXX_CODE_AGENT_NAME="+event.AgentName,
		"XXX_CODE_TOOL_NAME="+event.ToolName,
		"XXX_CODE_STATUS="+event.Status,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(output))
		if text == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, text)
	}
	return nil
}

func (m *ScriptManager) commandFor(kind engine.HookKind) string {
	switch kind {
	case engine.HookBeforeTool:
		return strings.TrimSpace(m.cfg.BeforeTool)
	case engine.HookAfterTool:
		return strings.TrimSpace(m.cfg.AfterTool)
	case engine.HookAfterTurn:
		return strings.TrimSpace(m.cfg.AfterTurn)
	case engine.HookAgentEvent:
		return strings.TrimSpace(m.cfg.AgentEvent)
	default:
		return ""
	}
}
