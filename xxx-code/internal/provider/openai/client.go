package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
)

const defaultBaseURL = "https://api.openai.com/v1"

type AuthMode string

const (
	AuthModeBearer AuthMode = "bearer"
	AuthModeAPIKey AuthMode = "api_key"
)

type Client struct {
	apiKey     string
	baseURL    string
	authMode   AuthMode
	httpClient *http.Client
}

func NewClient(apiKey, baseURL string) *Client {
	return NewClientWithAuth(apiKey, baseURL, AuthModeBearer)
}

func NewAzureClient(apiKey, baseURL string) *Client {
	return NewClientWithAuth(apiKey, baseURL, AuthModeAPIKey)
}

func NewClientWithAuth(apiKey, baseURL string, authMode AuthMode) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		apiKey:   strings.TrimSpace(apiKey),
		baseURL:  normalizeBaseURL(baseURL, authMode),
		authMode: authMode,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

type requestPayload struct {
	Model         string           `json:"model"`
	Messages      []messagePayload `json:"messages"`
	Tools         []toolPayload    `json:"tools,omitempty"`
	MaxTokens     int              `json:"max_tokens,omitempty"`
	Temperature   *float64         `json:"temperature,omitempty"`
	Stream        bool             `json:"stream,omitempty"`
	StreamOptions *streamOptions   `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type messagePayload struct {
	Role       string            `json:"role"`
	Content    any               `json:"content,omitempty"`
	ToolCalls  []toolCallPayload `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
}

type toolPayload struct {
	Type     string       `json:"type"`
	Function functionSpec `json:"function"`
}

type functionSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type toolCallPayload struct {
	ID       string               `json:"id,omitempty"`
	Type     string               `json:"type"`
	Function toolCallFunctionData `json:"function"`
}

type toolCallFunctionData struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type responsePayload struct {
	ID      string          `json:"id"`
	Choices []choicePayload `json:"choices"`
	Usage   responseUsage   `json:"usage"`
	Error   *responseError  `json:"error,omitempty"`
}

type choicePayload struct {
	Index        int             `json:"index,omitempty"`
	Message      responseMessage `json:"message"`
	FinishReason string          `json:"finish_reason"`
}

type responseMessage struct {
	Role      string            `json:"role"`
	Content   any               `json:"content"`
	ToolCalls []toolCallPayload `json:"tool_calls,omitempty"`
}

type responseUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type responseError struct {
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

func buildRequestPayload(request engine.CompletionRequest, stream bool) requestPayload {
	payload := requestPayload{
		Model:     request.Model,
		Messages:  buildMessages(request.System, request.Messages),
		Tools:     buildTools(request.Tools),
		MaxTokens: request.MaxTokens,
		Stream:    stream,
	}
	if request.Temperature != 0 {
		value := request.Temperature
		payload.Temperature = &value
	}
	if stream {
		payload.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	return payload
}

func buildMessages(system string, messages []engine.Message) []messagePayload {
	payloads := make([]messagePayload, 0, len(messages)+1)
	if strings.TrimSpace(system) != "" {
		payloads = append(payloads, messagePayload{
			Role:    "system",
			Content: system,
		})
	}

	for _, message := range messages {
		if toolMessages := toolResultMessages(message); len(toolMessages) > 0 {
			payloads = append(payloads, toolMessages...)
			continue
		}

		payload := messagePayload{
			Role: string(message.Role),
		}
		text := message.Text()
		if text != "" {
			payload.Content = text
		}
		if message.Role == engine.RoleAssistant {
			payload.ToolCalls = buildToolCalls(message.Content)
		}
		if payload.Content == nil && len(payload.ToolCalls) == 0 {
			payload.Content = ""
		}
		payloads = append(payloads, payload)
	}
	return payloads
}

func toolResultMessages(message engine.Message) []messagePayload {
	if message.Role != engine.RoleUser || len(message.Content) == 0 {
		return nil
	}
	results := make([]messagePayload, 0, len(message.Content))
	for _, block := range message.Content {
		if block.Type != engine.BlockToolResult {
			return nil
		}
		results = append(results, messagePayload{
			Role:       "tool",
			Content:    block.Result,
			ToolCallID: block.ToolUseID,
		})
	}
	return results
}

func buildToolCalls(blocks []engine.Block) []toolCallPayload {
	toolCalls := make([]toolCallPayload, 0)
	for _, block := range blocks {
		if block.Type != engine.BlockToolUse {
			continue
		}
		toolCalls = append(toolCalls, toolCallPayload{
			ID:   block.ID,
			Type: "function",
			Function: toolCallFunctionData{
				Name:      block.Name,
				Arguments: string(orEmptyJSON(block.Input)),
			},
		})
	}
	return toolCalls
}

func buildTools(definitions []engine.ToolDefinition) []toolPayload {
	if len(definitions) == 0 {
		return nil
	}
	tools := make([]toolPayload, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, toolPayload{
			Type: "function",
			Function: functionSpec{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  definition.InputSchema,
			},
		})
	}
	return tools
}

func (c *Client) newChatRequest(ctx context.Context, payload requestPayload) (*http.Request, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("content-type", "application/json")
	switch c.authMode {
	case AuthModeAPIKey:
		httpRequest.Header.Set("api-key", c.apiKey)
	default:
		httpRequest.Header.Set("authorization", "Bearer "+c.apiKey)
	}
	return httpRequest, nil
}

func (c *Client) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	httpRequest, err := c.newChatRequest(ctx, buildRequestPayload(request, false))
	if err != nil {
		return engine.CompletionResponse{}, err
	}

	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return engine.CompletionResponse{}, err
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return engine.CompletionResponse{}, err
	}
	if response.StatusCode >= 400 {
		return engine.CompletionResponse{}, decodeAPIError(raw)
	}

	var decoded responsePayload
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return engine.CompletionResponse{}, err
	}
	return decodeResponsePayload(decoded), nil
}

func decodeResponsePayload(decoded responsePayload) engine.CompletionResponse {
	if len(decoded.Choices) == 0 {
		return engine.CompletionResponse{}
	}
	choice := decoded.Choices[0]
	content := make([]engine.Block, 0, 1+len(choice.Message.ToolCalls))

	if text := decodeMessageText(choice.Message.Content); text != "" {
		content = append(content, engine.Block{
			Type: engine.BlockText,
			Text: text,
		})
	}
	for _, toolCall := range choice.Message.ToolCalls {
		content = append(content, engine.Block{
			Type:  engine.BlockToolUse,
			ID:    toolCall.ID,
			Name:  toolCall.Function.Name,
			Input: json.RawMessage(orEmptyJSONString(toolCall.Function.Arguments)),
		})
	}

	return engine.CompletionResponse{
		ID:         decoded.ID,
		StopReason: choice.FinishReason,
		Message: engine.Message{
			Role:    engine.RoleAssistant,
			Content: content,
		},
		Usage: engine.Usage{
			InputTokens:  decoded.Usage.PromptTokens,
			OutputTokens: decoded.Usage.CompletionTokens,
		},
	}
}

func decodeMessageText(content any) string {
	switch value := content.(type) {
	case nil:
		return ""
	case string:
		return value
	case []any:
		var parts []string
		for _, item := range value {
			mapped, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if mapped["type"] == "text" {
				if text, ok := mapped["text"].(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func decodeAPIError(raw []byte) error {
	var payload struct {
		Error *responseError `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil && payload.Error != nil {
		label := strings.TrimSpace(firstNonEmpty(payload.Error.Code, payload.Error.Type))
		if label != "" {
			return fmt.Errorf("openai api error (%s): %s", label, payload.Error.Message)
		}
		return fmt.Errorf("openai api error: %s", payload.Error.Message)
	}
	return fmt.Errorf("openai api error: %s", string(raw))
}

func normalizeBaseURL(raw string, authMode AuthMode) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, "/")
	trimmed := strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
	switch authMode {
	case AuthModeAPIKey:
		if strings.HasSuffix(raw, "/openai/v1") {
			return raw
		}
		if !strings.Contains(trimmed, "/") {
			return raw + "/openai/v1"
		}
	default:
		if strings.HasSuffix(raw, "/v1") {
			return raw
		}
		if !strings.Contains(trimmed, "/") {
			return raw + "/v1"
		}
	}
	return raw
}

func orEmptyJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	return raw
}

func orEmptyJSONString(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}
	return raw
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
