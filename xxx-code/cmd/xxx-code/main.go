package main

import (
	"context"
	"errors"
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
	os.Exit(runMain(os.Args[1:], os.Stdout, os.Stderr))
}

func runMain(args []string, stdout, stderr io.Writer) int {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "config error: %v\n", err)
		return 1
	}

	cfg, err := config.LoadArgs(args, os.LookupEnv, wd)
	if err != nil {
		var helpErr *config.HelpError
		if errors.As(err, &helpErr) {
			fmt.Fprint(stdout, helpErr.Usage)
			return 0
		}
		fmt.Fprintf(stderr, "config error: %v\n", err)
		return 1
	}
	if cfg.ShowVersion {
		fmt.Fprint(stdout, buildinfo.String())
		return 0
	}

	errOut, closeErrOut, err := openErrorOutput(cfg, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "log setup error: %v\n", err)
		return 1
	}
	defer func() {
		if closeErr := closeErrOut(); closeErr != nil {
			fmt.Fprintf(stderr, "log close error: %v\n", closeErr)
		}
	}()

	if cfg.Daemon && cfg.RemoteURL != "" {
		fmt.Fprintln(errOut, "config error: --daemon cannot be combined with --remote-url")
		return 1
	}

	if cfg.Daemon {
		if cfg.TUI {
			fmt.Fprintln(errOut, "config error: --daemon cannot be combined with --tui")
			return 1
		}
		if cfg.Print {
			fmt.Fprintln(errOut, "config error: --daemon cannot be combined with --print or a direct prompt")
			return 1
		}
		server := daemon.New(cfg, stdout, errOut, nil)
		if err := server.Run(context.Background()); err != nil {
			fmt.Fprintf(errOut, "run error: %v\n", err)
			return 1
		}
		return 0
	}

	if cfg.RemoteURL != "" {
		app := remote.New(cfg, stdout, errOut)
		if err := app.Run(context.Background()); err != nil {
			fmt.Fprintf(errOut, "run error: %v\n", err)
			return 1
		}
		return 0
	}

	app := cli.New(cfg, stdout, errOut)
	if err := app.Run(context.Background()); err != nil {
		fmt.Fprintf(errOut, "run error: %v\n", err)
		return 1
	}
	return 0
}

func openErrorOutput(cfg config.Config, baseErr io.Writer) (io.Writer, func() error, error) {
	if cfg.LogFile == "" {
		return baseErr, func() error { return nil }, nil
	}
	if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0o755); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	return io.MultiWriter(baseErr, file), file.Close, nil
}
