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
	"github.com/caowenhua/x-agent/xxx-code/internal/hooks"
	mcpruntime "github.com/caowenhua/x-agent/xxx-code/internal/mcp"
	"github.com/caowenhua/x-agent/xxx-code/internal/persist"
	"github.com/caowenhua/x-agent/xxx-code/internal/provider/anthropic"
	"github.com/caowenhua/x-agent/xxx-code/internal/tools"
)

type App struct {
	config     config.Config
	registry   *engine.Registry
	runner     *engine.Runner
	session    *engine.Session
	mcpManager *mcpruntime.Manager
	out        io.Writer
	errOut     io.Writer
	saveMu     sync.Mutex
	streamMu   sync.Mutex
	streaming  bool
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
		&tools.AgentFanoutTool{},
		&tools.AgentSendTool{},
		&tools.AgentCancelTool{},
		&tools.AgentWaitTool{},
		&tools.AgentListTool{},
	)
	app.registry = registry

	provider := anthropic.NewClient(cfg.APIKey, cfg.BaseURL, cfg.Version)
	app.runner = engine.NewRunner(provider, registry, engine.RunnerConfig{
		Model:               cfg.Model,
		SystemPrompt:        cfg.SystemPrompt,
		MaxTokens:           cfg.MaxTokens,
		MaxTurns:            cfg.MaxTurns,
		StreamResponses:     cfg.Stream,
		ContextBudget:       cfg.ContextBudget,
		CompactKeepMessages: cfg.CompactKeep,
		WorkingDir:          cfg.WorkingDir,
		ToolTimeout:         cfg.ToolTimeout,
		HookTimeout:         cfg.HookTimeout,
		MaxAgentDepth:       3,
		MaxParallelAgents:   cfg.MaxParallelAgents,
		PermissionPolicy: engine.PermissionPolicy{
			ReadRoots:   cfg.ReadRoots,
			WriteRoots:  cfg.WriteRoots,
			ReadOnly:    cfg.ReadOnly,
			BashEnabled: cfg.BashEnabled,
		},
		Hooks: hooks.NewScriptManager(hooks.Config{
			BeforeTool: cfg.HookBeforeTool,
			AfterTool:  cfg.HookAfterTool,
			AfterTurn:  cfg.HookAfterTurn,
			AgentEvent: cfg.HookAgentEvent,
		}),
		EventHandler: app.handleEvent,
	})

	return app
}

func (a *App) Run(ctx context.Context) (runErr error) {
	if err := a.initMCP(ctx); err != nil {
		return err
	}
	defer func() {
		if err := a.closeMCP(); err != nil {
			if runErr == nil {
				runErr = err
				return
			}
			fmt.Fprintf(a.errOut, "mcp shutdown error: %v\n", err)
		}
	}()

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
		fmt.Fprintln(a.out, ":mcp                      list MCP server status and loaded tools")
		fmt.Fprintln(a.out, ":wait <agent-id>          wait for an agent and print its snapshot")
		fmt.Fprintln(a.out, ":wait-all [agent-id ...]  wait for a batch of agents or every known agent")
		fmt.Fprintln(a.out, ":send <agent-id> <prompt> continue an existing agent")
		fmt.Fprintln(a.out, ":cancel <agent-id>        cancel a running agent and its descendants")
		fmt.Fprintln(a.out, ":history [n]              print the latest n main-session messages (default 10)")
		fmt.Fprintln(a.out, ":compact                  compact the main session immediately if it exceeds budget")
		fmt.Fprintln(a.out, ":policy                   print active permission policy")
		fmt.Fprintln(a.out, ":hooks                    print configured hook commands")
		fmt.Fprintln(a.out, ":save                     persist the current main session and agents")
		fmt.Fprintln(a.out, ":session                  print session file information")
		return false, nil
	case ":agents":
		snapshots := a.runner.ListAgents()
		data, _ := json.MarshalIndent(snapshots, "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":mcp":
		statuses := []mcpruntime.ServerStatus{}
		if a.mcpManager != nil {
			statuses = a.mcpManager.Statuses()
		}
		data, _ := json.MarshalIndent(statuses, "", "  ")
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
	case ":wait-all":
		waitCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		snapshots, err := a.runner.WaitAgents(waitCtx, fields[1:])
		if err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			return false, nil
		}
		data, _ := json.MarshalIndent(snapshots, "", "  ")
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
	case ":cancel":
		if len(fields) < 2 {
			fmt.Fprintln(a.errOut, "usage: :cancel <agent-id>")
			return false, nil
		}
		cancelCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		snapshot, err := a.runner.CancelAgent(cancelCtx, fields[1], true)
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
	case ":compact":
		report, changed := a.runner.CompactSession(a.session)
		if !changed {
			fmt.Fprintln(a.out, "session did not exceed the current compaction budget")
			return false, nil
		}
		fmt.Fprintf(
			a.out,
			"compacted session: ~%d -> ~%d tokens, %d -> %d messages\n",
			report.BeforeTokens,
			report.AfterTokens,
			report.BeforeMessages,
			report.AfterMessages,
		)
		return false, a.saveSession()
	case ":policy":
		data, _ := json.MarshalIndent(a.runner.PermissionPolicy(), "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":hooks":
		data, _ := json.MarshalIndent(map[string]any{
			"before_tool": a.config.HookBeforeTool,
			"after_tool":  a.config.HookAfterTool,
			"after_turn":  a.config.HookAfterTurn,
			"agent_event": a.config.HookAgentEvent,
		}, "", "  ")
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
		fmt.Fprintf(a.out, "approx tokens: %d\n", engine.EstimateTokens(a.session.Snapshot()))
		fmt.Fprintf(a.out, "context budget: %d\n", a.config.ContextBudget)
		fmt.Fprintf(a.out, "max parallel agents: %d\n", a.config.MaxParallelAgents)
		fmt.Fprintf(a.out, "agents: %d\n", len(a.runner.ListAgents()))
		if a.mcpManager != nil {
			fmt.Fprintf(a.out, "mcp config: %s\n", a.mcpManager.ConfigPath())
			fmt.Fprintf(a.out, "mcp servers: %d\n", a.mcpManager.ServerCount())
			fmt.Fprintf(a.out, "mcp tools: %d\n", a.mcpManager.ToolCount())
		} else {
			fmt.Fprintln(a.out, "mcp config: not loaded")
		}
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

func (a *App) initMCP(ctx context.Context) error {
	manager, err := mcpruntime.Start(ctx, a.registry, mcpruntime.Options{
		WorkingDir: a.config.WorkingDir,
		ConfigFile: a.config.MCPConfigFile,
	})
	if err != nil {
		return err
	}
	a.mcpManager = manager
	if a.mcpManager == nil {
		return nil
	}

	for _, status := range a.mcpManager.Statuses() {
		switch status.Status {
		case mcpruntime.ServerStatusConnected:
			if a.config.Verbose {
				fmt.Fprintf(a.errOut, "mcp server %s connected (%d tools)\n", status.Name, len(status.ToolNames))
			}
		case mcpruntime.ServerStatusFailed:
			fmt.Fprintf(a.errOut, "mcp server %s failed: %s\n", status.Name, status.Error)
		}
		for _, warning := range status.Warnings {
			fmt.Fprintf(a.errOut, "mcp server %s warning: %s\n", status.Name, warning)
		}
	}
	return nil
}

func (a *App) closeMCP() error {
	if a.mcpManager == nil {
		return nil
	}
	return a.mcpManager.Close()
}

func (a *App) handleEvent(event engine.Event) {
	a.printEvent(event)
	switch event.Kind {
	case engine.EventAgentSpawned, engine.EventAgentCompleted, engine.EventAgentCancelled:
		if err := a.saveSession(); err != nil {
			fmt.Fprintf(a.errOut, "autosave error: %v\n", err)
		}
	}
}

func (a *App) printEvent(event engine.Event) {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()

	switch event.Kind {
	case engine.EventAssistantTextDelta:
		if event.Text == "" {
			return
		}
		fmt.Fprint(a.out, event.Text)
		a.streaming = !strings.HasSuffix(event.Text, "\n")
	case engine.EventAssistantTextDone:
		if a.streaming {
			fmt.Fprintln(a.out)
			a.streaming = false
		}
	case engine.EventAssistantText:
		if a.streaming {
			fmt.Fprintln(a.out)
			a.streaming = false
		}
		if strings.TrimSpace(event.Text) == "" {
			return
		}
		if event.AgentName != "" {
			fmt.Fprintf(a.out, "[%s] %s\n", event.AgentName, event.Text)
			return
		}
		fmt.Fprintln(a.out, event.Text)
	case engine.EventToolCall:
		if a.config.Verbose {
			agentPrefix := ""
			if event.AgentName != "" {
				agentPrefix = "[" + event.AgentName + "] "
			}
			fmt.Fprintf(a.errOut, "%stool %s %s\n", agentPrefix, event.ToolName, event.Text)
		}
	case engine.EventToolResult:
		if a.config.Verbose {
			agentPrefix := ""
			if event.AgentName != "" {
				agentPrefix = "[" + event.AgentName + "] "
			}
			fmt.Fprintf(a.errOut, "%stool-result %s %s\n", agentPrefix, event.ToolName, event.Text)
		}
	case engine.EventAgentSpawned:
		fmt.Fprintf(a.errOut, "spawned agent %s (%s)\n", event.AgentName, event.AgentID)
	case engine.EventAgentCompleted:
		fmt.Fprintf(a.errOut, "agent %s completed\n", event.AgentName)
	case engine.EventAgentCancelled:
		fmt.Fprintf(a.errOut, "agent %s cancelled\n", event.AgentName)
	case engine.EventSessionCompacted:
		if a.config.Verbose {
			agentPrefix := ""
			if event.AgentName != "" {
				agentPrefix = "[" + event.AgentName + "] "
			}
			fmt.Fprintf(a.errOut, "%ssession %s\n", agentPrefix, event.Text)
		}
	case engine.EventHookError:
		fmt.Fprintf(a.errOut, "hook error: %s\n", event.Text)
	}
}
