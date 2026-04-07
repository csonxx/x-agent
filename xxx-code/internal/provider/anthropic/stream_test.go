package anthropic

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
)

func TestCreateMessageStreamBuildsFinalMessageAndEmitsDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"stream":true`) {
			t.Fatalf("expected stream request, got %s", string(body))
		}

		w.Header().Set("content-type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\n")
		fmt.Fprint(w, `data: {"type":"message_start","message":{"id":"msg_stream","role":"assistant","content":[],"stop_reason":"","usage":{"input_tokens":12,"output_tokens":1}}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_start\n")
		fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_stop\n")
		fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
		fmt.Fprint(w, "event: content_block_start\n")
		fmt.Fprint(w, `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"echo_tool","input":{}}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, `data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"value\":\"hi\"}"}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_stop\n")
		fmt.Fprint(w, `data: {"type":"content_block_stop","index":1}`+"\n\n")
		fmt.Fprint(w, "event: message_delta\n")
		fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":22}}`+"\n\n")
		fmt.Fprint(w, "event: message_stop\n")
		fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, defaultVersion)
	var chunks []string
	response, err := client.CreateMessageStream(context.Background(), engine.CompletionRequest{
		Model:     "test-model",
		MaxTokens: 256,
		Messages:  []engine.Message{engine.NewTextMessage(engine.RoleUser, "hello")},
		Tools: []engine.ToolDefinition{
			{
				Name:        "echo_tool",
				Description: "echo text",
				InputSchema: map[string]any{"type": "object"},
			},
		},
	}, func(event engine.StreamEvent) {
		chunks = append(chunks, event.Text)
	})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Join(chunks, "") != "Hello" {
		t.Fatalf("unexpected streamed chunks: %#v", chunks)
	}
	if response.ID != "msg_stream" {
		t.Fatalf("unexpected response id: %s", response.ID)
	}
	if response.StopReason != "tool_use" {
		t.Fatalf("unexpected stop reason: %s", response.StopReason)
	}
	if response.Usage.InputTokens != 12 || response.Usage.OutputTokens != 22 {
		t.Fatalf("unexpected usage: %+v", response.Usage)
	}
	if response.Message.Text() != "Hello" {
		t.Fatalf("unexpected text: %q", response.Message.Text())
	}
	if len(response.Message.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(response.Message.Content))
	}
	if response.Message.Content[1].Type != engine.BlockToolUse {
		t.Fatalf("expected tool_use block, got %+v", response.Message.Content[1])
	}
	if response.Message.Content[1].Name != "echo_tool" {
		t.Fatalf("unexpected tool name: %s", response.Message.Content[1].Name)
	}
	if string(response.Message.Content[1].Input) != `{"value":"hi"}` {
		t.Fatalf("unexpected tool input: %s", string(response.Message.Content[1].Input))
	}
}
