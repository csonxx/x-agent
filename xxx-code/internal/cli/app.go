package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/config"
	"github.com/caowenhua/x-agent/xxx-code/internal/diag"
	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	"github.com/caowenhua/x-agent/xxx-code/internal/hooks"
	mcpruntime "github.com/caowenhua/x-agent/xxx-code/internal/mcp"
	"github.com/caowenhua/x-agent/xxx-code/internal/persist"
	pluginruntime "github.com/caowenhua/x-agent/xxx-code/internal/plugins"
	"github.com/caowenhua/x-agent/xxx-code/internal/provider"
	"github.com/caowenhua/x-agent/xxx-code/internal/tools"
)

type App struct {
	config          config.Config
	registry        *engine.Registry
	runner          *engine.Runner
	session         *engine.Session
	workflowManager *tools.WorkflowManager
	mcpManager      *mcpruntime.Manager
	pluginManager   *pluginruntime.Manager
	out             io.Writer
	errOut          io.Writer
	saveMu          sync.Mutex
	streamMu        sync.Mutex
	streaming       bool
	tui             *terminalUI
	logger          *diag.Logger
}

func New(cfg config.Config, out, errOut io.Writer) *App {
	app := &App{
		config:          cfg,
		session:         engine.NewSession(),
		workflowManager: tools.NewWorkflowManager(),
		out:             out,
		errOut:          errOut,
		logger:          diag.New(errOut, cfg.LogLevel),
	}
	app.workflowManager.SetArtifactRoot(filepath.Join(cfg.WorkingDir, ".xxx-code", "artifacts", "workflows"))

	registry := engine.NewRegistry(
		&tools.BashTool{},
		&tools.ReadFileTool{},
		&tools.WriteFileTool{},
		&tools.EditFileTool{},
		&tools.GlobTool{},
		&tools.GrepTool{},
		&tools.AgentSpawnTool{},
		&tools.AgentFanoutTool{Manager: app.workflowManager},
		&tools.AgentSendTool{},
		&tools.AgentCancelTool{},
		&tools.AgentWaitTool{},
		&tools.AgentListTool{},
		&tools.WorkflowListTool{Manager: app.workflowManager},
		&tools.WorkflowGetTool{Manager: app.workflowManager},
		&tools.WorkflowTasksTool{Manager: app.workflowManager},
		&tools.WorkflowResumeTool{Manager: app.workflowManager},
	)
	app.registry = registry

	modelProvider := provider.New(cfg)
	app.runner = engine.NewRunner(modelProvider, registry, engine.RunnerConfig{
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
			ReadRoots:           cfg.ReadRoots,
			WriteRoots:          cfg.WriteRoots,
			AllowedTools:        cfg.AllowedTools,
			BlockedTools:        cfg.BlockedTools,
			BashAllowedPrefixes: cfg.BashAllowPrefixes,
			BashBlockedPrefixes: cfg.BashDenyPrefixes,
			ReadOnly:            cfg.ReadOnly,
			BashEnabled:         cfg.BashEnabled,
		},
		Hooks: hooks.NewBus(hooks.Config{
			BeforeTool: cfg.HookBeforeTool,
			AfterTool:  cfg.HookAfterTool,
			AfterTurn:  cfg.HookAfterTurn,
			AgentEvent: cfg.HookAgentEvent,
			EventFile:  cfg.HookEventFile,
		}),
		EventHandler: app.handleEvent,
	})
	app.workflowManager.SetOnChange(func() {
		if err := app.saveSession(); err != nil {
			fmt.Fprintf(app.errOut, "autosave error: %v\n", err)
		}
	})

	return app
}

func (a *App) Run(ctx context.Context) (runErr error) {
	a.logger.Debugf("starting local app mode print=%t tui=%t resume=%t cwd=%s session=%s config=%s", a.config.Print, a.config.TUI, a.config.Resume, a.config.WorkingDir, a.config.SessionFile, a.config.ConfigFile)
	if err := a.initPlugins(ctx); err != nil {
		return err
	}
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
		if err := a.closePlugins(); err != nil {
			if runErr == nil {
				runErr = err
				return
			}
			fmt.Fprintf(a.errOut, "plugin shutdown error: %v\n", err)
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
		a.logger.Debugf("completed one-shot run session=%s", a.config.SessionFile)
		return a.saveSession()
	}
	if a.config.TUI {
		return a.runTUI(ctx)
	}

	return a.runREPL(ctx)
}

func (a *App) runREPL(ctx context.Context) error {
	fmt.Fprintf(a.out, "xxx-code (%s)\n", a.config.Model)
	fmt.Fprintln(a.out, "Type :help for commands, :quit to exit.")
	if a.config.Resume {
		fmt.Fprintf(a.errOut, "resumed session from %s\n", a.config.SessionFile)
		a.logger.Debugf("resumed session from %s", a.config.SessionFile)
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
		fmt.Fprintln(a.out, ":workflows                list persisted workflow summaries")
		fmt.Fprintln(a.out, ":workflow <workflow-id>   print one persisted workflow snapshot")
		fmt.Fprintln(a.out, ":workflow-tasks <id> [status|name=<task>] list persisted workflow tasks")
		fmt.Fprintln(a.out, ":workflow-resume <id> [failed|task...]    resume a workflow from failed or selected tasks")
		fmt.Fprintln(a.out, ":plugins                  list loaded plugins and bridged tools")
		fmt.Fprintln(a.out, ":plugins-validate <path>  validate a plugin directory or manifest file")
		fmt.Fprintln(a.out, ":plugins-install <path> [force] install a plugin into the configured plugin dir")
		fmt.Fprintln(a.out, ":plugins-remove <name>    remove an installed plugin")
		fmt.Fprintln(a.out, ":plugins-reload           reload plugin manifests and bridged tools")
		fmt.Fprintln(a.out, ":mcp                      list MCP server status and loaded tools")
		fmt.Fprintln(a.out, ":mcp-health [server]      ping MCP servers and print live health")
		fmt.Fprintln(a.out, ":mcp-reload               reload the current MCP config and reconnect servers")
		fmt.Fprintln(a.out, ":mcp-validate [path]      validate the current MCP config file")
		fmt.Fprintln(a.out, ":mcp-resources [server]   list MCP resources")
		fmt.Fprintln(a.out, ":mcp-resource-templates [server] list MCP resource templates")
		fmt.Fprintln(a.out, ":mcp-prompts [server]     list MCP prompts")
		fmt.Fprintln(a.out, ":mcp-read <server> <uri>  read one MCP resource")
		fmt.Fprintln(a.out, ":mcp-prompt <server> <name> [k=v ...] fetch one MCP prompt")
		fmt.Fprintln(a.out, ":wait <agent-id>          wait for an agent and print its snapshot")
		fmt.Fprintln(a.out, ":wait-all [agent-id ...]  wait for a batch of agents or every known agent")
		fmt.Fprintln(a.out, ":send <agent-id> <prompt> continue an existing agent")
		fmt.Fprintln(a.out, ":cancel <agent-id>        cancel a running agent and its descendants")
		fmt.Fprintln(a.out, ":history [n]              print the latest n main-session messages (default 10)")
		fmt.Fprintln(a.out, ":compact                  compact the main session immediately if it exceeds budget")
		fmt.Fprintln(a.out, ":policy                   print active permission policy")
		fmt.Fprintln(a.out, ":hooks                    print configured hook commands")
		fmt.Fprintln(a.out, ":save                     persist the current main session, agents, and workflows")
		fmt.Fprintln(a.out, ":session                  print session file information")
		return false, nil
	case ":agents":
		snapshots := a.runner.ListAgents()
		data, _ := json.MarshalIndent(snapshots, "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":workflows":
		data, _ := json.MarshalIndent(a.workflowManager.ListWorkflows(), "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":workflow":
		if len(fields) < 2 {
			fmt.Fprintln(a.errOut, "usage: :workflow <workflow-id>")
			return false, nil
		}
		snapshot, ok := a.workflowManager.GetWorkflow(fields[1])
		if !ok {
			fmt.Fprintf(a.errOut, "error: workflow not found: %s\n", fields[1])
			return false, nil
		}
		data, _ := json.MarshalIndent(snapshot, "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
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
		tasks, err := a.workflowManager.ListWorkflowTasks(fields[1], statusFilter, nameFilter)
		if err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			return false, nil
		}
		data, _ := json.MarshalIndent(tasks, "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":workflow-resume":
		if len(fields) < 2 {
			fmt.Fprintln(a.errOut, "usage: :workflow-resume <workflow-id> [failed|task...]")
			return false, nil
		}
		options := tools.ResumeWorkflowOptions{}
		if len(fields) > 2 {
			if strings.EqualFold(fields[2], "failed") {
				options.OnlyFailed = true
			} else {
				options.TaskNames = append([]string(nil), fields[2:]...)
			}
		}
		resumeCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		snapshot, results, agents, err := a.workflowManager.ResumeWorkflow(resumeCtx, fields[1], &engine.ExecutionContext{
			Runner:     a.runner,
			Session:    a.session,
			WorkingDir: a.config.WorkingDir,
		}, options)
		if err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			return false, nil
		}
		data, _ := json.MarshalIndent(map[string]any{
			"workflow": snapshot,
			"tasks":    results,
			"agents":   agents,
		}, "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, a.saveSession()
	case ":plugins":
		data, _ := json.MarshalIndent(a.currentPluginSummary(), "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":plugins-validate":
		if len(fields) < 2 {
			fmt.Fprintln(a.errOut, "usage: :plugins-validate <path>")
			return false, nil
		}
		if a.pluginManager == nil {
			if err := a.initPlugins(ctx); err != nil {
				fmt.Fprintf(a.errOut, "error: %v\n", err)
				return false, nil
			}
		}
		data, _ := json.MarshalIndent(a.pluginManager.Validate(fields[1]), "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":plugins-install":
		if len(fields) < 2 {
			fmt.Fprintln(a.errOut, "usage: :plugins-install <path> [force]")
			return false, nil
		}
		if a.pluginManager == nil {
			if err := a.initPlugins(ctx); err != nil {
				fmt.Fprintf(a.errOut, "error: %v\n", err)
				return false, nil
			}
		}
		force := len(fields) > 2 && (strings.EqualFold(fields[2], "force") || strings.EqualFold(fields[2], "--force"))
		if err := a.pluginManager.Install(ctx, fields[1], force); err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			return false, nil
		}
		data, _ := json.MarshalIndent(a.currentPluginSummary(), "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":plugins-remove":
		if len(fields) < 2 {
			fmt.Fprintln(a.errOut, "usage: :plugins-remove <name>")
			return false, nil
		}
		if a.pluginManager == nil {
			if err := a.initPlugins(ctx); err != nil {
				fmt.Fprintf(a.errOut, "error: %v\n", err)
				return false, nil
			}
		}
		if err := a.pluginManager.Remove(ctx, fields[1]); err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			return false, nil
		}
		data, _ := json.MarshalIndent(a.currentPluginSummary(), "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":plugins-reload":
		if err := a.reloadPlugins(ctx); err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			return false, nil
		}
		data, _ := json.MarshalIndent(a.currentPluginSummary(), "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":mcp":
		data, _ := json.MarshalIndent(a.currentMCPSummary(), "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":mcp-health":
		if a.mcpManager == nil {
			fmt.Fprintln(a.errOut, "error: MCP is not configured")
			return false, nil
		}
		server := ""
		if len(fields) > 1 {
			server = fields[1]
		}
		result, err := a.mcpManager.Health(ctx, server)
		if err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			return false, nil
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":mcp-reload":
		if err := a.reloadMCP(ctx); err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			return false, nil
		}
		data, _ := json.MarshalIndent(a.currentMCPSummary(), "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":mcp-validate":
		configFile := a.config.MCPConfigFile
		if len(fields) > 1 {
			configFile = fields[1]
		}
		report := mcpruntime.ValidateOptions(mcpruntime.Options{
			WorkingDir: a.config.WorkingDir,
			ConfigFile: configFile,
		})
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":mcp-resources":
		if a.mcpManager == nil {
			fmt.Fprintln(a.errOut, "error: MCP is not configured")
			return false, nil
		}
		server := ""
		if len(fields) > 1 {
			server = fields[1]
		}
		result, err := a.mcpManager.ListResources(ctx, server)
		if err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			return false, nil
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":mcp-resource-templates":
		if a.mcpManager == nil {
			fmt.Fprintln(a.errOut, "error: MCP is not configured")
			return false, nil
		}
		server := ""
		if len(fields) > 1 {
			server = fields[1]
		}
		result, err := a.mcpManager.ListResourceTemplates(ctx, server)
		if err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			return false, nil
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":mcp-prompts":
		if a.mcpManager == nil {
			fmt.Fprintln(a.errOut, "error: MCP is not configured")
			return false, nil
		}
		server := ""
		if len(fields) > 1 {
			server = fields[1]
		}
		result, err := a.mcpManager.ListPrompts(ctx, server)
		if err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			return false, nil
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":mcp-read":
		if a.mcpManager == nil {
			fmt.Fprintln(a.errOut, "error: MCP is not configured")
			return false, nil
		}
		if len(fields) < 3 {
			fmt.Fprintln(a.errOut, "usage: :mcp-read <server> <uri>")
			return false, nil
		}
		result, err := a.mcpManager.ReadResource(ctx, fields[1], fields[2])
		if err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			return false, nil
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.out, string(data))
		return false, nil
	case ":mcp-prompt":
		if a.mcpManager == nil {
			fmt.Fprintln(a.errOut, "error: MCP is not configured")
			return false, nil
		}
		if len(fields) < 3 {
			fmt.Fprintln(a.errOut, "usage: :mcp-prompt <server> <name> [key=value ...]")
			return false, nil
		}
		arguments, err := parseLocalPromptArguments(fields[3:])
		if err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			return false, nil
		}
		result, err := a.mcpManager.GetPrompt(ctx, fields[1], fields[2], arguments)
		if err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			return false, nil
		}
		data, _ := json.MarshalIndent(result, "", "  ")
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
			"event_file":  a.config.HookEventFile,
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
		fmt.Fprintf(a.out, "workflows: %d\n", len(a.workflowManager.ListWorkflows()))
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
	if err := a.workflowManager.ImportWorkflows(state.Workflows); err != nil {
		return fmt.Errorf("resume workflows: %w", err)
	}
	return nil
}

func (a *App) saveSession() error {
	a.saveMu.Lock()
	defer a.saveMu.Unlock()
	err := persist.Save(a.config.SessionFile, a.session, a.runner, a.workflowManager)
	if err == nil {
		a.logger.Debugf("saved session file=%s messages=%d agents=%d workflows=%d", a.config.SessionFile, len(a.session.Snapshot()), len(a.runner.ListAgents()), len(a.workflowManager.ListWorkflows()))
	}
	return err
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

func (a *App) initPlugins(ctx context.Context) error {
	manager, err := pluginruntime.Start(ctx, a.registry, pluginruntime.Options{
		WorkingDir: a.config.WorkingDir,
		PluginDir:  a.config.PluginDir,
	})
	if err != nil {
		return err
	}
	a.pluginManager = manager
	if a.pluginManager == nil {
		return nil
	}
	for _, status := range a.pluginManager.Statuses() {
		switch status.Status {
		case pluginruntime.StatusLoaded:
			if a.config.Verbose {
				fmt.Fprintf(a.errOut, "plugin %s loaded (%d tools)\n", status.Name, len(status.ToolNames))
			}
		case pluginruntime.StatusFailed:
			fmt.Fprintf(a.errOut, "plugin %s failed: %s\n", status.Name, status.Error)
		}
		for _, warning := range status.Warnings {
			fmt.Fprintf(a.errOut, "plugin %s warning: %s\n", status.Name, warning)
		}
	}
	return nil
}

func (a *App) reloadPlugins(ctx context.Context) error {
	if a.pluginManager == nil {
		return a.initPlugins(ctx)
	}
	if err := a.pluginManager.Reload(ctx); err != nil {
		return err
	}
	for _, status := range a.pluginManager.Statuses() {
		switch status.Status {
		case pluginruntime.StatusFailed:
			fmt.Fprintf(a.errOut, "plugin %s failed: %s\n", status.Name, status.Error)
		}
		for _, warning := range status.Warnings {
			fmt.Fprintf(a.errOut, "plugin %s warning: %s\n", status.Name, warning)
		}
	}
	return nil
}

func (a *App) currentPluginSummary() map[string]any {
	statuses := []pluginruntime.Status{}
	pluginDir := ""
	pluginCount := 0
	toolCount := 0
	if a.pluginManager != nil {
		statuses = a.pluginManager.Statuses()
		pluginDir = a.pluginManager.PluginDir()
		pluginCount = a.pluginManager.PluginCount()
		toolCount = a.pluginManager.ToolCount()
	}
	return map[string]any{
		"plugin_dir":   pluginDir,
		"plugin_count": pluginCount,
		"tool_count":   toolCount,
		"statuses":     statuses,
	}
}

func (a *App) reloadMCP(ctx context.Context) error {
	if a.mcpManager == nil {
		return a.initMCP(ctx)
	}
	if err := a.mcpManager.Reload(ctx); err != nil {
		return err
	}
	for _, status := range a.mcpManager.Statuses() {
		switch status.Status {
		case mcpruntime.ServerStatusFailed:
			fmt.Fprintf(a.errOut, "mcp server %s failed: %s\n", status.Name, status.Error)
		}
		for _, warning := range status.Warnings {
			fmt.Fprintf(a.errOut, "mcp server %s warning: %s\n", status.Name, warning)
		}
	}
	return nil
}

func (a *App) currentMCPSummary() map[string]any {
	statuses := []mcpruntime.ServerStatus{}
	configPath := ""
	serverCount := 0
	toolCount := 0
	if a.mcpManager != nil {
		statuses = a.mcpManager.Statuses()
		configPath = a.mcpManager.ConfigPath()
		serverCount = a.mcpManager.ServerCount()
		toolCount = a.mcpManager.ToolCount()
	}
	return map[string]any{
		"config_path":  configPath,
		"server_count": serverCount,
		"tool_count":   toolCount,
		"statuses":     statuses,
	}
}

func (a *App) closePlugins() error {
	if a.pluginManager == nil {
		return nil
	}
	return a.pluginManager.Close()
}

func (a *App) closeMCP() error {
	if a.mcpManager == nil {
		return nil
	}
	return a.mcpManager.Close()
}

func (a *App) handleEvent(event engine.Event) {
	if a.tui != nil {
		a.tui.handleEvent(event)
	} else {
		a.printEvent(event)
	}
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

func (a *App) runPrompt(ctx context.Context, prompt string) (engine.RunResult, error) {
	result, err := a.runner.RunTurn(ctx, a.session, prompt)
	if err != nil {
		return result, err
	}
	return result, a.saveSession()
}

func parseLocalPromptArguments(parts []string) (map[string]string, error) {
	if len(parts) == 0 {
		return nil, nil
	}
	values := make(map[string]string, len(parts))
	for _, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("invalid prompt argument %q: expected key=value", part)
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return values, nil
}
