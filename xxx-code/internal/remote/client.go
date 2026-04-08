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

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	"github.com/caowenhua/x-agent/xxx-code/internal/tools"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

type Error struct {
	StatusCode int
	Message    string
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

func NewClient(baseURL string, httpClient *http.Client) *Client {
	baseURL = strings.TrimSpace(baseURL)
	baseURL = strings.TrimRight(baseURL, "/")
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 30 * time.Minute,
		}
	}
	return &Client{
		baseURL:    baseURL,
		httpClient: httpClient,
	}
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return fmt.Sprintf("remote daemon returned status %d", e.StatusCode)
	}
	return fmt.Sprintf("remote daemon returned status %d: %s", e.StatusCode, e.Message)
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

func (c *Client) SaveSession(ctx context.Context, sessionID string) (SessionSummary, error) {
	var response struct {
		Session SessionSummary `json:"session"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/sessions/"+url.PathEscape(strings.TrimSpace(sessionID))+"/save", map[string]any{}, &response); err != nil {
		return SessionSummary{}, err
	}
	return response.Session, nil
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

func (c *Client) ResumeWorkflow(ctx context.Context, sessionID, workflowID string, timeoutSeconds int) (WorkflowResumeResult, error) {
	var response WorkflowResumeResult
	payload := map[string]any{}
	if timeoutSeconds > 0 {
		payload["timeout_seconds"] = timeoutSeconds
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
		var remoteErr struct {
			Error string `json:"error"`
		}
		if len(bytes.TrimSpace(data)) > 0 {
			_ = json.Unmarshal(data, &remoteErr)
		}
		message := strings.TrimSpace(remoteErr.Error)
		if message == "" {
			message = strings.TrimSpace(string(data))
		}
		return &Error{
			StatusCode: resp.StatusCode,
			Message:    message,
		}
	}
	if responseBody == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, responseBody)
}
