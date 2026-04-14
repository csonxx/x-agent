package remote

import (
	"context"
	"fmt"
	"strings"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type terminalUI struct {
	app     *App
	ctx     context.Context
	program *tea.Program
}

type tuiEventMsg TurnStreamEvent

type tuiTurnDoneMsg struct {
	result   TurnResult
	session  SessionSummary
	streamed bool
	err      error
}

type tuiSaveDoneMsg struct {
	session SessionSummary
	err     error
}

type tuiModel struct {
	ui *terminalUI

	viewport viewport.Model
	input    textinput.Model

	width       int
	height      int
	ready       bool
	running     bool
	showSidebar bool
	status      string
	lines       []string

	streamText  string
	streamLabel string
}

var (
	tuiHeaderStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("24")).
			Padding(0, 1)
	tuiStatusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			Background(lipgloss.Color("238")).
			Padding(0, 1)
	tuiSidebarStyle = lipgloss.NewStyle().
			BorderLeft(true).
			BorderForeground(lipgloss.Color("240")).
			PaddingLeft(1)
	tuiUserStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("86"))
	tuiAssistantStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("229"))
	tuiEventStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	tuiErrorStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)

func (a *App) runTUI(ctx context.Context) error {
	ui := &terminalUI{app: a, ctx: ctx}
	model := newTUIModel(ui)
	program := tea.NewProgram(model, tea.WithAltScreen())
	ui.program = program
	_, err := program.Run()
	return err
}

func (u *terminalUI) handleEvent(event TurnStreamEvent) {
	if u == nil || u.program == nil {
		return
	}
	u.program.Send(tuiEventMsg(event))
}

func newTUIModel(ui *terminalUI) tuiModel {
	input := textinput.New()
	input.Placeholder = "Ask xxx-code remote to do something"
	input.Focus()
	input.Prompt = "> "

	model := tuiModel{
		ui:          ui,
		input:       input,
		showSidebar: true,
		status:      "idle",
	}
	model.appendEventLine("xxx-code remote TUI ready. Enter to send, Ctrl+S to save, Ctrl+L to clear, Ctrl+O to toggle sidebar, Ctrl+C to quit.")
	model.appendEventLine("connected to " + ui.app.client.BaseURL() + " session " + ui.app.sessionID)
	return model
}

func (m tuiModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.layout()
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "ctrl+s":
			m.status = "saving remote session..."
			return m, saveSessionCmd(m.ui)
		case "ctrl+l":
			m.lines = nil
			m.streamText = ""
			m.streamLabel = ""
			m.syncViewport()
			m.status = "cleared transcript"
			return m, nil
		case "ctrl+o":
			m.showSidebar = !m.showSidebar
			m.layout()
			return m, nil
		case "enter":
			prompt := strings.TrimSpace(m.input.Value())
			if prompt == "" || m.running {
				return m, nil
			}
			m.flushStream()
			m.appendLine(tuiUserStyle.Render("you"), prompt)
			m.input.SetValue("")
			m.running = true
			m.status = "running..."
			return m, runTurnCmd(m.ui, prompt)
		}
	}

	switch msg := msg.(type) {
	case tuiEventMsg:
		m.consumeEvent(TurnStreamEvent(msg))
		return m, nil
	case tuiTurnDoneMsg:
		m.running = false
		m.ui.app.sessionID = msg.session.ID
		m.flushStream()
		if msg.err != nil {
			m.appendLine(tuiErrorStyle.Render("error"), msg.err.Error())
			m.status = "turn failed"
			return m, nil
		}
		if !msg.streamed && strings.TrimSpace(msg.result.FinalText) != "" {
			m.appendLine(tuiAssistantStyle.Render("assistant"), msg.result.FinalText)
		}
		m.status = "idle"
		return m, nil
	case tuiSaveDoneMsg:
		if msg.err != nil {
			m.appendLine(tuiErrorStyle.Render("error"), msg.err.Error())
			m.status = "save failed"
			return m, nil
		}
		m.ui.app.sessionID = msg.session.ID
		m.status = "session saved"
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m tuiModel) View() string {
	if !m.ready {
		return "loading xxx-code remote TUI..."
	}

	header := tuiHeaderStyle.Width(m.width).Render(fmt.Sprintf("xxx-code remote  %s  %s", m.ui.app.sessionID, m.ui.app.client.BaseURL()))
	bodyHeight := maxInt(5, m.height-4)
	var body string
	if m.showSidebar {
		sidebarWidth := maxInt(28, minInt(38, m.width/3))
		mainWidth := maxInt(20, m.width-sidebarWidth-1)
		m.viewport.Width = mainWidth
		body = lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.NewStyle().Width(mainWidth).Height(bodyHeight).Render(m.viewport.View()),
			tuiSidebarStyle.Width(sidebarWidth).Height(bodyHeight).Render(m.sidebarView()),
		)
	} else {
		body = lipgloss.NewStyle().Width(m.width).Height(bodyHeight).Render(m.viewport.View())
	}

	input := lipgloss.NewStyle().Padding(0, 1).Width(m.width).Render(m.input.View())
	status := tuiStatusStyle.Width(m.width).Render(m.statusView())

	return lipgloss.JoinVertical(lipgloss.Left, header, body, input, status)
}

func (m *tuiModel) consumeEvent(event TurnStreamEvent) {
	switch event.Type {
	case string(engine.EventAssistantTextDelta):
		label := "assistant"
		if event.AgentName != "" {
			label = event.AgentName
		}
		if m.streamLabel != "" && m.streamLabel != label {
			m.flushStream()
		}
		m.streamLabel = label
		m.streamText += event.Text
		m.syncViewport()
	case string(engine.EventAssistantTextDone):
		m.flushStream()
	case string(engine.EventAssistantText):
		m.flushStream()
		if strings.TrimSpace(event.Text) == "" {
			return
		}
		label := "assistant"
		if event.AgentName != "" {
			label = event.AgentName
		}
		m.appendLine(tuiAssistantStyle.Render(label), event.Text)
	case string(engine.EventToolCall):
		m.flushStream()
		m.appendEventLine(fmt.Sprintf("tool %s %s", event.ToolName, strings.TrimSpace(event.Text)))
	case string(engine.EventToolResult):
		m.flushStream()
		m.appendEventLine(fmt.Sprintf("tool-result %s %s", event.ToolName, strings.TrimSpace(event.Text)))
	case string(engine.EventAgentSpawned):
		m.flushStream()
		m.appendEventLine(fmt.Sprintf("spawned agent %s (%s)", event.AgentName, event.AgentID))
	case string(engine.EventAgentCompleted):
		m.flushStream()
		m.appendEventLine(fmt.Sprintf("agent %s completed", event.AgentName))
	case string(engine.EventAgentCancelled):
		m.flushStream()
		m.appendEventLine(fmt.Sprintf("agent %s cancelled", event.AgentName))
	case string(engine.EventHookError):
		m.flushStream()
		m.appendLine(tuiErrorStyle.Render("hook"), event.Text)
	}
}

func (m *tuiModel) appendLine(label, text string) {
	line := label + "  " + strings.TrimSpace(text)
	m.lines = append(m.lines, line)
	m.syncViewport()
}

func (m *tuiModel) appendEventLine(text string) {
	m.lines = append(m.lines, tuiEventStyle.Render(text))
	m.syncViewport()
}

func (m *tuiModel) flushStream() {
	if strings.TrimSpace(m.streamText) == "" {
		m.streamText = ""
		m.streamLabel = ""
		return
	}
	label := m.streamLabel
	if label == "" {
		label = "assistant"
	}
	m.lines = append(m.lines, tuiAssistantStyle.Render(label)+"  "+strings.TrimSpace(m.streamText))
	m.streamText = ""
	m.streamLabel = ""
	m.syncViewport()
}

func (m *tuiModel) syncViewport() {
	content := append([]string(nil), m.lines...)
	if strings.TrimSpace(m.streamText) != "" {
		label := m.streamLabel
		if label == "" {
			label = "assistant"
		}
		content = append(content, tuiAssistantStyle.Render(label)+"  "+strings.TrimSpace(m.streamText))
	}
	m.viewport.SetContent(strings.Join(content, "\n"))
	m.viewport.GotoBottom()
}

func (m *tuiModel) layout() {
	inputHeight := 1
	headerHeight := 1
	statusHeight := 1
	bodyHeight := maxInt(5, m.height-headerHeight-inputHeight-statusHeight)

	sidebarWidth := 0
	if m.showSidebar {
		sidebarWidth = maxInt(28, minInt(38, m.width/3))
	}
	mainWidth := m.width
	if m.showSidebar {
		mainWidth = maxInt(20, m.width-sidebarWidth-1)
	}

	m.viewport = viewport.New(mainWidth, bodyHeight)
	m.input.Width = maxInt(10, m.width-4)
	m.syncViewport()
}

func (m tuiModel) sidebarView() string {
	lines := []string{
		lipgloss.NewStyle().Bold(true).Render("Session"),
		fmt.Sprintf("id         %s", m.ui.app.sessionID),
		fmt.Sprintf("remote     %s", m.ui.app.client.BaseURL()),
		fmt.Sprintf("stream     %t", m.ui.app.config.Stream),
		fmt.Sprintf("verbose    %t", m.ui.app.config.Verbose),
		"",
		lipgloss.NewStyle().Bold(true).Render("Keys"),
		"Enter    send",
		"Ctrl+S   save",
		"Ctrl+L   clear",
		"Ctrl+O   sidebar",
		"Ctrl+C   quit",
	}
	if strings.TrimSpace(m.ui.app.config.RemoteToken) != "" || strings.TrimSpace(m.ui.app.config.RemoteTokenFile) != "" {
		lines = append(lines[:4], append([]string{"auth       bearer"}, lines[4:]...)...)
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) statusView() string {
	mode := "idle"
	if m.running {
		mode = "busy"
	}
	return fmt.Sprintf("%s  %s  remote=%s", mode, m.status, m.ui.app.client.BaseURL())
}

func runTurnCmd(ui *terminalUI, prompt string) tea.Cmd {
	return func() tea.Msg {
		if ui.app.config.Stream {
			streamed := false
			result, session, err := ui.app.client.StreamTurn(ui.ctx, ui.app.sessionID, prompt, 0, func(event TurnStreamEvent) {
				if event.Type == string(engine.EventAssistantTextDelta) {
					streamed = true
				}
				ui.handleEvent(event)
			})
			return tuiTurnDoneMsg{result: result, session: session, streamed: streamed, err: err}
		}
		result, session, err := ui.app.client.RunTurn(ui.ctx, ui.app.sessionID, prompt, 0)
		return tuiTurnDoneMsg{result: result, session: session, streamed: false, err: err}
	}
}

func saveSessionCmd(ui *terminalUI) tea.Cmd {
	return func() tea.Msg {
		session, err := ui.app.client.SaveSession(ui.ctx, ui.app.sessionID)
		return tuiSaveDoneMsg{session: session, err: err}
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
