package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/config"
	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	"github.com/caowenhua/x-agent/xxx-code/internal/persist"
	"github.com/caowenhua/x-agent/xxx-code/internal/provider/anthropic"
	"github.com/caowenhua/x-agent/xxx-code/internal/tools"
)

type App struct {
	config  config.Config
	runner  *engine.Runner
	session *engine.Session
	out     io.Writer
	errOut  io.Writer
	saveMu  sync.Mutex
}

func New(cfg config.Config, out, errOut io.Writer) *App {
	app := &App{
		config:  cfg,
		session: engine.NewSession(),
		out:     out,
		errOut:  errOut,
	}

	registry := engine.NewRegistry(
		&tools.BashTool{},
		&tools.ReadFileTool{},
		&tools.WriteFileTool{},
		&tools.EditFileTool{},
		&tools.GlobTool{},
		&tools.GrepTool{},
		&tools.AgentSpawnTool{},
		&tools.AgentSendTool{},
		&tools.AgentWaitTool{},
		&tools.AgentListTool{},
	)

	provider := anthropic.NewClient(cfg.APIKey, cfg.BaseURL, cfg.Version)
	app.runner = engine.NewRunner(provider, registry, engine.RunnerConfig{
		Model:         cfg.Model,
		SystemPrompt:  cfg.SystemPrompt,
		MaxTokens:     cfg.MaxTokens,
		MaxTurns:      cfg.MaxTurns,
		WorkingDir:    cfg.WorkingDir,
		ToolTimeout:   cfg.ToolTimeout,
		MaxAgentDepth: 3,
		EventHandler:  app.handleEvent,
	})

	return app
}

func (a *App) Run(ctx context.Context) error {
	if a.config.Resume {
		if err := a.resume(); err != nil {
			return err
		}
	}

	if a.config.Print {
		if _, err := a.runner.RunTurn(ctx, a.session, a.config.Prompt); err != nil {
			return err
		}
		return a.saveSession()
	}

	return a.runREPL(ctx)
}

func (a *App) runREPL(ctx context.Context) error {
	fmt.Fprintf(a.out, "xxx-code (%s)\n", a.config.Model)
	fmt.Fprintln(a.out, "Type :help for commands, :quit to exit.")
	if a.config.Resume {
		fmt.Fprintf(a.errOut, "resumed session from %s\n", a.config.SessionFile)
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for {
		fmt.Fprint(a.out, ">>> ")
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return err
			}
			return a.saveSession()
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, ":") {
			if done, err := a.handleCommand(ctx, line); err != nil {
				return err
			} else if done {
				return nil
			}
			continue
		}

		if _, err := a.runner.RunTurn(ctx, a.session, line); err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			continue
		}
		if err := a.saveSession(); err != nil {
			fmt.Fprintf(a.errOut, "autosave error: %v\n", err)
		}
	}
}

func (a *App) handleCommand(ctx context.Context, line string) (bool, error) {
	fields := strings.Fields(line)
	switch fields[0] {
	case ":quit", ":exit":
		return true, a.saveSession()
	case ":help":
		fmt.Fprintln(a.out, ":help                     show this help")
		fmt.Fprintln(a.out, ":quit                     save and exit the REPL")
		fmt.Fprintln(a.out, ":agents                   list spawned agents")
		fmt.Fprintln(a.out, ":wait <agent-id>          wait for an agent and print its snapshot")
		fmt.Fprintln(a.out, ":send <agent-id> <prompt> continue an existing agent")
		fmt.Fprintln(a.out, ":history [n]              print the latest n main-session messages (default 10)")
		fmt.Fprintln(a.out, ":save                     persist the current main session and agents")
		fmt.Fprintln(a.out, ":session                  print session file information")
		return false, nil
	case ":agents":
		snapshots := a.runner.ListAgents()
		data, _ := json.MarshalIndent(snapshots, "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":wait":
		if len(fields) < 2 {
			fmt.Fprintln(a.errOut, "usage: :wait <agent-id>")
			return false, nil
		}
		waitCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		snapshot, err := a.runner.WaitAgent(waitCtx, fields[1])
		if err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			return false, nil
		}
		data, _ := json.MarshalIndent(snapshot, "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":send":
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 3 || strings.TrimSpace(parts[1]) == "" || strings.TrimSpace(parts[2]) == "" {
			fmt.Fprintln(a.errOut, "usage: :send <agent-id> <prompt>")
			return false, nil
		}
		sendCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		snapshot, err := a.runner.SendAgent(sendCtx, strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2]), false)
		if err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			return false, nil
		}
		data, _ := json.MarshalIndent(snapshot, "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":history":
		limit := 10
		if len(fields) > 1 {
			n, err := strconv.Atoi(fields[1])
			if err != nil || n <= 0 {
				fmt.Fprintln(a.errOut, "usage: :history [positive-number]")
				return false, nil
			}
			limit = n
		}
		messages := a.session.Snapshot()
		if len(messages) > limit {
			messages = messages[len(messages)-limit:]
		}
		data, _ := json.MarshalIndent(messages, "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":save":
		if err := a.saveSession(); err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			return false, nil
		}
		fmt.Fprintf(a.out, "saved session to %s\n", a.config.SessionFile)
		return false, nil
	case ":session":
		fmt.Fprintf(a.out, "session file: %s\n", a.config.SessionFile)
		fmt.Fprintf(a.out, "main messages: %d\n", len(a.session.Snapshot()))
		fmt.Fprintf(a.out, "agents: %d\n", len(a.runner.ListAgents()))
		return false, nil
	default:
		fmt.Fprintf(a.errOut, "unknown command: %s\n", fields[0])
		return false, nil
	}
}

func (a *App) resume() error {
	state, err := persist.Load(a.config.SessionFile)
	if err != nil {
		return fmt.Errorf("resume session: %w", err)
	}
	a.session.Replace(state.Main)
	if err := a.runner.ImportAgents(state.Agents); err != nil {
		return fmt.Errorf("resume agents: %w", err)
	}
	return nil
}

func (a *App) saveSession() error {
	a.saveMu.Lock()
	defer a.saveMu.Unlock()
	return persist.Save(a.config.SessionFile, a.session, a.runner)
}

func (a *App) handleEvent(event engine.Event) {
	printEvent(a.config.Verbose, a.out, a.errOut, event)
	switch event.Kind {
	case engine.EventAgentSpawned, engine.EventAgentCompleted:
		if err := a.saveSession(); err != nil {
			fmt.Fprintf(a.errOut, "autosave error: %v\n", err)
		}
	}
}

func printEvent(verbose bool, out, errOut io.Writer, event engine.Event) {
	switch event.Kind {
	case engine.EventAssistantText:
		if strings.TrimSpace(event.Text) == "" {
			return
		}
		if event.AgentName != "" {
			fmt.Fprintf(out, "[%s] %s\n", event.AgentName, event.Text)
			return
		}
		fmt.Fprintln(out, event.Text)
	case engine.EventToolCall:
		if verbose {
			agentPrefix := ""
			if event.AgentName != "" {
				agentPrefix = "[" + event.AgentName + "] "
			}
			fmt.Fprintf(errOut, "%stool %s %s\n", agentPrefix, event.ToolName, event.Text)
		}
	case engine.EventToolResult:
		if verbose {
			agentPrefix := ""
			if event.AgentName != "" {
				agentPrefix = "[" + event.AgentName + "] "
			}
			fmt.Fprintf(errOut, "%stool-result %s %s\n", agentPrefix, event.ToolName, event.Text)
		}
	case engine.EventAgentSpawned:
		fmt.Fprintf(errOut, "spawned agent %s (%s)\n", event.AgentName, event.AgentID)
	case engine.EventAgentCompleted:
		fmt.Fprintf(errOut, "agent %s completed\n", event.AgentName)
	}
}
