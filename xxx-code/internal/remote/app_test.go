package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caowenhua/x-agent/xxx-code/internal/config"
	"github.com/caowenhua/x-agent/xxx-code/internal/daemon"
	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	pluginruntime "github.com/caowenhua/x-agent/xxx-code/internal/plugins"
	"github.com/caowenhua/x-agent/xxx-code/internal/tools"
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

func TestHandleCommandPluginAndMCPLifecycleStory(t *testing.T) {
	cfg := newTestConfig(t)
	mcpServer := newRemoteMCPHTTPServer(t)
	defer mcpServer.Close()

	sourceDir := writeRemotePluginSource(t, filepath.Join(cfg.WorkingDir, "plugin-sources"), "echoer", "#!/bin/sh\ncat\n")
	configJSON := fmt.Sprintf(`{
  "mcpServers": {
    "tester": {
      "transport": "http",
      "url": %q
    }
  }
}`, mcpServer.URL)
	if err := os.WriteFile(filepath.Join(cfg.WorkingDir, ".mcp.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	server := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &remoteTestProvider{}
	})
	httpServer := httptest.NewServer(server.Handler())
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	client := NewClient(httpServer.URL, "", httpServer.Client())
	session, err := client.EnsureSession(context.Background(), "remote-command-story")
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		config:    config.Config{RemoteURL: client.BaseURL()},
		client:    client,
		out:       &out,
		errOut:    &errOut,
		sessionID: session.ID,
	}

	validateOutput := mustRunRemoteCommand(t, app, &out, &errOut, ":plugins-validate "+sourceDir)
	var report pluginruntime.ValidationReport
	if err := json.Unmarshal([]byte(validateOutput), &report); err != nil {
		t.Fatalf("unmarshal remote plugin validation: %v", err)
	}
	if !report.Valid || report.PluginName != "echoer" || report.ToolCount != 1 {
		t.Fatalf("unexpected remote plugin validation report: %+v", report)
	}

	installOutput := mustRunRemoteCommand(t, app, &out, &errOut, ":plugins-install "+sourceDir)
	var installed PluginSummary
	if err := json.Unmarshal([]byte(installOutput), &installed); err != nil {
		t.Fatalf("unmarshal remote plugin summary: %v", err)
	}
	if installed.PluginCount != 1 || installed.ToolCount != 1 {
		t.Fatalf("unexpected remote plugin install summary: %+v", installed)
	}

	if output := mustRunRemoteCommand(t, app, &out, &errOut, ":plugins"); !strings.Contains(output, `"plugin_count": 1`) {
		t.Fatalf("expected plugin status output, got %s", output)
	}

	reloadOutput := mustRunRemoteCommand(t, app, &out, &errOut, ":mcp-reload")
	var summary MCPSummary
	if err := json.Unmarshal([]byte(reloadOutput), &summary); err != nil {
		t.Fatalf("unmarshal remote MCP summary: %v", err)
	}
	if summary.ServerCount != 1 || summary.ToolCount != 1 {
		t.Fatalf("unexpected remote MCP summary: %+v", summary)
	}

	for _, tc := range []struct {
		command string
		needle  string
	}{
		{command: ":mcp-resources", needle: `"file:///a"`},
		{command: ":mcp-resource-templates", needle: `"file:///dir/{f}"`},
		{command: ":mcp-prompts", needle: `"greet"`},
		{command: ":mcp-read tester file:///a", needle: `"alpha"`},
		{command: ":mcp-prompt tester greet name=Pat", needle: `Say hi to Pat`},
	} {
		if output := mustRunRemoteCommand(t, app, &out, &errOut, tc.command); !strings.Contains(output, tc.needle) {
			t.Fatalf("expected %q output to contain %q, got %s", tc.command, tc.needle, output)
		}
	}

	removeOutput := mustRunRemoteCommand(t, app, &out, &errOut, ":plugins-remove echoer")
	var removed PluginSummary
	if err := json.Unmarshal([]byte(removeOutput), &removed); err != nil {
		t.Fatalf("unmarshal remote plugin remove summary: %v", err)
	}
	if removed.PluginCount != 0 || removed.ToolCount != 0 {
		t.Fatalf("unexpected remote plugin removal summary: %+v", removed)
	}
}

func TestHandleCommandAgentLifecycleStory(t *testing.T) {
	cfg := newTestConfig(t)
	server := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &remoteAgentProvider{}
	})
	httpServer := httptest.NewServer(server.Handler())
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	client := NewClient(httpServer.URL, "", httpServer.Client())
	foregroundSession, err := client.EnsureSession(context.Background(), "remote-agent-command-foreground")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.RunTurn(context.Background(), foregroundSession.ID, "delegate work", 0); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		config:    config.Config{RemoteURL: client.BaseURL()},
		client:    client,
		out:       &out,
		errOut:    &errOut,
		sessionID: foregroundSession.ID,
	}

	agentsOutput := mustRunRemoteCommand(t, app, &out, &errOut, ":agents")
	var agents []engine.AgentSnapshot
	if err := json.Unmarshal([]byte(agentsOutput), &agents); err != nil {
		t.Fatalf("unmarshal remote agents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected one foreground agent, got %+v", agents)
	}

	var idleID string
	for _, agent := range agents {
		if agent.Prompt == "child task" && idleID == "" {
			idleID = agent.ID
		}
	}
	if idleID == "" {
		t.Fatalf("expected a foreground child agent, got %+v", agents)
	}

	waitOutput := mustRunRemoteCommand(t, app, &out, &errOut, ":wait "+idleID)
	var waited engine.AgentSnapshot
	if err := json.Unmarshal([]byte(waitOutput), &waited); err != nil {
		t.Fatalf("unmarshal waited agent: %v", err)
	}
	if waited.Status != engine.AgentIdle {
		t.Fatalf("expected waited agent to be idle, got %+v", waited)
	}

	sendOutput := mustRunRemoteCommand(t, app, &out, &errOut, ":send "+idleID+" follow-up")
	var sent engine.AgentSnapshot
	if err := json.Unmarshal([]byte(sendOutput), &sent); err != nil {
		t.Fatalf("unmarshal sent agent: %v", err)
	}
	if sent.Result != "reply:follow-up" || sent.Status != engine.AgentIdle {
		t.Fatalf("unexpected send result: %+v", sent)
	}

	backgroundSession, err := client.EnsureSession(context.Background(), "remote-agent-command-background")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.RunTurn(context.Background(), backgroundSession.ID, "background work", 0); err != nil {
		t.Fatal(err)
	}
	app.sessionID = backgroundSession.ID

	backgroundOutput := mustRunRemoteCommand(t, app, &out, &errOut, ":agents")
	var backgroundAgents []engine.AgentSnapshot
	if err := json.Unmarshal([]byte(backgroundOutput), &backgroundAgents); err != nil {
		t.Fatalf("unmarshal background agents: %v", err)
	}

	var backgroundID string
	for _, agent := range backgroundAgents {
		if agent.Prompt == "block child" {
			backgroundID = agent.ID
			break
		}
	}
	if backgroundID == "" {
		t.Fatalf("expected a background agent, got %+v", backgroundAgents)
	}

	cancelOutput := mustRunRemoteCommand(t, app, &out, &errOut, ":cancel "+backgroundID)
	var cancelled engine.AgentSnapshot
	if err := json.Unmarshal([]byte(cancelOutput), &cancelled); err != nil {
		t.Fatalf("unmarshal cancelled agent: %v", err)
	}
	if cancelled.Status != engine.AgentCancelled {
		t.Fatalf("expected cancelled background agent, got %+v", cancelled)
	}
}

func TestHandleCommandWorkflowLifecycleStory(t *testing.T) {
	cfg := newTestConfig(t)
	server := daemon.New(cfg, io.Discard, io.Discard, func(config.Config) engine.Provider {
		return &remoteWorkflowProvider{}
	})
	httpServer := httptest.NewServer(server.Handler())
	defer func() {
		httpServer.Close()
		_ = server.Close()
	}()

	client := NewClient(httpServer.URL, "", httpServer.Client())
	session, err := client.EnsureSession(context.Background(), "remote-workflow-command-story")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.RunTurn(context.Background(), session.ID, "fanout work", 0); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		config:    config.Config{RemoteURL: client.BaseURL()},
		client:    client,
		out:       &out,
		errOut:    &errOut,
		sessionID: session.ID,
	}

	workflowsOutput := mustRunRemoteCommand(t, app, &out, &errOut, ":workflows")
	var workflows []tools.WorkflowSnapshot
	if err := json.Unmarshal([]byte(workflowsOutput), &workflows); err != nil {
		t.Fatalf("unmarshal workflows: %v", err)
	}
	if len(workflows) != 1 {
		t.Fatalf("expected one workflow, got %+v", workflows)
	}
	workflowID := workflows[0].ID

	workflowOutput := mustRunRemoteCommand(t, app, &out, &errOut, ":workflow "+workflowID)
	var snapshot tools.WorkflowSnapshot
	if err := json.Unmarshal([]byte(workflowOutput), &snapshot); err != nil {
		t.Fatalf("unmarshal workflow snapshot: %v", err)
	}
	if snapshot.ID != workflowID || snapshot.Status != tools.WorkflowCompleted {
		t.Fatalf("unexpected workflow snapshot: %+v", snapshot)
	}

	tasksOutput := mustRunRemoteCommand(t, app, &out, &errOut, ":workflow-tasks "+workflowID)
	var tasks []tools.WorkflowTaskState
	if err := json.Unmarshal([]byte(tasksOutput), &tasks); err != nil {
		t.Fatalf("unmarshal workflow tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected two workflow tasks, got %+v", tasks)
	}

	resumeOutput := mustRunRemoteCommand(t, app, &out, &errOut, ":workflow-resume "+workflowID+" one")
	var resumed WorkflowResumeResult
	if err := json.Unmarshal([]byte(resumeOutput), &resumed); err != nil {
		t.Fatalf("unmarshal resumed workflow: %v", err)
	}
	if resumed.Workflow.Status != tools.WorkflowCompleted {
		t.Fatalf("expected resumed workflow to complete, got %+v", resumed.Workflow)
	}
}

func TestRemoteRunREPLProcessesPromptSessionAndSaveCommands(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := New(config.Config{
		RemoteURL:     client.BaseURL(),
		RemoteSession: "repl-remote",
	}, &out, &errOut)
	app.client = client

	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readPipe.Close()

	oldStdin := os.Stdin
	os.Stdin = readPipe
	defer func() { os.Stdin = oldStdin }()

	if _, err := writePipe.WriteString("hello remote\n:session\n:save\n:quit\n"); err != nil {
		t.Fatal(err)
	}
	if err := writePipe.Close(); err != nil {
		t.Fatal(err)
	}

	if err := app.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := out.String(); !strings.Contains(got, "xxx-code remote (") || !strings.Contains(got, "reply:hello remote") || !strings.Contains(got, `"id": "repl-remote"`) || !strings.Contains(got, `"session_file":`) {
		t.Fatalf("unexpected remote repl output: %q", got)
	}
	if got := errOut.String(); got != "" {
		t.Fatalf("unexpected remote repl stderr: %q", got)
	}
}

func mustRunRemoteCommand(t *testing.T, app *App, out, errOut *bytes.Buffer, command string) string {
	t.Helper()
	out.Reset()
	errOut.Reset()

	done, err := app.handleCommand(context.Background(), command)
	if err != nil {
		t.Fatalf("run %q: %v", command, err)
	}
	if done {
		t.Fatalf("expected %q to keep repl running", command)
	}
	if errText := strings.TrimSpace(errOut.String()); errText != "" {
		t.Fatalf("unexpected stderr for %q: %s", command, errText)
	}
	return strings.TrimSpace(out.String())
}
