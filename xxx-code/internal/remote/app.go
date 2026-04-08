package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/config"
	"github.com/caowenhua/x-agent/xxx-code/internal/diag"
	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
)

type App struct {
	config    config.Config
	client    *Client
	out       io.Writer
	errOut    io.Writer
	sessionID string
	streaming bool
	logger    *diag.Logger
}

func New(cfg config.Config, out, errOut io.Writer) *App {
	if out == nil {
		out = io.Discard
	}
	if errOut == nil {
		errOut = io.Discard
	}
	return &App{
		config: cfg,
		client: NewClient(cfg.RemoteURL, cfg.RemoteToken, nil),
		out:    out,
		errOut: errOut,
		logger: diag.New(errOut, cfg.LogLevel),
	}
}

func (a *App) Run(ctx context.Context) error {
	a.logger.Debugf("starting remote app url=%s session=%s print=%t tui=%t config=%s", a.config.RemoteURL, a.config.RemoteSession, a.config.Print, a.config.TUI, a.config.ConfigFile)
	if strings.TrimSpace(a.config.RemoteURL) == "" {
		return fmt.Errorf("--remote-url is required")
	}
	if a.config.RemoteList {
		return a.printJSON(ctx, func(ctx context.Context) (any, error) {
			return a.client.ListSessions(ctx)
		})
	}

	session, err := a.openSession(ctx)
	if err != nil {
		return err
	}
	a.sessionID = session.ID
	a.logger.Debugf("connected to remote session id=%s", a.sessionID)

	if a.config.Print {
		if strings.TrimSpace(a.config.Prompt) == "" {
			return fmt.Errorf("remote print mode requires a prompt")
		}
		_, updated, err := a.runTurn(ctx, a.config.Prompt)
		if err != nil {
			return err
		}
		a.sessionID = updated.ID
		return nil
	}
	if a.config.TUI {
		return a.runTUI(ctx)
	}

	return a.runREPL(ctx)
}

func (a *App) openSession(ctx context.Context) (SessionSummary, error) {
	sessionID := strings.TrimSpace(a.config.RemoteSession)
	if sessionID == "" {
		return a.client.CreateSession(ctx, "", false)
	}
	if a.config.Resume {
		return a.client.GetSession(ctx, sessionID)
	}
	return a.client.EnsureSession(ctx, sessionID)
}

func (a *App) runREPL(ctx context.Context) error {
	fmt.Fprintf(a.out, "xxx-code remote (%s)\n", a.client.BaseURL())
	fmt.Fprintf(a.out, "Connected to session %s\n", a.sessionID)
	fmt.Fprintln(a.out, "Type :help for commands, :quit to exit.")

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for {
		fmt.Fprint(a.out, ">>> ")
		if !scanner.Scan() {
			return scanner.Err()
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ":") {
			if done, err := a.handleCommand(ctx, line); err != nil {
				fmt.Fprintf(a.errOut, "error: %v\n", err)
				continue
			} else if done {
				return nil
			}
			continue
		}

		_, updated, err := a.runTurn(ctx, line)
		if err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			continue
		}
		a.sessionID = updated.ID
	}
}

func (a *App) handleCommand(ctx context.Context, line string) (bool, error) {
	fields := strings.Fields(line)
	switch fields[0] {
	case ":quit", ":exit":
		return true, nil
	case ":help":
		fmt.Fprintln(a.out, ":help                     show this help")
		fmt.Fprintln(a.out, ":quit                     exit the remote REPL")
		fmt.Fprintln(a.out, ":session                  print remote session summary")
		fmt.Fprintln(a.out, ":history [n]              print the latest n remote messages (default 10)")
		fmt.Fprintln(a.out, ":mcp                      print remote MCP status")
		fmt.Fprintln(a.out, ":mcp-resources [server]   list remote MCP resources")
		fmt.Fprintln(a.out, ":mcp-resource-templates [server] list remote MCP resource templates")
		fmt.Fprintln(a.out, ":mcp-prompts [server]     list remote MCP prompts")
		fmt.Fprintln(a.out, ":mcp-read <server> <uri>  read one remote MCP resource")
		fmt.Fprintln(a.out, ":mcp-prompt <server> <name> [k=v ...] fetch one remote MCP prompt")
		fmt.Fprintln(a.out, ":policy                   print remote permission policy")
		fmt.Fprintln(a.out, ":hooks                    print remote hook configuration")
		fmt.Fprintln(a.out, ":agents                   list remote agents")
		fmt.Fprintln(a.out, ":wait <agent-id>          wait for a remote agent")
		fmt.Fprintln(a.out, ":send <agent-id> <prompt> continue an existing remote agent")
		fmt.Fprintln(a.out, ":cancel <agent-id>        cancel a remote agent and descendants")
		fmt.Fprintln(a.out, ":workflows                list remote workflows")
		fmt.Fprintln(a.out, ":workflow <id>            print one remote workflow snapshot")
		fmt.Fprintln(a.out, ":workflow-tasks <id> [status|name=<task>] list remote workflow tasks")
		fmt.Fprintln(a.out, ":workflow-resume <id> [failed|task...]    resume remote workflow from failed or selected tasks")
		fmt.Fprintln(a.out, ":save                     persist the current remote session")
		return false, nil
	case ":session":
		return false, a.printJSON(ctx, func(ctx context.Context) (any, error) {
			return a.client.GetSession(ctx, a.sessionID)
		})
	case ":history":
		limit := 10
		if len(fields) > 1 {
			value, err := strconv.Atoi(fields[1])
			if err != nil || value < 0 {
				fmt.Fprintf(a.errOut, "error: invalid history limit: %s\n", fields[1])
				return false, nil
			}
			limit = value
		}
		return false, a.printJSON(ctx, func(ctx context.Context) (any, error) {
			return a.client.ListMessages(ctx, a.sessionID, limit)
		})
	case ":mcp":
		return false, a.printJSON(ctx, func(ctx context.Context) (any, error) {
			return a.client.GetMCP(ctx, a.sessionID)
		})
	case ":mcp-resources":
		server := ""
		if len(fields) > 1 {
			server = fields[1]
		}
		return false, a.printJSON(ctx, func(ctx context.Context) (any, error) {
			return a.client.ListMCPResources(ctx, a.sessionID, server)
		})
	case ":mcp-resource-templates":
		server := ""
		if len(fields) > 1 {
			server = fields[1]
		}
		return false, a.printJSON(ctx, func(ctx context.Context) (any, error) {
			return a.client.ListMCPResourceTemplates(ctx, a.sessionID, server)
		})
	case ":mcp-prompts":
		server := ""
		if len(fields) > 1 {
			server = fields[1]
		}
		return false, a.printJSON(ctx, func(ctx context.Context) (any, error) {
			return a.client.ListMCPPrompts(ctx, a.sessionID, server)
		})
	case ":mcp-read":
		if len(fields) < 3 {
			fmt.Fprintln(a.errOut, "usage: :mcp-read <server> <uri>")
			return false, nil
		}
		return false, a.printJSON(ctx, func(ctx context.Context) (any, error) {
			return a.client.ReadMCPResource(ctx, a.sessionID, fields[1], fields[2])
		})
	case ":mcp-prompt":
		if len(fields) < 3 {
			fmt.Fprintln(a.errOut, "usage: :mcp-prompt <server> <name> [key=value ...]")
			return false, nil
		}
		arguments, err := parsePromptArguments(fields[3:])
		if err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			return false, nil
		}
		return false, a.printJSON(ctx, func(ctx context.Context) (any, error) {
			return a.client.GetMCPPrompt(ctx, a.sessionID, fields[1], fields[2], arguments)
		})
	case ":policy":
		return false, a.printJSON(ctx, func(ctx context.Context) (any, error) {
			return a.client.GetPolicy(ctx, a.sessionID)
		})
	case ":hooks":
		return false, a.printJSON(ctx, func(ctx context.Context) (any, error) {
			return a.client.GetHooks(ctx, a.sessionID)
		})
	case ":agents":
		return false, a.printJSON(ctx, func(ctx context.Context) (any, error) {
			return a.client.ListAgents(ctx, a.sessionID)
		})
	case ":wait":
		if len(fields) < 2 {
			fmt.Fprintln(a.errOut, "usage: :wait <agent-id>")
			return false, nil
		}
		return false, a.printJSON(ctx, func(ctx context.Context) (any, error) {
			waitCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
			defer cancel()
			return a.client.WaitAgent(waitCtx, a.sessionID, fields[1], 1800)
		})
	case ":send":
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 3 {
			fmt.Fprintln(a.errOut, "usage: :send <agent-id> <prompt>")
			return false, nil
		}
		return false, a.printJSON(ctx, func(ctx context.Context) (any, error) {
			return a.client.SendAgent(ctx, a.sessionID, strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2]), false)
		})
	case ":cancel":
		if len(fields) < 2 {
			fmt.Fprintln(a.errOut, "usage: :cancel <agent-id>")
			return false, nil
		}
		return false, a.printJSON(ctx, func(ctx context.Context) (any, error) {
			return a.client.CancelAgent(ctx, a.sessionID, fields[1], true)
		})
	case ":workflows":
		return false, a.printJSON(ctx, func(ctx context.Context) (any, error) {
			return a.client.ListWorkflows(ctx, a.sessionID)
		})
	case ":workflow":
		if len(fields) < 2 {
			fmt.Fprintln(a.errOut, "usage: :workflow <workflow-id>")
			return false, nil
		}
		return false, a.printJSON(ctx, func(ctx context.Context) (any, error) {
			return a.client.GetWorkflow(ctx, a.sessionID, fields[1])
		})
	case ":workflow-tasks":
		if len(fields) < 2 {
			fmt.Fprintln(a.errOut, "usage: :workflow-tasks <workflow-id> [status|name=<task>]")
			return false, nil
		}
		statusFilter := ""
		nameFilter := ""
		if len(fields) > 2 {
			value := strings.TrimSpace(fields[2])
			if strings.HasPrefix(strings.ToLower(value), "name=") {
				nameFilter = strings.TrimSpace(strings.TrimPrefix(value, "name="))
			} else {
				statusFilter = value
			}
		}
		return false, a.printJSON(ctx, func(ctx context.Context) (any, error) {
			return a.client.ListWorkflowTasks(ctx, a.sessionID, fields[1], statusFilter, nameFilter)
		})
	case ":workflow-resume":
		if len(fields) < 2 {
			fmt.Fprintln(a.errOut, "usage: :workflow-resume <workflow-id> [failed|task...]")
			return false, nil
		}
		options := WorkflowResumeOptions{}
		if len(fields) > 2 {
			if strings.EqualFold(fields[2], "failed") {
				options.OnlyFailed = true
			} else {
				options.TaskNames = append([]string(nil), fields[2:]...)
			}
		}
		return false, a.printJSON(ctx, func(ctx context.Context) (any, error) {
			resumeCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
			defer cancel()
			options.TimeoutSeconds = 1800
			return a.client.ResumeWorkflow(resumeCtx, a.sessionID, fields[1], options)
		})
	case ":save":
		return false, a.printJSON(ctx, func(ctx context.Context) (any, error) {
			return a.client.SaveSession(ctx, a.sessionID)
		})
	default:
		fmt.Fprintf(a.errOut, "error: unknown command %s\n", fields[0])
		return false, nil
	}
}

func (a *App) printJSON(ctx context.Context, fn func(context.Context) (any, error)) error {
	value, err := fn(ctx)
	if err != nil {
		return err
	}
	data, marshalErr := json.MarshalIndent(value, "", "  ")
	if marshalErr != nil {
		return marshalErr
	}
	fmt.Fprintln(a.out, string(data))
	return nil
}

func (a *App) runTurn(ctx context.Context, prompt string) (TurnResult, SessionSummary, error) {
	if !a.config.Stream {
		result, updated, err := a.client.RunTurn(ctx, a.sessionID, prompt, 0)
		if err != nil {
			return TurnResult{}, SessionSummary{}, err
		}
		if strings.TrimSpace(result.FinalText) != "" {
			fmt.Fprintln(a.out, result.FinalText)
		}
		return result, updated, nil
	}

	streamedText := false
	result, updated, err := a.client.StreamTurn(ctx, a.sessionID, prompt, 0, func(event TurnStreamEvent) {
		switch event.Type {
		case string(engine.EventAssistantTextDelta):
			if event.Text == "" {
				return
			}
			streamedText = true
			a.streaming = true
			fmt.Fprint(a.out, event.Text)
		case string(engine.EventAssistantTextDone):
			if a.streaming {
				fmt.Fprintln(a.out)
				a.streaming = false
			}
		default:
			if a.config.Verbose {
				a.printVerboseEvent(event)
			}
		}
	})
	if a.streaming {
		fmt.Fprintln(a.out)
		a.streaming = false
	}
	if err != nil {
		return TurnResult{}, SessionSummary{}, err
	}
	if !streamedText && strings.TrimSpace(result.FinalText) != "" {
		fmt.Fprintln(a.out, result.FinalText)
	}
	return result, updated, nil
}

func (a *App) printVerboseEvent(event TurnStreamEvent) {
	switch event.Type {
	case string(engine.EventToolCall):
		fmt.Fprintf(a.errOut, "[tool call] %s %s\n", event.ToolName, event.Text)
	case string(engine.EventToolResult):
		fmt.Fprintf(a.errOut, "[tool result] %s %s\n", event.ToolName, event.Text)
	case string(engine.EventAgentSpawned), string(engine.EventAgentCompleted), string(engine.EventAgentCancelled):
		name := event.AgentName
		if strings.TrimSpace(name) == "" {
			name = event.AgentID
		}
		fmt.Fprintf(a.errOut, "[agent] %s %s\n", event.Type, name)
	case string(engine.EventHookError):
		fmt.Fprintf(a.errOut, "[hook error] %s\n", event.Text)
	}
}

func parsePromptArguments(parts []string) (map[string]string, error) {
	if len(parts) == 0 {
		return nil, nil
	}
	arguments := make(map[string]string, len(parts))
	for _, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid prompt argument %q, expected key=value", part)
		}
		arguments[key] = value
	}
	return arguments, nil
}
