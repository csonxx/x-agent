package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
)

func TestCreateMessageDecodesTextAndToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if got := r.Header.Get("authorization"); got != "Bearer test-key" {
			t.Fatalf("unexpected auth header: %q", got)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"model":"gpt-4.1"`) {
			t.Fatalf("expected model in request, got %s", string(body))
		}
		if !strings.Contains(string(body), `"tool_calls"`) {
			t.Fatalf("expected tool call history in request, got %s", string(body))
		}
		if !strings.Contains(string(body), `"role":"tool"`) {
			t.Fatalf("expected tool result message in request, got %s", string(body))
		}

		w.Header().Set("content-type", "application/json")
		fmt.Fprint(w, `{
  "id": "chatcmpl_test",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "Hello",
      "tool_calls": [{
        "id": "call_1",
        "type": "function",
        "function": {
          "name": "echo_tool",
          "arguments": "{\"value\":\"hi\"}"
        }
      }]
    },
    "finish_reason": "tool_calls"
  }],
  "usage": {
    "prompt_tokens": 12,
    "completion_tokens": 7
  }
}`)
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL)
	input, _ := json.Marshal(map[string]any{"value": "before"})
	response, err := client.CreateMessage(context.Background(), engine.CompletionRequest{
		Model:     "gpt-4.1",
		System:    "system prompt",
		MaxTokens: 128,
		Messages: []engine.Message{
			engine.NewTextMessage(engine.RoleUser, "hello"),
			{
				Role: engine.RoleAssistant,
				Content: []engine.Block{
					{Type: engine.BlockText, Text: "calling"},
					{Type: engine.BlockToolUse, ID: "call_prev", Name: "echo_tool", Input: input},
				},
			},
			{
				Role: engine.RoleUser,
				Content: []engine.Block{
					{Type: engine.BlockToolResult, ToolUseID: "call_prev", Result: "ok"},
				},
			},
		},
		Tools: []engine.ToolDefinition{{
			Name:        "echo_tool",
			Description: "Echo text",
			InputSchema: map[string]any{"type": "object"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if response.ID != "chatcmpl_test" {
		t.Fatalf("unexpected response id: %s", response.ID)
	}
	if response.StopReason != "tool_calls" {
		t.Fatalf("unexpected stop reason: %s", response.StopReason)
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
		t.Fatalf("unexpected tool input: %s", response.Message.Content[1].Input)
	}
	if response.Usage.InputTokens != 12 || response.Usage.OutputTokens != 7 {
		t.Fatalf("unexpected usage: %+v", response.Usage)
	}
}

func TestCreateMessageStreamBuildsFinalMessageAndEmitsDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"stream":true`) {
			t.Fatalf("expected stream request, got %s", string(body))
		}
		if !strings.Contains(string(body), `"include_usage":true`) {
			t.Fatalf("expected usage stream option, got %s", string(body))
		}

		w.Header().Set("content-type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"chatcmpl_stream","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":""}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl_stream","choices":[{"index":0,"delta":{"content":"lo","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"echo_tool","arguments":"{\"value\":\"h"}}]},"finish_reason":""}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl_stream","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"i\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":5}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL)
	var chunks []string
	response, err := client.CreateMessageStream(context.Background(), engine.CompletionRequest{
		Model:     "gpt-4.1",
		MaxTokens: 256,
		Messages:  []engine.Message{engine.NewTextMessage(engine.RoleUser, "hello")},
		Tools: []engine.ToolDefinition{{
			Name:        "echo_tool",
			Description: "Echo text",
			InputSchema: map[string]any{"type": "object"},
		}},
	}, func(event engine.StreamEvent) {
		chunks = append(chunks, event.Text)
	})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Join(chunks, "") != "Hello" {
		t.Fatalf("unexpected streamed chunks: %#v", chunks)
	}
	if response.ID != "chatcmpl_stream" {
		t.Fatalf("unexpected response id: %s", response.ID)
	}
	if response.StopReason != "tool_calls" {
		t.Fatalf("unexpected stop reason: %s", response.StopReason)
	}
	if response.Message.Text() != "Hello" {
		t.Fatalf("unexpected text: %q", response.Message.Text())
	}
	if len(response.Message.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(response.Message.Content))
	}
	if string(response.Message.Content[1].Input) != `{"value":"hi"}` {
		t.Fatalf("unexpected tool input: %s", response.Message.Content[1].Input)
	}
	if response.Usage.InputTokens != 9 || response.Usage.OutputTokens != 5 {
		t.Fatalf("unexpected usage: %+v", response.Usage)
	}
}

func TestNewAzureClientUsesAPIKeyHeaderAndNormalizesBaseURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("api-key"); got != "azure-key" {
			t.Fatalf("unexpected api-key header: %q", got)
		}
		if got := r.URL.Path; got != "/openai/v1/chat/completions" {
			t.Fatalf("unexpected request path: %s", got)
		}
		w.Header().Set("content-type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl_azure","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer server.Close()

	client := NewAzureClient("azure-key", server.URL)
	response, err := client.CreateMessage(context.Background(), engine.CompletionRequest{
		Model:     "deployment-name",
		MaxTokens: 64,
		Messages:  []engine.Message{engine.NewTextMessage(engine.RoleUser, "ping")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Message.Text() != "ok" {
		t.Fatalf("unexpected response: %+v", response)
	}
}
