package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/config"
	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	"github.com/caowenhua/x-agent/xxx-code/internal/persist"
	pluginruntime "github.com/caowenhua/x-agent/xxx-code/internal/plugins"
)

type cliPromptProvider struct{}

func (p *cliPromptProvider) CreateMessage(ctx context.Context, request engine.CompletionRequest) (engine.CompletionResponse, error) {
	_ = ctx
	return engine.CompletionResponse{
		Message: engine.NewTextMessage(engine.RoleAssistant, "reply:"+latestCLIUserText(request.Messages)),
	}, nil
}

func latestCLIUserText(messages []engine.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == engine.RoleUser {
			return messages[i].Text()
		}
	}
	return ""
}

func TestHandleCommandHelpIncludesPluginLifecycleCommands(t *testing.T) {
	app, out, _ := newTestApp(t)

	done, err := app.handleCommand(context.Background(), ":help")
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("expected help command to keep repl running")
	}

	help := out.String()
	for _, needle := range []string{
		":plugins-validate <path>",
		":plugins-install <path> [force]",
		":plugins-remove <name>",
		":plugins-reload",
	} {
		if !strings.Contains(help, needle) {
			t.Fatalf("expected help output to contain %q, got %q", needle, help)
		}
	}
}

func TestHandleCommandPluginLifecycle(t *testing.T) {
	app, out, errOut := newTestApp(t)
	sourceDir := writeCLIPluginSource(t, filepath.Join(app.config.WorkingDir, "plugin-sources"), "echoer", "#!/bin/sh\ncat\n")

	validateOutput := mustRunCommand(t, app, out, errOut, ":plugins-validate "+sourceDir)
	var report pluginruntime.ValidationReport
	if err := json.Unmarshal([]byte(validateOutput), &report); err != nil {
		t.Fatalf("unmarshal validation report: %v", err)
	}
	if !report.Valid || report.PluginName != "echoer" || report.ToolCount != 1 {
		t.Fatalf("unexpected validation report: %+v", report)
	}

	installOutput := mustRunCommand(t, app, out, errOut, ":plugins-install "+sourceDir)
	installSummary := decodePluginSummary(t, installOutput)
	if installSummary.PluginCount != 1 || installSummary.ToolCount != 1 {
		t.Fatalf("unexpected plugin summary after install: %+v", installSummary)
	}
	if _, err := os.Stat(filepath.Join(app.config.WorkingDir, ".xxx-code", "plugins", "echoer", "plugin.json")); err != nil {
		t.Fatalf("expected installed plugin manifest: %v", err)
	}

	removeOutput := mustRunCommand(t, app, out, errOut, ":plugins-remove echoer")
	removeSummary := decodePluginSummary(t, removeOutput)
	if removeSummary.PluginCount != 0 || removeSummary.ToolCount != 0 {
		t.Fatalf("unexpected plugin summary after remove: %+v", removeSummary)
	}
	if _, err := os.Stat(filepath.Join(app.config.WorkingDir, ".xxx-code", "plugins", "echoer")); !os.IsNotExist(err) {
		t.Fatalf("expected plugin directory to be removed, got err=%v", err)
	}
}

func TestRunPromptSavesSession(t *testing.T) {
	app, _, _ := newTestApp(t)
	installPromptRunner(app)

	result, err := app.runPrompt(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "reply:hello" {
		t.Fatalf("unexpected final text: %q", result.FinalText)
	}

	state, err := persist.Load(app.config.SessionFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Main) != 2 {
		t.Fatalf("expected saved session with 2 messages, got %d", len(state.Main))
	}
}

func TestPrintEventHandlesStreamingVerboseAndHooks(t *testing.T) {
	app, out, errOut := newTestApp(t)
	app.config.Verbose = true

	app.printEvent(engine.Event{Kind: engine.EventAssistantTextDelta, Text: "hello"})
	app.printEvent(engine.Event{Kind: engine.EventAssistantTextDone})
	app.printEvent(engine.Event{Kind: engine.EventToolCall, ToolName: "bash", Text: `{"command":"pwd"}`})
	app.printEvent(engine.Event{Kind: engine.EventToolResult, ToolName: "bash", Text: `{"output":"/tmp"}`})
	app.printEvent(engine.Event{Kind: engine.EventAssistantText, AgentName: "worker", Text: "done"})
	app.printEvent(engine.Event{Kind: engine.EventHookError, Text: "boom"})

	if got := out.String(); !strings.Contains(got, "hello\n[worker] done\n") {
		t.Fatalf("unexpected stdout: %q", got)
	}
	if got := errOut.String(); !strings.Contains(got, "tool bash") || !strings.Contains(got, "tool-result bash") || !strings.Contains(got, "hook error: boom") {
		t.Fatalf("unexpected stderr: %q", got)
	}
}

func TestHandleEventAutosavesAgentLifecycle(t *testing.T) {
	app, _, _ := newTestApp(t)
	app.session.Append(engine.NewTextMessage(engine.RoleUser, "hello"))

	app.handleEvent(engine.Event{Kind: engine.EventAgentSpawned, AgentID: "agent_1", AgentName: "worker"})

	if _, err := os.Stat(app.config.SessionFile); err != nil {
		t.Fatalf("expected session file to be written, got %v", err)
	}
}

func TestParseLocalPromptArguments(t *testing.T) {
	values, err := parseLocalPromptArguments([]string{"name=alice", "mode=fast"})
	if err != nil {
		t.Fatal(err)
	}
	if values["name"] != "alice" || values["mode"] != "fast" {
		t.Fatalf("unexpected parsed values: %+v", values)
	}

	_, err = parseLocalPromptArguments([]string{"invalid"})
	if err == nil || !strings.Contains(err.Error(), "expected key=value") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestRunPrintModeSavesSession(t *testing.T) {
	app, _, _ := newTestApp(t)
	installPromptRunner(app)
	app.config.Print = true
	app.config.Prompt = "hello"

	if err := app.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	state, err := persist.Load(app.config.SessionFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Main) != 2 {
		t.Fatalf("expected persisted print-mode session, got %d messages", len(state.Main))
	}
}

func TestRunREPLProcessesPromptAndQuit(t *testing.T) {
	app, out, errOut := newTestApp(t)
	installPromptRunner(app)

	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readPipe.Close()

	oldStdin := os.Stdin
	os.Stdin = readPipe
	defer func() { os.Stdin = oldStdin }()

	if _, err := writePipe.WriteString("hello\n:quit\n"); err != nil {
		t.Fatal(err)
	}
	if err := writePipe.Close(); err != nil {
		t.Fatal(err)
	}

	if err := app.runREPL(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "xxx-code (test-model)") || !strings.Contains(got, "reply:hello") {
		t.Fatalf("unexpected repl output: %q", got)
	}
	if got := errOut.String(); got != "" {
		t.Fatalf("unexpected repl stderr: %q", got)
	}
}

func TestResumeRestoresPersistedSession(t *testing.T) {
	app, _, _ := newTestApp(t)
	installPromptRunner(app)
	if _, err := app.runPrompt(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	var errOut bytes.Buffer
	restored := New(app.config, &out, &errOut)
	t.Cleanup(func() {
		_ = restored.closePlugins()
		_ = restored.closeMCP()
	})

	if err := restored.resume(); err != nil {
		t.Fatal(err)
	}
	if len(restored.session.Snapshot()) != 2 {
		t.Fatalf("expected restored session to have 2 messages, got %d", len(restored.session.Snapshot()))
	}
}

type pluginSummaryPayload struct {
	PluginDir   string                 `json:"plugin_dir,omitempty"`
	PluginCount int                    `json:"plugin_count"`
	ToolCount   int                    `json:"tool_count"`
	Statuses    []pluginruntime.Status `json:"statuses"`
}

func decodePluginSummary(t *testing.T, raw string) pluginSummaryPayload {
	t.Helper()
	var summary pluginSummaryPayload
	if err := json.Unmarshal([]byte(raw), &summary); err != nil {
		t.Fatalf("unmarshal plugin summary: %v", err)
	}
	return summary
}

func mustRunCommand(t *testing.T, app *App, out, errOut *bytes.Buffer, command string) string {
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

func newTestApp(t *testing.T) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		Provider:          "anthropic",
		BaseURL:           "https://api.anthropic.com",
		Version:           "2023-06-01",
		Model:             "test-model",
		MaxTurns:          4,
		MaxTokens:         512,
		MaxParallelAgents: 1,
		ContextBudget:     2048,
		CompactKeep:       4,
		WorkingDir:        dir,
		SessionFile:       filepath.Join(dir, ".xxx-code", "session.json"),
		ReadRoots:         []string{dir},
		WriteRoots:        []string{dir},
		BashEnabled:       true,
		Stream:            false,
		HookTimeout:       time.Second,
		ToolTimeout:       time.Second,
		SystemPrompt:      "test prompt",
	}
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := New(cfg, &out, &errOut)
	t.Cleanup(func() {
		_ = app.closePlugins()
		_ = app.closeMCP()
	})
	return app, &out, &errOut
}

func installPromptRunner(app *App) {
	app.runner = engine.NewRunner(&cliPromptProvider{}, app.registry, engine.RunnerConfig{
		Model:               app.config.Model,
		SystemPrompt:        app.config.SystemPrompt,
		MaxTokens:           app.config.MaxTokens,
		MaxTurns:            app.config.MaxTurns,
		StreamResponses:     false,
		ContextBudget:       app.config.ContextBudget,
		CompactKeepMessages: app.config.CompactKeep,
		WorkingDir:          app.config.WorkingDir,
		ToolTimeout:         app.config.ToolTimeout,
		HookTimeout:         app.config.HookTimeout,
		MaxAgentDepth:       3,
		MaxParallelAgents:   app.config.MaxParallelAgents,
		PermissionPolicy: engine.PermissionPolicy{
			ReadRoots:   app.config.ReadRoots,
			WriteRoots:  app.config.WriteRoots,
			ReadOnly:    app.config.ReadOnly,
			BashEnabled: app.config.BashEnabled,
		},
		EventHandler: app.handleEvent,
	})
}

func writeCLIPluginSource(t *testing.T, rootDir, pluginName, script string) string {
	t.Helper()
	pluginDir := filepath.Join(rootDir, pluginName)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "tool.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "name": "` + pluginName + `",
  "tools": [{
    "name": "echo",
    "description": "Echo plugin",
    "input_schema": {"type": "object"},
    "command": "./tool.sh"
  }]
}`
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return pluginDir
}
