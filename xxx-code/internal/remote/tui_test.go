package remote

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/caowenhua/x-agent/xxx-code/internal/config"
	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	tea "github.com/charmbracelet/bubbletea"
)

func TestRemoteTUIModelLifecycleAndCommands(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	session, err := client.EnsureSession(context.Background(), "remote-tui")
	if err != nil {
		t.Fatal(err)
	}

	app := &App{
		config:    config.Config{RemoteURL: client.BaseURL(), Stream: false},
		client:    client,
		out:       &bytes.Buffer{},
		errOut:    &bytes.Buffer{},
		sessionID: session.ID,
	}
	ui := &terminalUI{app: app, ctx: context.Background()}
	model := newTUIModel(ui)

	if got := model.View(); got != "loading xxx-code remote TUI..." {
		t.Fatalf("unexpected loading view: %q", got)
	}

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	model = updated.(tuiModel)
	model.input.SetValue("hello remote")

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tuiModel)
	if !model.running || model.status != "running..." {
		t.Fatalf("expected running state, got %+v", model)
	}

	msg := cmd().(tuiTurnDoneMsg)
	updated, _ = model.Update(msg)
	model = updated.(tuiModel)
	if model.running || model.status != "idle" {
		t.Fatalf("expected idle state after turn, got %+v", model)
	}
	if joined := strings.Join(model.lines, "\n"); !strings.Contains(joined, "assistant  reply:hello remote") {
		t.Fatalf("expected assistant reply in transcript, got %q", joined)
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	model = updated.(tuiModel)
	saveMsg := cmd().(tuiSaveDoneMsg)
	updated, _ = model.Update(saveMsg)
	model = updated.(tuiModel)
	if model.status != "session saved" {
		t.Fatalf("expected session saved status, got %q", model.status)
	}
}

func TestRemoteTUIModelConsumeEventsAndSidebar(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	app := &App{
		config: config.Config{
			RemoteURL:       client.BaseURL(),
			RemoteToken:     "token",
			RemoteTokenFile: "/tmp/token-file",
			Stream:          true,
			Verbose:         true,
		},
		client:    client,
		out:       &bytes.Buffer{},
		errOut:    &bytes.Buffer{},
		sessionID: "session-123",
	}
	ui := &terminalUI{app: app, ctx: context.Background()}
	model := newTUIModel(ui)
	model.width = 90
	model.height = 24
	model.ready = true
	model.layout()

	model.consumeEvent(TurnStreamEvent{Type: string(engine.EventAssistantTextDelta), Text: "Hel"})
	model.consumeEvent(TurnStreamEvent{Type: string(engine.EventAssistantTextDelta), Text: "lo"})
	model.consumeEvent(TurnStreamEvent{Type: string(engine.EventAssistantTextDone)})
	model.consumeEvent(TurnStreamEvent{Type: string(engine.EventAssistantText), AgentName: "worker", Text: "done"})
	model.consumeEvent(TurnStreamEvent{Type: string(engine.EventToolCall), ToolName: "bash", Text: `{"command":"pwd"}`})
	model.consumeEvent(TurnStreamEvent{Type: string(engine.EventToolResult), ToolName: "bash", Text: `{"output":"/tmp"}`})
	model.consumeEvent(TurnStreamEvent{Type: string(engine.EventAgentSpawned), AgentName: "worker", AgentID: "agent_1"})
	model.consumeEvent(TurnStreamEvent{Type: string(engine.EventAgentCompleted), AgentName: "worker"})
	model.consumeEvent(TurnStreamEvent{Type: string(engine.EventAgentCancelled), AgentName: "worker"})
	model.consumeEvent(TurnStreamEvent{Type: string(engine.EventHookError), Text: "hook exploded"})

	joined := strings.Join(model.lines, "\n")
	for _, needle := range []string{
		"assistant  Hello",
		"worker  done",
		"tool bash",
		"tool-result bash",
		"spawned agent worker (agent_1)",
		"agent worker completed",
		"agent worker cancelled",
		"hook  hook exploded",
	} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("expected transcript to contain %q, got %q", needle, joined)
		}
	}

	if sidebar := model.sidebarView(); !strings.Contains(sidebar, "auth       bearer") || !strings.Contains(sidebar, "session-123") {
		t.Fatalf("unexpected sidebar: %q", sidebar)
	}
	if status := model.statusView(); !strings.Contains(status, "remote=") {
		t.Fatalf("unexpected status: %q", status)
	}
	if view := model.View(); !strings.Contains(view, "xxx-code remote") {
		t.Fatalf("unexpected view: %q", view)
	}
}
