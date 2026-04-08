package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/caowenhua/x-agent/xxx-code/internal/buildinfo"
	"github.com/caowenhua/x-agent/xxx-code/internal/cli"
	"github.com/caowenhua/x-agent/xxx-code/internal/config"
	"github.com/caowenhua/x-agent/xxx-code/internal/daemon"
	"github.com/caowenhua/x-agent/xxx-code/internal/remote"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}
	if cfg.ShowVersion {
		fmt.Fprint(os.Stdout, buildinfo.String())
		return
	}

	errOut, closeErrOut, err := openErrorOutput(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "log setup error: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if closeErr := closeErrOut(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "log close error: %v\n", closeErr)
		}
	}()

	if cfg.Daemon && cfg.RemoteURL != "" {
		fmt.Fprintln(errOut, "config error: --daemon cannot be combined with --remote-url")
		os.Exit(1)
	}

	if cfg.Daemon {
		if cfg.TUI {
			fmt.Fprintln(errOut, "config error: --daemon cannot be combined with --tui")
			os.Exit(1)
		}
		if cfg.Print {
			fmt.Fprintln(errOut, "config error: --daemon cannot be combined with --print or a direct prompt")
			os.Exit(1)
		}
		server := daemon.New(cfg, os.Stdout, errOut, nil)
		if err := server.Run(context.Background()); err != nil {
			fmt.Fprintf(errOut, "run error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if cfg.RemoteURL != "" {
		app := remote.New(cfg, os.Stdout, errOut)
		if err := app.Run(context.Background()); err != nil {
			fmt.Fprintf(errOut, "run error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	app := cli.New(cfg, os.Stdout, errOut)
	if err := app.Run(context.Background()); err != nil {
		fmt.Fprintf(errOut, "run error: %v\n", err)
		os.Exit(1)
	}
}

func openErrorOutput(cfg config.Config) (io.Writer, func() error, error) {
	if cfg.LogFile == "" {
		return os.Stderr, func() error { return nil }, nil
	}
	if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0o755); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	return io.MultiWriter(os.Stderr, file), file.Close, nil
}
