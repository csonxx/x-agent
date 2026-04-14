package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
)

func TestBashToolBlocksWriteRedirectionInReadOnlyMode(t *testing.T) {
	dir := t.TempDir()
	runner := engine.NewRunner(nil, engine.NewRegistry(), engine.RunnerConfig{
		WorkingDir: dir,
		PermissionPolicy: engine.PermissionPolicy{
			ReadRoots:   []string{dir},
			WriteRoots:  []string{dir},
			ReadOnly:    true,
			BashEnabled: true,
		},
	})

	input, _ := json.Marshal(map[string]any{
		"command": "echo blocked > blocked.txt",
	})

	_, err := (&BashTool{}).Call(context.Background(), &engine.ExecutionContext{
		Runner:     runner,
		WorkingDir: dir,
	}, input)
	if err == nil || !strings.Contains(err.Error(), "read-only mode") {
		t.Fatalf("expected read-only bash error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "blocked.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("expected blocked.txt to not be created, got err=%v", statErr)
	}
}

func TestBashToolAllowsReadOnlyCommandInReadOnlyMode(t *testing.T) {
	dir := t.TempDir()
	runner := engine.NewRunner(nil, engine.NewRegistry(), engine.RunnerConfig{
		WorkingDir: dir,
		PermissionPolicy: engine.PermissionPolicy{
			ReadRoots:   []string{dir},
			WriteRoots:  []string{dir},
			ReadOnly:    true,
			BashEnabled: true,
		},
	})

	input, _ := json.Marshal(map[string]any{
		"command": "pwd",
	})

	result, err := (&BashTool{}).Call(context.Background(), &engine.ExecutionContext{
		Runner:     runner,
		WorkingDir: dir,
	}, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected pwd to succeed, got %s", result.Content)
	}
	if !strings.Contains(result.Content, dir) {
		t.Fatalf("expected output to mention working dir %q, got %s", dir, result.Content)
	}
}
