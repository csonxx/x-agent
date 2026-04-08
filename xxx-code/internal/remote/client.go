package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/diag"
	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	mcpruntime "github.com/caowenhua/x-agent/xxx-code/internal/mcp"
	"github.com/caowenhua/x-agent/xxx-code/internal/tools"
)

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type Error struct {
	StatusCode int
	Code       string
	Message    string
	Retryable  bool
}

type SessionSummary struct {
	ID            string    `json:"id"`
	SessionFile   string    `json:"session_file"`
	WorkingDir    string    `json:"working_dir"`
	Loaded        bool      `json:"loaded"`
	LoadedAt      time.Time `json:"loaded_at,omitempty"`
	MessageCount  int       `json:"message_count"`
	ApproxTokens  int       `json:"approx_tokens"`
	AgentCount    int       `json:"agent_count"`
	WorkflowCount int       `json:"workflow_count"`
	SavedAt       time.Time `json:"saved_at,omitempty"`
}

type TurnUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type TurnResult struct {
	FinalText string           `json:"final_text"`
	Usage     TurnUsage        `json:"usage"`
	Messages  []engine.Message `json:"messages"`
}

type WorkflowResumeResult struct {
	Workflow tools.WorkflowSnapshot        `json:"workflow"`
	Tasks    []tools.FanoutTaskResultAlias `json:"tasks"`
	Agents   []engine.AgentSnapshot        `json:"agents"`
}

type WorkflowResumeOptions struct {
	TimeoutSeconds int
	OnlyFailed     bool
	TaskNames      []string
}

type MCPSummary struct {
	ConfigPath  string                    `json:"config_path,omitempty"`
	ServerCount int                       `json:"server_count"`
	ToolCount   int                       `json:"tool_count"`
	Statuses    []mcpruntime.ServerStatus `json:"statuses"`
}

type HookConfig struct {
	BeforeTool string `json:"before_tool,omitempty"`
	AfterTool  string `json:"after_tool,omitempty"`
	AfterTurn  string `json:"after_turn,omitempty"`
	AgentEvent string `json:"agent_event,omitempty"`
	Timeout    string `json:"timeout,omitempty"`
}

type TurnStreamEvent struct {
	Type         string          `json:"type"`
	AgentID      string          `json:"agent_id,omitempty"`
	AgentName    string          `json:"agent_name,omitempty"`
	ToolName     string          `json:"tool_name,omitempty"`
	Text         string          `json:"text,omitempty"`
	Result       *TurnResult     `json:"result,omitempty"`
	Session      *SessionSummary `json:"session,omitempty"`
	Error        string          `json:"error,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
	ErrorRetryOK bool            `json:"retryable,omitempty"`
}

func NewClient(baseURL, token string, httpClient *http.Client) *Client {
	baseURL = strings.TrimSpace(baseURL)
	baseURL = strings.TrimRight(baseURL, "/")
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 30 * time.Minute,
		}
	}
	return &Client{
		baseURL:    baseURL,
		token:      strings.TrimSpace(token),
		httpClient: httpClient,
	}
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" && e.Code == "" {
		return fmt.Sprintf("remote daemon returned status %d", e.StatusCode)
	}
	if e.StatusCode > 0 {
		if e.Code != "" {
			return fmt.Sprintf("remote daemon returned status %d (%s): %s", e.StatusCode, e.Code, e.Message)
		}
		return fmt.Sprintf("remote daemon returned status %d: %s", e.StatusCode, e.Message)
	}
	if e.Code != "" {
		return fmt.Sprintf("remote daemon error (%s): %s", e.Code, e.Message)
	}
	return e.Message
}

func (c *Client) BaseURL() string {
	return c.baseURL
}

func (c *Client) ListSessions(ctx context.Context) ([]SessionSummary, error) {
	var response struct {
		Sessions []SessionSummary `json:"sessions"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/sessions", nil, &response); err != nil {
		return nil, err
	}
	return response.Sessions, nil
}

func (c *Client) GetSession(ctx context.Context, sessionID string) (SessionSummary, error) {
	var response struct {
		Session SessionSummary `json:"session"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/sessions/"+url.PathEscape(strings.TrimSpace(sessionID)), nil, &response); err != nil {
		return SessionSummary{}, err
	}
	return response.Session, nil
}

func (c *Client) CreateSession(ctx context.Context, sessionID string, resume bool) (SessionSummary, error) {
	var response struct {
		Session SessionSummary `json:"session"`
	}
	payload := map[string]any{}
	if strings.TrimSpace(sessionID) != "" {
		payload["session_id"] = strings.TrimSpace(sessionID)
	}
	if resume {
		payload["resume"] = true
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/sessions", payload, &response); err != nil {
		return SessionSummary{}, err
	}
	return response.Session, nil
}

func (c *Client) EnsureSession(ctx context.Context, sessionID string) (SessionSummary, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return c.CreateSession(ctx, "", false)
	}
	summary, err := c.GetSession(ctx, sessionID)
	if err == nil {
		return summary, nil
	}
	var remoteErr *Error
	if !errors.As(err, &remoteErr) || remoteErr.StatusCode != http.StatusNotFound {
		return SessionSummary{}, err
	}
	return c.CreateSession(ctx, sessionID, false)
}

func (c *Client) ListMessages(ctx context.Context, sessionID string, limit int) ([]engine.Message, error) {
	path := "/v1/sessions/" + url.PathEscape(strings.TrimSpace(sessionID)) + "/messages"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	var response struct {
		Messages []engine.Message `json:"messages"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, err
	}
	return response.Messages, nil
}

func (c *Client) RunTurn(ctx context.Context, sessionID, prompt string, timeoutSeconds int) (TurnResult, SessionSummary, error) {
	var response struct {
		Result  TurnResult     `json:"result"`
		Session SessionSummary `json:"session"`
	}
	payload := map[string]any{
		"prompt": strings.TrimSpace(prompt),
	}
	if timeoutSeconds > 0 {
		payload["timeout_seconds"] = timeoutSeconds
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/sessions/"+url.PathEscape(strings.TrimSpace(sessionID))+"/turns", payload, &response); err != nil {
		return TurnResult{}, SessionSummary{}, err
	}
	return response.Result, response.Session, nil
}

func (c *Client) StreamTurn(ctx context.Context, sessionID, prompt string, timeoutSeconds int, handle func(TurnStreamEvent)) (TurnResult, SessionSummary, error) {
	payload := map[string]any{
		"prompt": strings.TrimSpace(prompt),
	}
	if timeoutSeconds > 0 {
		payload["timeout_seconds"] = timeoutSeconds
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return TurnResult{}, SessionSummary{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/sessions/"+url.PathEscape(strings.TrimSpace(sessionID))+"/turns/stream", bytes.NewReader(data))
	if err != nil {
		return TurnResult{}, SessionSummary{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	c.applyAuth(req)
	req.Header.Set(diag.TraceHeader, diag.NewTraceID())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return TurnResult{}, SessionSummary{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if readErr != nil {
			return TurnResult{}, SessionSummary{}, readErr
		}
		return TurnResult{}, SessionSummary{}, parseRemoteError(resp.StatusCode, body)
	}

	parser := newSSEParser(resp.Body)
	for {
		_, raw, err := parser.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return TurnResult{}, SessionSummary{}, io.ErrUnexpectedEOF
			}
			return TurnResult{}, SessionSummary{}, err
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}

		var event TurnStreamEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return TurnResult{}, SessionSummary{}, err
		}
		if handle != nil {
			handle(event)
		}
		if event.Error != "" {
			return TurnResult{}, SessionSummary{}, &Error{
				Code:      strings.TrimSpace(event.ErrorCode),
				Message:   strings.TrimSpace(event.Error),
				Retryable: event.ErrorRetryOK,
			}
		}
		if event.Result != nil && event.Session != nil {
			return *event.Result, *event.Session, nil
		}
	}
}

func (c *Client) SaveSession(ctx context.Context, sessionID string) (SessionSummary, error) {
	var response struct {
		Session SessionSummary `json:"session"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/sessions/"+url.PathEscape(strings.TrimSpace(sessionID))+"/save", map[string]any{}, &response); err != nil {
		return SessionSummary{}, err
	}
	return response.Session, nil
}

func (c *Client) GetPolicy(ctx context.Context, sessionID string) (engine.PermissionPolicy, error) {
	var response struct {
		Policy engine.PermissionPolicy `json:"policy"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/sessions/"+url.PathEscape(strings.TrimSpace(sessionID))+"/policy", nil, &response); err != nil {
		return engine.PermissionPolicy{}, err
	}
	return response.Policy, nil
}

func (c *Client) GetHooks(ctx context.Context, sessionID string) (HookConfig, error) {
	var response struct {
		Hooks HookConfig `json:"hooks"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/sessions/"+url.PathEscape(strings.TrimSpace(sessionID))+"/hooks", nil, &response); err != nil {
		return HookConfig{}, err
	}
	return response.Hooks, nil
}

func (c *Client) GetMCP(ctx context.Context, sessionID string) (MCPSummary, error) {
	var response struct {
		MCP MCPSummary `json:"mcp"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/sessions/"+url.PathEscape(strings.TrimSpace(sessionID))+"/mcp", nil, &response); err != nil {
		return MCPSummary{}, err
	}
	return response.MCP, nil
}

func (c *Client) ListMCPResources(ctx context.Context, sessionID, serverName string) ([]mcpruntime.Resource, error) {
	var response struct {
		Resources []mcpruntime.Resource `json:"resources"`
	}
	if err := c.doJSON(ctx, http.MethodGet, withServerQuery("/v1/sessions/"+url.PathEscape(strings.TrimSpace(sessionID))+"/mcp/resources", serverName), nil, &response); err != nil {
		return nil, err
	}
	return response.Resources, nil
}

func (c *Client) ListMCPResourceTemplates(ctx context.Context, sessionID, serverName string) ([]mcpruntime.ResourceTemplate, error) {
	var response struct {
		ResourceTemplates []mcpruntime.ResourceTemplate `json:"resource_templates"`
	}
	if err := c.doJSON(ctx, http.MethodGet, withServerQuery("/v1/sessions/"+url.PathEscape(strings.TrimSpace(sessionID))+"/mcp/resource-templates", serverName), nil, &response); err != nil {
		return nil, err
	}
	return response.ResourceTemplates, nil
}

func (c *Client) ListMCPPrompts(ctx context.Context, sessionID, serverName string) ([]mcpruntime.Prompt, error) {
	var response struct {
		Prompts []mcpruntime.Prompt `json:"prompts"`
	}
	if err := c.doJSON(ctx, http.MethodGet, withServerQuery("/v1/sessions/"+url.PathEscape(strings.TrimSpace(sessionID))+"/mcp/prompts", serverName), nil, &response); err != nil {
		return nil, err
	}
	return response.Prompts, nil
}

func (c *Client) ReadMCPResource(ctx context.Context, sessionID, serverName, resourceURI string) (mcpruntime.ResourceDetails, error) {
	var response struct {
		Resource mcpruntime.ResourceDetails `json:"resource"`
	}
	payload := map[string]any{
		"server": strings.TrimSpace(serverName),
		"uri":    strings.TrimSpace(resourceURI),
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/sessions/"+url.PathEscape(strings.TrimSpace(sessionID))+"/mcp/read-resource", payload, &response); err != nil {
		return mcpruntime.ResourceDetails{}, err
	}
	return response.Resource, nil
}

func (c *Client) GetMCPPrompt(ctx context.Context, sessionID, serverName, promptName string, arguments map[string]string) (mcpruntime.PromptDetails, error) {
	var response struct {
		Prompt mcpruntime.PromptDetails `json:"prompt"`
	}
	payload := map[string]any{
		"server": strings.TrimSpace(serverName),
		"name":   strings.TrimSpace(promptName),
	}
	if len(arguments) > 0 {
		payload["arguments"] = arguments
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/sessions/"+url.PathEscape(strings.TrimSpace(sessionID))+"/mcp/get-prompt", payload, &response); err != nil {
		return mcpruntime.PromptDetails{}, err
	}
	return response.Prompt, nil
}

func (c *Client) ListAgents(ctx context.Context, sessionID string) ([]engine.AgentSnapshot, error) {
	var response struct {
		Agents []engine.AgentSnapshot `json:"agents"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/sessions/"+url.PathEscape(strings.TrimSpace(sessionID))+"/agents", nil, &response); err != nil {
		return nil, err
	}
	return response.Agents, nil
}

func (c *Client) SendAgent(ctx context.Context, sessionID, agentID, prompt string, background bool) (engine.AgentSnapshot, error) {
	var response struct {
		Agent engine.AgentSnapshot `json:"agent"`
	}
	payload := map[string]any{
		"prompt": strings.TrimSpace(prompt),
	}
	if background {
		payload["background"] = true
	}
	path := "/v1/sessions/" + url.PathEscape(strings.TrimSpace(sessionID)) + "/agents/" + url.PathEscape(strings.TrimSpace(agentID)) + "/send"
	if err := c.doJSON(ctx, http.MethodPost, path, payload, &response); err != nil {
		return engine.AgentSnapshot{}, err
	}
	return response.Agent, nil
}

func (c *Client) CancelAgent(ctx context.Context, sessionID, agentID string, recursive bool) (engine.AgentSnapshot, error) {
	var response struct {
		Agent engine.AgentSnapshot `json:"agent"`
	}
	payload := map[string]any{}
	if recursive {
		payload["recursive"] = true
	}
	path := "/v1/sessions/" + url.PathEscape(strings.TrimSpace(sessionID)) + "/agents/" + url.PathEscape(strings.TrimSpace(agentID)) + "/cancel"
	if err := c.doJSON(ctx, http.MethodPost, path, payload, &response); err != nil {
		return engine.AgentSnapshot{}, err
	}
	return response.Agent, nil
}

func (c *Client) WaitAgent(ctx context.Context, sessionID, agentID string, timeoutSeconds int) (engine.AgentSnapshot, error) {
	var response struct {
		Agent engine.AgentSnapshot `json:"agent"`
	}
	payload := map[string]any{}
	if timeoutSeconds > 0 {
		payload["timeout_seconds"] = timeoutSeconds
	}
	path := "/v1/sessions/" + url.PathEscape(strings.TrimSpace(sessionID)) + "/agents/" + url.PathEscape(strings.TrimSpace(agentID)) + "/wait"
	if err := c.doJSON(ctx, http.MethodPost, path, payload, &response); err != nil {
		return engine.AgentSnapshot{}, err
	}
	return response.Agent, nil
}

func (c *Client) ListWorkflows(ctx context.Context, sessionID string) ([]tools.WorkflowSummary, error) {
	var response struct {
		Workflows []tools.WorkflowSummary `json:"workflows"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/sessions/"+url.PathEscape(strings.TrimSpace(sessionID))+"/workflows", nil, &response); err != nil {
		return nil, err
	}
	return response.Workflows, nil
}

func (c *Client) GetWorkflow(ctx context.Context, sessionID, workflowID string) (tools.WorkflowSnapshot, error) {
	var response struct {
		Workflow tools.WorkflowSnapshot `json:"workflow"`
	}
	path := "/v1/sessions/" + url.PathEscape(strings.TrimSpace(sessionID)) + "/workflows/" + url.PathEscape(strings.TrimSpace(workflowID))
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &response); err != nil {
		return tools.WorkflowSnapshot{}, err
	}
	return response.Workflow, nil
}

func (c *Client) ListWorkflowTasks(ctx context.Context, sessionID, workflowID, statusFilter, nameFilter string) ([]tools.WorkflowTaskState, error) {
	var response struct {
		Tasks []tools.WorkflowTaskState `json:"tasks"`
	}
	path := "/v1/sessions/" + url.PathEscape(strings.TrimSpace(sessionID)) + "/workflows/" + url.PathEscape(strings.TrimSpace(workflowID)) + "/tasks"
	query := url.Values{}
	if strings.TrimSpace(statusFilter) != "" {
		query.Set("status", strings.TrimSpace(statusFilter))
	}
	if strings.TrimSpace(nameFilter) != "" {
		query.Set("name", strings.TrimSpace(nameFilter))
	}
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, err
	}
	return response.Tasks, nil
}

func (c *Client) ResumeWorkflow(ctx context.Context, sessionID, workflowID string, options WorkflowResumeOptions) (WorkflowResumeResult, error) {
	var response WorkflowResumeResult
	payload := map[string]any{}
	if options.TimeoutSeconds > 0 {
		payload["timeout_seconds"] = options.TimeoutSeconds
	}
	if options.OnlyFailed {
		payload["only_failed"] = true
	}
	if len(options.TaskNames) > 0 {
		payload["task_names"] = append([]string(nil), options.TaskNames...)
	}
	path := "/v1/sessions/" + url.PathEscape(strings.TrimSpace(sessionID)) + "/workflows/" + url.PathEscape(strings.TrimSpace(workflowID)) + "/resume"
	if err := c.doJSON(ctx, http.MethodPost, path, payload, &response); err != nil {
		return WorkflowResumeResult{}, err
	}
	return response, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, requestBody any, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		data, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.applyAuth(req)
	req.Header.Set(diag.TraceHeader, diag.NewTraceID())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseRemoteError(resp.StatusCode, data)
	}
	if responseBody == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, responseBody)
}

func parseRemoteError(statusCode int, data []byte) *Error {
	var payload struct {
		Error     string `json:"error"`
		Code      string `json:"code"`
		Retryable bool   `json:"retryable"`
	}
	if len(bytes.TrimSpace(data)) > 0 {
		_ = json.Unmarshal(data, &payload)
	}
	message := strings.TrimSpace(payload.Error)
	if message == "" {
		message = strings.TrimSpace(string(data))
	}
	return &Error{
		StatusCode: statusCode,
		Code:       strings.TrimSpace(payload.Code),
		Message:    message,
		Retryable:  payload.Retryable,
	}
}

func withServerQuery(path, serverName string) string {
	serverName = strings.TrimSpace(serverName)
	if serverName == "" {
		return path
	}
	values := url.Values{}
	values.Set("server", serverName)
	return path + "?" + values.Encode()
}

func (c *Client) applyAuth(req *http.Request) {
	if req == nil || strings.TrimSpace(c.token) == "" {
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
}
