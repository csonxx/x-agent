package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/caowenhua/x-agent/xxx-code/internal/config"
	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
)

func TestNewDefaultsNilWriters(t *testing.T) {
	app := New(config.Config{RemoteURL: "http://example.com"}, nil, nil)
	if app.out == nil || app.errOut == nil {
		t.Fatal("expected nil writers to default to io.Discard")
	}
}

func TestAppRunRequiresRemoteURL(t *testing.T) {
	app := New(config.Config{}, &bytes.Buffer{}, &bytes.Buffer{})

	err := app.Run(context.Background())
	if err == nil || err.Error() != "--remote-url is required" {
		t.Fatalf("expected missing remote url error, got %v", err)
	}
}

func TestHandleCommandHelpIncludesRemotePluginAndWorkflowCommands(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := New(config.Config{RemoteURL: "http://example.com"}, &out, &errOut)

	done, err := app.handleCommand(context.Background(), ":help")
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("expected help command to keep repl running")
	}

	help := out.String()
	for _, needle := range []string{
		":plugins-install <path> [force]",
		":plugins-remove <name>",
		":workflow-resume <id> [failed|task...]",
		":mcp-resource-templates [server]",
	} {
		if !strings.Contains(help, needle) {
			t.Fatalf("expected help output to contain %q, got %q", needle, help)
		}
	}
}

func TestPrintVerboseEventFallsBackToAgentID(t *testing.T) {
	var errOut bytes.Buffer
	app := New(config.Config{}, &bytes.Buffer{}, &errOut)

	app.printVerboseEvent(TurnStreamEvent{
		Type:    string(engine.EventAgentCompleted),
		AgentID: "agent_123",
	})

	if got := errOut.String(); !strings.Contains(got, "[agent] agent_completed agent_123") {
		t.Fatalf("unexpected verbose output: %q", got)
	}
}

func TestParsePromptArgumentsRejectsInvalidArgument(t *testing.T) {
	_, err := parsePromptArguments([]string{"bad"})
	if err == nil || !strings.Contains(err.Error(), "expected key=value") {
		t.Fatalf("expected invalid argument error, got %v", err)
	}
}

func TestPrintJSONWritesIndentedPayload(t *testing.T) {
	var out bytes.Buffer
	app := New(config.Config{RemoteURL: "http://example.com"}, &out, &bytes.Buffer{})

	err := app.printJSON(context.Background(), func(context.Context) (any, error) {
		return map[string]any{"hello": "world"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "\"hello\": \"world\"") {
		t.Fatalf("unexpected JSON output: %q", got)
	}
}

func TestOpenSessionUsesRemoteSessionID(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	app := &App{
		config: config.Config{RemoteURL: client.BaseURL(), RemoteSession: "named-remote"},
		client: client,
		out:    &bytes.Buffer{},
		errOut: &bytes.Buffer{},
	}

	session, err := app.openSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if session.ID != "named-remote" {
		t.Fatalf("unexpected session: %+v", session)
	}
}

func TestRunTurnWithoutStreamingPrintsFinalText(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	session, err := client.EnsureSession(context.Background(), "print-turn")
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	app := &App{
		config:    config.Config{RemoteURL: client.BaseURL(), Stream: false},
		client:    client,
		out:       &out,
		errOut:    &bytes.Buffer{},
		sessionID: session.ID,
	}

	result, updated, err := app.runTurn(context.Background(), "hello remote")
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "reply:hello remote" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if updated.ID != session.ID {
		t.Fatalf("unexpected updated session: %+v", updated)
	}
	if got := out.String(); !strings.Contains(got, "reply:hello remote") {
		t.Fatalf("expected final text in output, got %q", got)
	}
}

func TestRunTurnWithStreamingPrintsChunksAndVerboseEvents(t *testing.T) {
	client, cleanup := newStreamingTestClient(t)
	defer cleanup()

	session, err := client.EnsureSession(context.Background(), "stream-turn")
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		config:    config.Config{RemoteURL: client.BaseURL(), Stream: true, Verbose: true},
		client:    client,
		out:       &out,
		errOut:    &errOut,
		sessionID: session.ID,
	}

	result, updated, err := app.runTurn(context.Background(), "stream me")
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "reply:stream me" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if updated.ID != session.ID {
		t.Fatalf("unexpected updated session: %+v", updated)
	}
	if got := out.String(); !strings.Contains(got, "reply:stream me") {
		t.Fatalf("expected streamed output, got %q", got)
	}
}

func TestParsePromptArgumentsAcceptsKeyValuePairs(t *testing.T) {
	values, err := parsePromptArguments([]string{"name=alice", "mode=fast"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"mode":"fast","name":"alice"}` && string(data) != `{"name":"alice","mode":"fast"}` {
		t.Fatalf("unexpected argument map: %s", string(data))
	}
}
