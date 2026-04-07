package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	ServerStatusConnected = "connected"
	ServerStatusFailed    = "failed"
)

type ServerStatus struct {
	Name      string   `json:"name"`
	Transport string   `json:"transport"`
	Command   string   `json:"command,omitempty"`
	Status    string   `json:"status"`
	ToolNames []string `json:"tool_names,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type Manager struct {
	configPath string
	servers    []*connectedServer
	statuses   []ServerStatus
}

type connectedServer struct {
	name    string
	session *sdkmcp.ClientSession
}

type toolBridge struct {
	fullName    string
	serverName  string
	remoteName  string
	description string
	inputSchema map[string]any
	session     *sdkmcp.ClientSession
}

func Start(ctx context.Context, registry *engine.Registry, options Options) (*Manager, error) {
	if registry == nil {
		return nil, errors.New("mcp manager requires a tool registry")
	}

	configPath, ok, err := ResolveConfigPath(options.WorkingDir, options.ConfigFile)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		return nil, err
	}

	manager := &Manager{configPath: configPath}
	names := make([]string, 0, len(cfg.Servers))
	for name := range cfg.Servers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		serverCfg := cfg.Servers[name]
		status := ServerStatus{
			Name:      name,
			Transport: serverCfg.Transport(),
			Command:   serverCfg.Command,
		}

		if strings.TrimSpace(name) == "" {
			status.Status = ServerStatusFailed
			status.Error = "server name cannot be empty"
			manager.statuses = append(manager.statuses, status)
			continue
		}
		if status.Transport != "stdio" {
			status.Status = ServerStatusFailed
			status.Error = "unsupported MCP transport: " + status.Transport
			manager.statuses = append(manager.statuses, status)
			continue
		}
		if strings.TrimSpace(serverCfg.Command) == "" {
			status.Status = ServerStatusFailed
			status.Error = "stdio MCP server command cannot be empty"
			manager.statuses = append(manager.statuses, status)
			continue
		}

		server, err := connectServer(ctx, name, serverCfg, options.WorkingDir)
		if err != nil {
			status.Status = ServerStatusFailed
			status.Error = err.Error()
			manager.statuses = append(manager.statuses, status)
			continue
		}

		remoteTools, err := listAllTools(ctx, server.session)
		if err != nil {
			_ = server.session.Close()
			status.Status = ServerStatusFailed
			status.Error = "list tools: " + err.Error()
			manager.statuses = append(manager.statuses, status)
			continue
		}

		for _, remoteTool := range remoteTools {
			bridge, err := newToolBridge(name, remoteTool, server.session)
			if err != nil {
				status.Warnings = append(status.Warnings, err.Error())
				continue
			}
			if err := registry.AddTool(bridge); err != nil {
				status.Warnings = append(status.Warnings, err.Error())
				continue
			}
			status.ToolNames = append(status.ToolNames, bridge.Definition().Name)
		}

		status.Status = ServerStatusConnected
		manager.servers = append(manager.servers, server)
		manager.statuses = append(manager.statuses, status)
	}

	return manager, nil
}

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	var errs []error
	for _, server := range m.servers {
		if err := server.session.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close MCP server %s: %w", server.name, err))
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) ConfigPath() string {
	if m == nil {
		return ""
	}
	return m.configPath
}

func (m *Manager) Statuses() []ServerStatus {
	if m == nil {
		return nil
	}
	statuses := make([]ServerStatus, 0, len(m.statuses))
	for _, status := range m.statuses {
		copyStatus := status
		copyStatus.ToolNames = append([]string(nil), status.ToolNames...)
		copyStatus.Warnings = append([]string(nil), status.Warnings...)
		statuses = append(statuses, copyStatus)
	}
	return statuses
}

func (m *Manager) ToolCount() int {
	if m == nil {
		return 0
	}
	count := 0
	for _, status := range m.statuses {
		count += len(status.ToolNames)
	}
	return count
}

func (m *Manager) ServerCount() int {
	if m == nil {
		return 0
	}
	return len(m.statuses)
}

func connectServer(ctx context.Context, name string, cfg ServerConfig, workingDir string) (*connectedServer, error) {
	commandDir, err := cfg.CommandDir(workingDir)
	if err != nil {
		return nil, fmt.Errorf("resolve cwd for %s: %w", name, err)
	}

	command := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	command.Dir = commandDir
	command.Env = mergeEnv(os.Environ(), cfg.Env)
	command.Stderr = os.Stderr

	client := sdkmcp.NewClient(&sdkmcp.Implementation{
		Name:    "xxx-code",
		Version: "dev",
	}, nil)
	session, err := client.Connect(ctx, &sdkmcp.CommandTransport{Command: command}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to MCP server %s: %w", name, err)
	}

	return &connectedServer{
		name:    name,
		session: session,
	}, nil
}

func listAllTools(ctx context.Context, session *sdkmcp.ClientSession) ([]*sdkmcp.Tool, error) {
	var (
		cursor string
		tools  []*sdkmcp.Tool
	)
	for {
		result, err := session.ListTools(ctx, &sdkmcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		tools = append(tools, result.Tools...)
		if result.NextCursor == "" {
			return tools, nil
		}
		cursor = result.NextCursor
	}
}

func newToolBridge(serverName string, tool *sdkmcp.Tool, session *sdkmcp.ClientSession) (*toolBridge, error) {
	if tool == nil {
		return nil, errors.New("nil MCP tool definition")
	}
	if strings.TrimSpace(tool.Name) == "" {
		return nil, fmt.Errorf("server %s exposed a tool with an empty name", serverName)
	}

	return &toolBridge{
		fullName:    buildToolName(serverName, tool.Name),
		serverName:  serverName,
		remoteName:  tool.Name,
		description: toolDescription(serverName, tool),
		inputSchema: normalizeSchema(tool.InputSchema),
		session:     session,
	}, nil
}

func (t *toolBridge) Definition() engine.ToolDefinition {
	return engine.ToolDefinition{
		Name:        t.fullName,
		Description: t.description,
		InputSchema: t.inputSchema,
	}
}

func (t *toolBridge) Call(ctx context.Context, exec *engine.ExecutionContext, input json.RawMessage) (engine.ToolResult, error) {
	_ = exec

	var args any = map[string]any{}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return engine.ToolResult{}, fmt.Errorf("decode MCP tool input for %s: %w", t.fullName, err)
		}
		if args == nil {
			args = map[string]any{}
		}
	}

	result, err := t.session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      t.remoteName,
		Arguments: args,
	})
	if err != nil {
		return engine.ToolResult{}, fmt.Errorf("call MCP tool %s: %w", t.fullName, err)
	}

	return engine.ToolResult{
		Content: renderCallToolResult(result),
		IsError: result.IsError,
	}, nil
}

func toolDescription(serverName string, tool *sdkmcp.Tool) string {
	description := strings.TrimSpace(tool.Description)
	if description == "" {
		return fmt.Sprintf("MCP tool %s from server %s", tool.Name, serverName)
	}
	return fmt.Sprintf("[MCP:%s] %s", serverName, description)
}

func normalizeSchema(schema any) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object"}
	}
	if ready, ok := schema.(map[string]any); ok && ready != nil {
		return ready
	}

	data, err := json.Marshal(schema)
	if err != nil {
		return map[string]any{"type": "object"}
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil || decoded == nil {
		return map[string]any{"type": "object"}
	}
	return decoded
}

func renderCallToolResult(result *sdkmcp.CallToolResult) string {
	if result == nil {
		return "MCP tool completed with no result"
	}

	parts := make([]string, 0, len(result.Content)+1)
	sawText := false
	for _, content := range result.Content {
		switch value := content.(type) {
		case *sdkmcp.TextContent:
			if strings.TrimSpace(value.Text) == "" {
				continue
			}
			sawText = true
			parts = append(parts, value.Text)
		case *sdkmcp.ImageContent:
			parts = append(parts, fmt.Sprintf("[image %s, %d bytes]", firstNonEmpty(value.MIMEType, "application/octet-stream"), len(value.Data)))
		case *sdkmcp.AudioContent:
			parts = append(parts, fmt.Sprintf("[audio %s, %d bytes]", firstNonEmpty(value.MIMEType, "application/octet-stream"), len(value.Data)))
		case *sdkmcp.ResourceLink:
			label := firstNonEmpty(value.Title, value.Name, value.URI)
			line := "[resource_link] " + label
			if value.URI != "" && value.URI != label {
				line += " <" + value.URI + ">"
			}
			if value.MIMEType != "" {
				line += " (" + value.MIMEType + ")"
			}
			parts = append(parts, line)
		case *sdkmcp.EmbeddedResource:
			parts = append(parts, formatEmbeddedResource(value.Resource))
		default:
			raw, err := json.Marshal(value)
			if err != nil {
				parts = append(parts, fmt.Sprintf("[unsupported MCP content %T]", value))
				continue
			}
			parts = append(parts, string(raw))
		}
	}

	if result.StructuredContent != nil && !sawText {
		if rendered, ok := renderJSON(result.StructuredContent); ok {
			parts = append(parts, rendered)
		}
	}

	if len(parts) == 0 {
		if result.IsError {
			return "MCP tool returned an error with no content"
		}
		return "MCP tool completed with no content"
	}
	return strings.Join(parts, "\n\n")
}

func formatEmbeddedResource(resource *sdkmcp.ResourceContents) string {
	if resource == nil {
		return "[resource]"
	}
	header := "[resource]"
	if resource.URI != "" {
		header += " " + resource.URI
	}
	if resource.MIMEType != "" {
		header += " (" + resource.MIMEType + ")"
	}
	if strings.TrimSpace(resource.Text) != "" {
		return header + "\n" + resource.Text
	}
	if len(resource.Blob) > 0 {
		return fmt.Sprintf("%s [%d bytes]", header, len(resource.Blob))
	}
	return header
}

func renderJSON(value any) (string, bool) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil || len(data) == 0 {
		return "", false
	}
	return string(data), true
}

func mergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	values := make(map[string]string, len(base)+len(overrides))
	for _, item := range base {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		values[key] = value
	}
	for key, value := range overrides {
		values[key] = value
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
