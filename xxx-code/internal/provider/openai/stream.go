package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	"github.com/caowenhua/x-agent/xxx-code/internal/sse"
)

type streamChunk struct {
	ID      string         `json:"id"`
	Choices []choiceChunk  `json:"choices"`
	Usage   responseUsage  `json:"usage"`
	Error   *responseError `json:"error,omitempty"`
}

type choiceChunk struct {
	Index        int        `json:"index"`
	Delta        deltaChunk `json:"delta"`
	FinishReason string     `json:"finish_reason"`
}

type deltaChunk struct {
	Role      string               `json:"role,omitempty"`
	Content   string               `json:"content,omitempty"`
	ToolCalls []toolCallDeltaChunk `json:"tool_calls,omitempty"`
}

type toolCallDeltaChunk struct {
	Index    int                  `json:"index"`
	ID       string               `json:"id,omitempty"`
	Type     string               `json:"type,omitempty"`
	Function toolCallFunctionData `json:"function,omitempty"`
}

type streamAccumulator struct {
	response   responsePayload
	textFilter reasoningFilter
}

func (c *Client) CreateMessageStream(ctx context.Context, request engine.CompletionRequest, handle func(engine.StreamEvent)) (engine.CompletionResponse, error) {
	httpRequest, err := c.newChatRequest(ctx, buildRequestPayload(request, true))
	if err != nil {
		return engine.CompletionResponse{}, err
	}

	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return engine.CompletionResponse{}, err
	}
	defer response.Body.Close()

	if response.StatusCode >= 400 {
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			return engine.CompletionResponse{}, err
		}
		return engine.CompletionResponse{}, decodeAPIError(raw)
	}

	parser := sse.NewParser(response.Body)
	accumulator := &streamAccumulator{}

	for {
		_, data, err := parser.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if accumulator.response.ID == "" {
					return engine.CompletionResponse{}, io.ErrUnexpectedEOF
				}
				if text := accumulator.finishText(); text != "" && handle != nil {
					handle(engine.StreamEvent{
						Kind: engine.StreamEventTextDelta,
						Text: text,
					})
				}
				return decodeResponsePayload(accumulator.response), nil
			}
			return engine.CompletionResponse{}, err
		}
		if len(data) == 0 {
			continue
		}
		if strings.TrimSpace(string(data)) == "[DONE]" {
			if text := accumulator.finishText(); text != "" && handle != nil {
				handle(engine.StreamEvent{
					Kind: engine.StreamEventTextDelta,
					Text: text,
				})
			}
			return decodeResponsePayload(accumulator.response), nil
		}

		var chunk streamChunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			return engine.CompletionResponse{}, fmt.Errorf("decode stream chunk: %w", err)
		}
		if chunk.Error != nil {
			label := strings.TrimSpace(firstNonEmpty(chunk.Error.Code, chunk.Error.Type))
			if label != "" {
				return engine.CompletionResponse{}, fmt.Errorf("openai api error (%s): %s", label, chunk.Error.Message)
			}
			return engine.CompletionResponse{}, fmt.Errorf("openai api error: %s", chunk.Error.Message)
		}
		accumulator.applyUsage(chunk.Usage)
		accumulator.response.ID = firstNonEmpty(chunk.ID, accumulator.response.ID)

		for _, choice := range chunk.Choices {
			if text := accumulator.applyDelta(choice.Delta); text != "" && handle != nil {
				handle(engine.StreamEvent{
					Kind: engine.StreamEventTextDelta,
					Text: text,
				})
			}
			if choice.FinishReason != "" {
				accumulator.response.Choices = []choicePayload{{
					Message:      accumulator.response.Choices[0].Message,
					FinishReason: choice.FinishReason,
				}}
			}
		}
	}
}

func (a *streamAccumulator) applyDelta(delta deltaChunk) string {
	if len(a.response.Choices) == 0 {
		a.response.Choices = []choicePayload{{}}
	}
	choice := &a.response.Choices[0]
	if choice.Message.Role == "" && delta.Role != "" {
		choice.Message.Role = delta.Role
	}
	var emittedText string
	if delta.Content != "" {
		emittedText = a.textFilter.Append(delta.Content)
		if emittedText != "" {
			current := decodeMessageText(choice.Message.Content)
			choice.Message.Content = current + emittedText
		}
	}
	for _, toolCall := range delta.ToolCalls {
		a.ensureToolCall(toolCall.Index)
		current := &choice.Message.ToolCalls[toolCall.Index]
		if toolCall.ID != "" {
			current.ID = toolCall.ID
		}
		if toolCall.Type != "" {
			current.Type = toolCall.Type
		}
		if toolCall.Function.Name != "" {
			current.Function.Name = toolCall.Function.Name
		}
		if toolCall.Function.Arguments != "" {
			current.Function.Arguments += toolCall.Function.Arguments
		}
	}
	return emittedText
}

func (a *streamAccumulator) finishText() string {
	text := a.textFilter.Finish()
	if text == "" {
		return ""
	}
	if len(a.response.Choices) == 0 {
		a.response.Choices = []choicePayload{{}}
	}
	current := decodeMessageText(a.response.Choices[0].Message.Content)
	a.response.Choices[0].Message.Content = current + text
	return text
}

func (a *streamAccumulator) ensureToolCall(index int) {
	if len(a.response.Choices) == 0 {
		a.response.Choices = []choicePayload{{}}
	}
	toolCalls := a.response.Choices[0].Message.ToolCalls
	for len(toolCalls) <= index {
		toolCalls = append(toolCalls, toolCallPayload{
			Type: "function",
		})
	}
	a.response.Choices[0].Message.ToolCalls = toolCalls
}

func (a *streamAccumulator) applyUsage(usage responseUsage) {
	if usage.PromptTokens != 0 {
		a.response.Usage.PromptTokens = usage.PromptTokens
	}
	if usage.CompletionTokens != 0 {
		a.response.Usage.CompletionTokens = usage.CompletionTokens
	}
}
