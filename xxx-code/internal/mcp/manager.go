package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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
	URL       string   `json:"url,omitempty"`
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

type Resource struct {
	Server      string `json:"server"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	URI         string `json:"uri"`
	MIMEType    string `json:"mime_type,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

type ResourceTemplate struct {
	Server      string `json:"server"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	URITemplate string `json:"uri_template"`
	MIMEType    string `json:"mime_type,omitempty"`
}

type Prompt struct {
	Server      string           `json:"server"`
	Name        string           `json:"name"`
	Title       string           `json:"title,omitempty"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}

type PromptArgument struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type PromptMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type PromptDetails struct {
	Server      string          `json:"server"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

type ResourceContent struct {
	URI       string `json:"uri"`
	MIMEType  string `json:"mime_type,omitempty"`
	Text      string `json:"text,omitempty"`
	BlobBytes int    `json:"blob_bytes,omitempty"`
	Preview   string `json:"preview"`
}

type ResourceDetails struct {
	Server   string            `json:"server"`
	URI      string            `json:"uri"`
	Contents []ResourceContent `json:"contents"`
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
	manager.registerSupportTools(registry)
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
			URL:       serverCfg.URL,
		}

		if strings.TrimSpace(name) == "" {
			status.Status = ServerStatusFailed
			status.Error = "server name cannot be empty"
			manager.statuses = append(manager.statuses, status)
			continue
		}
		switch status.Transport {
		case "stdio":
			if strings.TrimSpace(serverCfg.Command) == "" {
				status.Status = ServerStatusFailed
				status.Error = "stdio MCP server command cannot be empty"
				manager.statuses = append(manager.statuses, status)
				continue
			}
		case "http", "sse", "ws":
			endpoint, err := serverCfg.EndpointForTransport(status.Transport)
			if err != nil {
				status.Status = ServerStatusFailed
				status.Error = err.Error()
				manager.statuses = append(manager.statuses, status)
				continue
			}
			status.URL = endpoint
		default:
			status.Status = ServerStatusFailed
			status.Error = "unsupported MCP transport: " + status.Transport
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
	client := sdkmcp.NewClient(&sdkmcp.Implementation{
		Name:    "xxx-code",
		Version: "dev",
	}, nil)
	rootPath := strings.TrimSpace(workingDir)
	if fileURI, err := fileRootURI(rootPath); err == nil {
		client.AddRoots(&sdkmcp.Root{
			Name: "workspace",
			URI:  fileURI,
		})
	}

	transport, err := buildTransport(ctx, name, cfg, workingDir)
	if err != nil {
		return nil, err
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to MCP server %s: %w", name, err)
	}

	return &connectedServer{
		name:    name,
		session: session,
	}, nil
}

func buildTransport(ctx context.Context, name string, cfg ServerConfig, workingDir string) (sdkmcp.Transport, error) {
	switch cfg.Transport() {
	case "stdio":
		commandDir, err := cfg.CommandDir(workingDir)
		if err != nil {
			return nil, fmt.Errorf("resolve cwd for %s: %w", name, err)
		}

		command := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
		command.Dir = commandDir
		command.Env = mergeEnv(os.Environ(), cfg.Env)
		command.Stderr = os.Stderr
		return &sdkmcp.CommandTransport{Command: command}, nil
	case "http":
		endpoint, err := cfg.EndpointForTransport("http")
		if err != nil {
			return nil, err
		}
		return &sdkmcp.StreamableClientTransport{
			Endpoint:   endpoint,
			HTTPClient: newHTTPClient(cfg.Headers),
		}, nil
	case "sse":
		endpoint, err := cfg.EndpointForTransport("sse")
		if err != nil {
			return nil, err
		}
		return &sdkmcp.SSEClientTransport{
			Endpoint:   endpoint,
			HTTPClient: newHTTPClient(cfg.Headers),
		}, nil
	case "ws":
		endpoint, err := cfg.EndpointForTransport("ws")
		if err != nil {
			return nil, err
		}
		return &websocketClientTransport{
			Endpoint: endpoint,
			Header:   headerMapToHTTPHeader(cfg.Headers),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported MCP transport: %s", cfg.Transport())
	}
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

func (m *Manager) ListResources(ctx context.Context, serverName string) ([]Resource, error) {
	var resources []Resource
	servers, err := m.selectedServers(serverName)
	if err != nil {
		return nil, err
	}
	for _, server := range servers {
		list, err := listAllResources(ctx, server.session)
		if err != nil {
			return nil, fmt.Errorf("list resources for %s: %w", server.name, err)
		}
		for _, resource := range list {
			if resource == nil {
				continue
			}
			resources = append(resources, Resource{
				Server:      server.name,
				Name:        resource.Name,
				Title:       resource.Title,
				Description: resource.Description,
				URI:         resource.URI,
				MIMEType:    resource.MIMEType,
				Size:        resource.Size,
			})
		}
	}
	return resources, nil
}

func (m *Manager) ListResourceTemplates(ctx context.Context, serverName string) ([]ResourceTemplate, error) {
	var templates []ResourceTemplate
	servers, err := m.selectedServers(serverName)
	if err != nil {
		return nil, err
	}
	for _, server := range servers {
		list, err := listAllResourceTemplates(ctx, server.session)
		if err != nil {
			return nil, fmt.Errorf("list resource templates for %s: %w", server.name, err)
		}
		for _, template := range list {
			if template == nil {
				continue
			}
			templates = append(templates, ResourceTemplate{
				Server:      server.name,
				Name:        template.Name,
				Title:       template.Title,
				Description: template.Description,
				URITemplate: template.URITemplate,
				MIMEType:    template.MIMEType,
			})
		}
	}
	return templates, nil
}

func (m *Manager) ListPrompts(ctx context.Context, serverName string) ([]Prompt, error) {
	var prompts []Prompt
	servers, err := m.selectedServers(serverName)
	if err != nil {
		return nil, err
	}
	for _, server := range servers {
		list, err := listAllPrompts(ctx, server.session)
		if err != nil {
			return nil, fmt.Errorf("list prompts for %s: %w", server.name, err)
		}
		for _, prompt := range list {
			if prompt == nil {
				continue
			}
			prompts = append(prompts, Prompt{
				Server:      server.name,
				Name:        prompt.Name,
				Title:       prompt.Title,
				Description: prompt.Description,
				Arguments:   convertPromptArguments(prompt.Arguments),
			})
		}
	}
	return prompts, nil
}

func (m *Manager) ReadResource(ctx context.Context, serverName, uri string) (ResourceDetails, error) {
	server, err := m.serverByName(serverName)
	if err != nil {
		return ResourceDetails{}, err
	}
	result, err := server.session.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: uri})
	if err != nil {
		return ResourceDetails{}, fmt.Errorf("read resource from %s: %w", server.name, err)
	}

	details := ResourceDetails{
		Server:   server.name,
		URI:      uri,
		Contents: make([]ResourceContent, 0, len(result.Contents)),
	}
	for _, content := range result.Contents {
		if content == nil {
			continue
		}
		details.Contents = append(details.Contents, ResourceContent{
			URI:       content.URI,
			MIMEType:  content.MIMEType,
			Text:      content.Text,
			BlobBytes: len(content.Blob),
			Preview:   previewResourceContent(content),
		})
	}
	return details, nil
}

func (m *Manager) GetPrompt(ctx context.Context, serverName, promptName string, arguments map[string]string) (PromptDetails, error) {
	server, err := m.serverByName(serverName)
	if err != nil {
		return PromptDetails{}, err
	}
	result, err := server.session.GetPrompt(ctx, &sdkmcp.GetPromptParams{
		Name:      promptName,
		Arguments: arguments,
	})
	if err != nil {
		return PromptDetails{}, fmt.Errorf("get prompt from %s: %w", server.name, err)
	}

	details := PromptDetails{
		Server:      server.name,
		Name:        promptName,
		Description: result.Description,
		Messages:    make([]PromptMessage, 0, len(result.Messages)),
	}
	for _, message := range result.Messages {
		if message == nil {
			continue
		}
		details.Messages = append(details.Messages, PromptMessage{
			Role:    string(message.Role),
			Content: renderPromptContent(message.Content),
		})
	}
	return details, nil
}

func listAllResources(ctx context.Context, session *sdkmcp.ClientSession) ([]*sdkmcp.Resource, error) {
	var (
		cursor    string
		resources []*sdkmcp.Resource
	)
	for {
		result, err := session.ListResources(ctx, &sdkmcp.ListResourcesParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		resources = append(resources, result.Resources...)
		if result.NextCursor == "" {
			return resources, nil
		}
		cursor = result.NextCursor
	}
}

func listAllResourceTemplates(ctx context.Context, session *sdkmcp.ClientSession) ([]*sdkmcp.ResourceTemplate, error) {
	var (
		cursor    string
		templates []*sdkmcp.ResourceTemplate
	)
	for {
		result, err := session.ListResourceTemplates(ctx, &sdkmcp.ListResourceTemplatesParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		templates = append(templates, result.ResourceTemplates...)
		if result.NextCursor == "" {
			return templates, nil
		}
		cursor = result.NextCursor
	}
}

func listAllPrompts(ctx context.Context, session *sdkmcp.ClientSession) ([]*sdkmcp.Prompt, error) {
	var (
		cursor  string
		prompts []*sdkmcp.Prompt
	)
	for {
		result, err := session.ListPrompts(ctx, &sdkmcp.ListPromptsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		prompts = append(prompts, result.Prompts...)
		if result.NextCursor == "" {
			return prompts, nil
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
		rendered, isText := renderContent(content)
		if rendered == "" {
			continue
		}
		sawText = sawText || isText
		parts = append(parts, rendered)
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

func (m *Manager) selectedServers(name string) ([]*connectedServer, error) {
	if strings.TrimSpace(name) == "" {
		servers := make([]*connectedServer, 0, len(m.servers))
		servers = append(servers, m.servers...)
		return servers, nil
	}
	server, err := m.serverByName(name)
	if err != nil {
		return nil, err
	}
	return []*connectedServer{server}, nil
}

func (m *Manager) serverByName(name string) (*connectedServer, error) {
	if m == nil {
		return nil, errors.New("MCP is not configured")
	}
	for _, server := range m.servers {
		if server.name == name {
			return server, nil
		}
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("MCP server name is required")
	}
	return nil, fmt.Errorf("unknown MCP server: %s", name)
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

func previewResourceContent(resource *sdkmcp.ResourceContents) string {
	if resource == nil {
		return "[resource]"
	}
	if strings.TrimSpace(resource.Text) != "" {
		return resource.Text
	}
	if len(resource.Blob) > 0 {
		return fmt.Sprintf("[blob %d bytes]", len(resource.Blob))
	}
	return formatEmbeddedResource(resource)
}

func renderPromptContent(content sdkmcp.Content) string {
	rendered, _ := renderContent(content)
	return rendered
}

func renderContent(content sdkmcp.Content) (string, bool) {
	switch value := content.(type) {
	case *sdkmcp.TextContent:
		if strings.TrimSpace(value.Text) == "" {
			return "", true
		}
		return value.Text, true
	case *sdkmcp.ImageContent:
		return fmt.Sprintf("[image %s, %d bytes]", firstNonEmpty(value.MIMEType, "application/octet-stream"), len(value.Data)), false
	case *sdkmcp.AudioContent:
		return fmt.Sprintf("[audio %s, %d bytes]", firstNonEmpty(value.MIMEType, "application/octet-stream"), len(value.Data)), false
	case *sdkmcp.ResourceLink:
		label := firstNonEmpty(value.Title, value.Name, value.URI)
		line := "[resource_link] " + label
		if value.URI != "" && value.URI != label {
			line += " <" + value.URI + ">"
		}
		if value.MIMEType != "" {
			line += " (" + value.MIMEType + ")"
		}
		return line, false
	case *sdkmcp.EmbeddedResource:
		return formatEmbeddedResource(value.Resource), false
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprintf("[unsupported MCP content %T]", value), false
		}
		return string(raw), false
	}
}

func convertPromptArguments(arguments []*sdkmcp.PromptArgument) []PromptArgument {
	converted := make([]PromptArgument, 0, len(arguments))
	for _, argument := range arguments {
		if argument == nil {
			continue
		}
		converted = append(converted, PromptArgument{
			Name:        argument.Name,
			Title:       argument.Title,
			Description: argument.Description,
			Required:    argument.Required,
		})
	}
	return converted
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

func fileRootURI(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("path is empty")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String(), nil
}

func newHTTPClient(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return nil
	}
	return &http.Client{
		Transport: &headerRoundTripper{
			base:    http.DefaultTransport,
			headers: headers,
		},
	}
}

func headerMapToHTTPHeader(headers map[string]string) http.Header {
	if len(headers) == 0 {
		return nil
	}
	values := make(http.Header, len(headers))
	for key, value := range headers {
		values.Set(key, value)
	}
	return values
}

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (rt *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}

	cloned := req.Clone(req.Context())
	cloned.Header = req.Header.Clone()
	for key, value := range rt.headers {
		cloned.Header.Del(key)
		cloned.Header.Set(key, value)
	}
	return base.RoundTrip(cloned)
}
