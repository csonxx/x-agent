package anthropic

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
)

type streamEventPayload struct {
	Type         string           `json:"type"`
	Index        int              `json:"index,omitempty"`
	Message      *responsePayload `json:"message,omitempty"`
	ContentBlock *responseBlock   `json:"content_block,omitempty"`
	Delta        *streamDelta     `json:"delta,omitempty"`
	Usage        responseUsage    `json:"usage,omitempty"`
	Error        *responseError   `json:"error,omitempty"`
}

type streamDelta struct {
	Type        string `json:"type,omitempty"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

type streamAccumulator struct {
	response responsePayload
}

func (c *Client) CreateMessageStream(ctx context.Context, request engine.CompletionRequest, handle func(engine.StreamEvent)) (engine.CompletionResponse, error) {
	payload := buildRequestPayload(request)
	payload.Stream = true

	httpRequest, err := c.newMessagesRequest(ctx, payload)
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
		var apiErr responsePayload
		if err := json.Unmarshal(raw, &apiErr); err == nil && apiErr.Error != nil {
			return engine.CompletionResponse{}, fmt.Errorf("anthropic api error (%s): %s", apiErr.Error.Type, apiErr.Error.Message)
		}
		return engine.CompletionResponse{}, fmt.Errorf("anthropic api error: %s", string(raw))
	}

	parser := newSSEParser(response.Body)
	accumulator := &streamAccumulator{}

	for {
		_, data, err := parser.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if accumulator.response.ID == "" {
					return engine.CompletionResponse{}, io.ErrUnexpectedEOF
				}
				return decodeResponsePayload(accumulator.response), nil
			}
			return engine.CompletionResponse{}, err
		}
		if len(data) == 0 {
			continue
		}

		var event streamEventPayload
		if err := json.Unmarshal(data, &event); err != nil {
			return engine.CompletionResponse{}, fmt.Errorf("decode stream event: %w", err)
		}
		if event.Error != nil {
			return engine.CompletionResponse{}, fmt.Errorf("anthropic api error (%s): %s", event.Error.Type, event.Error.Message)
		}

		switch event.Type {
		case "ping":
			continue
		case "message_start":
			accumulator.startMessage(event.Message)
		case "content_block_start":
			accumulator.startBlock(event.Index, event.ContentBlock)
		case "content_block_delta":
			if text := accumulator.applyDelta(event.Index, event.Delta); text != "" && handle != nil {
				handle(engine.StreamEvent{
					Kind: engine.StreamEventTextDelta,
					Text: text,
				})
			}
		case "content_block_stop":
			continue
		case "message_delta":
			accumulator.applyMessageDelta(event.Delta, event.Usage)
		case "message_stop":
			return decodeResponsePayload(accumulator.response), nil
		}
	}
}

func (a *streamAccumulator) startMessage(message *responsePayload) {
	if message == nil {
		return
	}
	a.response = *message
	a.response.Content = append([]responseBlock(nil), message.Content...)
}

func (a *streamAccumulator) startBlock(index int, block *responseBlock) {
	if block == nil {
		return
	}
	a.ensureBlock(index)
	copyBlock := *block
	if copyBlock.Type == "tool_use" {
		copyBlock.Input = nil
	}
	a.response.Content[index] = copyBlock
}

func (a *streamAccumulator) applyDelta(index int, delta *streamDelta) string {
	if delta == nil {
		return ""
	}
	a.ensureBlock(index)

	switch delta.Type {
	case "text_delta":
		a.response.Content[index].Text += delta.Text
		return delta.Text
	case "input_json_delta":
		a.response.Content[index].Input = append(a.response.Content[index].Input, []byte(delta.PartialJSON)...)
	}
	return ""
}

func (a *streamAccumulator) applyMessageDelta(delta *streamDelta, usage responseUsage) {
	if delta != nil && delta.StopReason != "" {
		a.response.StopReason = delta.StopReason
	}
	if usage.InputTokens != 0 {
		a.response.Usage.InputTokens = usage.InputTokens
	}
	if usage.OutputTokens != 0 {
		a.response.Usage.OutputTokens = usage.OutputTokens
	}
}

func (a *streamAccumulator) ensureBlock(index int) {
	for len(a.response.Content) <= index {
		a.response.Content = append(a.response.Content, responseBlock{})
	}
}

type sseParser struct {
	reader *bufio.Reader
}

func newSSEParser(reader io.Reader) *sseParser {
	return &sseParser{reader: bufio.NewReader(reader)}
}

func (p *sseParser) Next() (string, []byte, error) {
	var (
		eventName string
		dataLines []string
	)

	for {
		line, err := p.reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", nil, err
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(dataLines) == 0 && eventName == "" {
				if errors.Is(err, io.EOF) {
					return "", nil, io.EOF
				}
				continue
			}
			return eventName, []byte(strings.Join(dataLines, "\n")), nil
		}

		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case strings.HasPrefix(line, ":"):
			// Ignore SSE comments.
		}

		if errors.Is(err, io.EOF) {
			if len(dataLines) == 0 && eventName == "" {
				return "", nil, io.EOF
			}
			return eventName, []byte(strings.Join(dataLines, "\n")), nil
		}
	}
}
