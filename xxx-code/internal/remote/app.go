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
)

type App struct {
	config    config.Config
	client    *Client
	out       io.Writer
	errOut    io.Writer
	sessionID string
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
		client: NewClient(cfg.RemoteURL, nil),
		out:    out,
		errOut: errOut,
	}
}

func (a *App) Run(ctx context.Context) error {
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

	if a.config.Print {
		if strings.TrimSpace(a.config.Prompt) == "" {
			return fmt.Errorf("remote print mode requires a prompt")
		}
		result, updated, err := a.client.RunTurn(ctx, a.sessionID, a.config.Prompt, 0)
		if err != nil {
			return err
		}
		a.sessionID = updated.ID
		if strings.TrimSpace(result.FinalText) != "" {
			fmt.Fprintln(a.out, result.FinalText)
		}
		return nil
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

		result, updated, err := a.client.RunTurn(ctx, a.sessionID, line, 0)
		if err != nil {
			fmt.Fprintf(a.errOut, "error: %v\n", err)
			continue
		}
		a.sessionID = updated.ID
		if strings.TrimSpace(result.FinalText) != "" {
			fmt.Fprintln(a.out, result.FinalText)
		}
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
		fmt.Fprintln(a.out, ":agents                   list remote agents")
		fmt.Fprintln(a.out, ":wait <agent-id>          wait for a remote agent")
		fmt.Fprintln(a.out, ":send <agent-id> <prompt> continue an existing remote agent")
		fmt.Fprintln(a.out, ":cancel <agent-id>        cancel a remote agent and descendants")
		fmt.Fprintln(a.out, ":workflows                list remote workflows")
		fmt.Fprintln(a.out, ":workflow <id>            print one remote workflow snapshot")
		fmt.Fprintln(a.out, ":workflow-resume <id>     resume an interrupted remote workflow")
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
	case ":workflow-resume":
		if len(fields) < 2 {
			fmt.Fprintln(a.errOut, "usage: :workflow-resume <workflow-id>")
			return false, nil
		}
		return false, a.printJSON(ctx, func(ctx context.Context) (any, error) {
			resumeCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
			defer cancel()
			return a.client.ResumeWorkflow(resumeCtx, a.sessionID, fields[1], 1800)
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
