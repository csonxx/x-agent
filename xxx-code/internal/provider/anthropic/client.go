package anthropic

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

const defaultVersion = "2023-06-01"

type Client struct {
	apiKey     string
	baseURL    string
	version    string
	httpClient *http.Client
}

func NewClient(apiKey, baseURL, version string) *Client {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	if version == "" {
		version = defaultVersion
	}
	return &Client{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		version: version,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

type requestPayload struct {
	Model       string           `json:"model"`
	System      string           `json:"system,omitempty"`
	MaxTokens   int              `json:"max_tokens"`
	Temperature float64          `json:"temperature,omitempty"`
	Stream      bool             `json:"stream,omitempty"`
	Messages    []messagePayload `json:"messages"`
	Tools       []toolPayload    `json:"tools,omitempty"`
}

type messagePayload struct {
	Role    string           `json:"role"`
	Content []contentPayload `json:"content"`
}

type contentPayload struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type toolPayload struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type responsePayload struct {
	ID         string          `json:"id"`
	Role       string          `json:"role"`
	Content    []responseBlock `json:"content"`
	StopReason string          `json:"stop_reason"`
	Usage      responseUsage   `json:"usage"`
	Error      *responseError  `json:"error,omitempty"`
}

type responseBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type responseUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type responseError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func buildRequestPayload(request engine.CompletionRequest) requestPayload {
	payload := requestPayload{
		Model:       request.Model,
		System:      request.System,
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		Messages:    make([]messagePayload, 0, len(request.Messages)),
		Tools:       make([]toolPayload, 0, len(request.Tools)),
	}

	for _, msg := range request.Messages {
		content := make([]contentPayload, 0, len(msg.Content))
		for _, block := range msg.Content {
			item := contentPayload{
				Type: string(block.Type),
			}
			switch block.Type {
			case engine.BlockText:
				item.Text = block.Text
			case engine.BlockToolUse:
				item.ID = block.ID
				item.Name = block.Name
				item.Input = block.Input
			case engine.BlockToolResult:
				item.ToolUseID = block.ToolUseID
				item.Content = block.Result
				item.IsError = block.IsError
			}
			content = append(content, item)
		}
		payload.Messages = append(payload.Messages, messagePayload{
			Role:    string(msg.Role),
			Content: content,
		})
	}

	for _, tool := range request.Tools {
		payload.Tools = append(payload.Tools, toolPayload{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
		})
	}
	return payload
}

func (c *Client) newMessagesRequest(ctx context.Context, payload requestPayload) (*http.Request, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("content-type", "application/json")
	httpRequest.Header.Set("x-api-key", c.apiKey)
	httpRequest.Header.Set("anthropic-version", c.version)
	return httpRequest, nil
}

func (c *Client) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	httpRequest, err := c.newMessagesRequest(ctx, buildRequestPayload(request))
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
		var apiErr responsePayload
		if err := json.Unmarshal(raw, &apiErr); err == nil && apiErr.Error != nil {
			return engine.CompletionResponse{}, fmt.Errorf("anthropic api error (%s): %s", apiErr.Error.Type, apiErr.Error.Message)
		}
		return engine.CompletionResponse{}, fmt.Errorf("anthropic api error: %s", string(raw))
	}

	var decoded responsePayload
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return engine.CompletionResponse{}, err
	}

	return decodeResponsePayload(decoded), nil
}

func decodeResponsePayload(decoded responsePayload) engine.CompletionResponse {
	content := make([]engine.Block, 0, len(decoded.Content))
	for _, block := range decoded.Content {
		switch block.Type {
		case "text":
			content = append(content, engine.Block{
				Type: engine.BlockText,
				Text: block.Text,
			})
		case "tool_use":
			content = append(content, engine.Block{
				Type:  engine.BlockToolUse,
				ID:    block.ID,
				Name:  block.Name,
				Input: block.Input,
			})
		}
	}

	return engine.CompletionResponse{
		ID:         decoded.ID,
		StopReason: decoded.StopReason,
		Message: engine.Message{
			Role:    engine.RoleAssistant,
			Content: content,
		},
		Usage: engine.Usage{
			InputTokens:  decoded.Usage.InputTokens,
			OutputTokens: decoded.Usage.OutputTokens,
		},
	}
}
