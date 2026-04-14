package cli

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	tea "github.com/charmbracelet/bubbletea"
)

func TestTUIModelLifecycleAndCommands(t *testing.T) {
	app, _, _ := newTestApp(t)
	installPromptRunner(app)
	app.config.Resume = true
	ui := &terminalUI{app: app, ctx: context.Background()}

	model := newTUIModel(ui)
	if !strings.Contains(strings.Join(model.lines, "\n"), "resumed session from") {
		t.Fatalf("expected resume banner, got %+v", model.lines)
	}
	if got := model.View(); got != "loading xxx-code TUI..." {
		t.Fatalf("unexpected loading view: %q", got)
	}

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	model = updated.(tuiModel)
	if !model.ready || model.viewport.Width == 0 {
		t.Fatalf("expected laid out model, got %+v", model)
	}

	model.input.SetValue("hello")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tuiModel)
	if !model.running || model.status != "running..." {
		t.Fatalf("expected running state after enter, got %+v", model)
	}
	if !strings.Contains(strings.Join(model.lines, "\n"), "you  hello") {
		t.Fatalf("expected prompt transcript, got %+v", model.lines)
	}

	msg := cmd().(tuiTurnDoneMsg)
	updated, _ = model.Update(msg)
	model = updated.(tuiModel)
	if model.running || model.status != "idle" {
		t.Fatalf("expected idle state after turn, got %+v", model)
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	model = updated.(tuiModel)
	if model.status != "saving session..." {
		t.Fatalf("expected save status, got %q", model.status)
	}
	saveMsg := cmd().(tuiSaveDoneMsg)
	updated, _ = model.Update(saveMsg)
	model = updated.(tuiModel)
	if model.status != "session saved" {
		t.Fatalf("expected session saved status, got %q", model.status)
	}
	if _, err := os.Stat(app.config.SessionFile); err != nil {
		t.Fatalf("expected session file to exist, got %v", err)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	model = updated.(tuiModel)
	if model.showSidebar {
		t.Fatal("expected ctrl+o to toggle sidebar off")
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	model = updated.(tuiModel)
	if len(model.lines) != 0 || model.status != "cleared transcript" {
		t.Fatalf("expected cleared transcript, got lines=%d status=%q", len(model.lines), model.status)
	}
}

func TestTUIModelConsumeEventsAndRenderViews(t *testing.T) {
	app, _, _ := newTestApp(t)
	ui := &terminalUI{app: app, ctx: context.Background()}
	model := newTUIModel(ui)
	model.width = 90
	model.height = 24
	model.ready = true
	model.layout()

	model.consumeEvent(engine.Event{Kind: engine.EventAssistantTextDelta, Text: "Hel"})
	model.consumeEvent(engine.Event{Kind: engine.EventAssistantTextDelta, Text: "lo"})
	model.consumeEvent(engine.Event{Kind: engine.EventAssistantTextDone})
	model.consumeEvent(engine.Event{Kind: engine.EventAssistantText, AgentName: "worker", Text: "done"})
	model.consumeEvent(engine.Event{Kind: engine.EventToolCall, ToolName: "bash", Text: `{"command":"pwd"}`})
	model.consumeEvent(engine.Event{Kind: engine.EventToolResult, ToolName: "bash", Text: `{"output":"/tmp"}`})
	model.consumeEvent(engine.Event{Kind: engine.EventAgentSpawned, AgentName: "worker", AgentID: "agent_1"})
	model.consumeEvent(engine.Event{Kind: engine.EventAgentCompleted, AgentName: "worker"})
	model.consumeEvent(engine.Event{Kind: engine.EventAgentCancelled, AgentName: "worker"})
	model.consumeEvent(engine.Event{Kind: engine.EventSessionCompacted, Text: "shrunk context"})
	model.consumeEvent(engine.Event{Kind: engine.EventHookError, Text: "hook exploded"})

	joined := strings.Join(model.lines, "\n")
	for _, needle := range []string{
		"assistant  Hello",
		"worker  done",
		"tool bash",
		"tool-result bash",
		"spawned agent worker (agent_1)",
		"agent worker completed",
		"agent worker cancelled",
		"session compacted: shrunk context",
		"hook  hook exploded",
	} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("expected transcript to contain %q, got %q", needle, joined)
		}
	}

	if sidebar := model.sidebarView(); !strings.Contains(sidebar, "Session") || !strings.Contains(sidebar, "Keys") {
		t.Fatalf("unexpected sidebar view: %q", sidebar)
	}
	if status := model.statusView(); !strings.Contains(status, "idle") || !strings.Contains(status, "model=test-model") {
		t.Fatalf("unexpected status view: %q", status)
	}
	if view := model.View(); !strings.Contains(view, "xxx-code") {
		t.Fatalf("unexpected full view: %q", view)
	}
}
