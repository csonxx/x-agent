package engine

import "context"

type CompletionRequest struct {
	Model       string
	System      string
	MaxTokens   int
	Messages    []Message
	Tools       []ToolDefinition
	Temperature float64
}

type CompletionResponse struct {
	ID         string
	StopReason string
	Message    Message
	Usage      Usage
}

type Provider interface {
	CreateMessage(ctx context.Context, request CompletionRequest) (CompletionResponse, error)
}

type StreamEventKind string

const (
	StreamEventTextDelta StreamEventKind = "text_delta"
)

type StreamEvent struct {
	Kind StreamEventKind
	Text string
}

type StreamingProvider interface {
	CreateMessageStream(ctx context.Context, request CompletionRequest, handle func(StreamEvent)) (CompletionResponse, error)
}
