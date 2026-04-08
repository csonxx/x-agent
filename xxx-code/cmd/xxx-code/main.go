package main

import (
	"context"
	"fmt"
	"os"

	"github.com/caowenhua/x-agent/xxx-code/internal/cli"
	"github.com/caowenhua/x-agent/xxx-code/internal/config"
	"github.com/caowenhua/x-agent/xxx-code/internal/daemon"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	if cfg.Daemon {
		if cfg.TUI {
			fmt.Fprintln(os.Stderr, "config error: --daemon cannot be combined with --tui")
			os.Exit(1)
		}
		if cfg.Print {
			fmt.Fprintln(os.Stderr, "config error: --daemon cannot be combined with --print or a direct prompt")
			os.Exit(1)
		}
		server := daemon.New(cfg, os.Stdout, os.Stderr, nil)
		if err := server.Run(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "run error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	app := cli.New(cfg, os.Stdout, os.Stderr)
	if err := app.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "run error: %v\n", err)
		os.Exit(1)
	}
}
