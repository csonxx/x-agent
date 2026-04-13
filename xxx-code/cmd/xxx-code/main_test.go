package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caowenhua/x-agent/xxx-code/internal/config"
)

func TestOpenErrorOutputDefaultsToStderr(t *testing.T) {
	var stderr bytes.Buffer
	writer, closeFn, err := openErrorOutput(config.Config{}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if writer != &stderr {
		t.Fatalf("expected stderr writer, got %T", writer)
	}
	if err := closeFn(); err != nil {
		t.Fatalf("expected noop close function, got %v", err)
	}
}

func TestOpenErrorOutputCreatesAndWritesLogFile(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "logs", "xxx-code.log")

	oldStderr := os.Stderr
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writePipe
	defer func() {
		os.Stderr = oldStderr
		_ = readPipe.Close()
	}()

	writer, closeFn, err := openErrorOutput(config.Config{LogFile: logFile}, writePipe)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, "hello log\n"); err != nil {
		t.Fatal(err)
	}
	if err := closeFn(); err != nil {
		t.Fatal(err)
	}
	if err := writePipe.Close(); err != nil {
		t.Fatal(err)
	}

	stderrData, err := io.ReadAll(readPipe)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stderrData), "hello log") {
		t.Fatalf("expected mirrored stderr output, got %q", string(stderrData))
	}

	fileData, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fileData), "hello log") {
		t.Fatalf("expected log file to contain written text, got %q", string(fileData))
	}
}

func TestRunMainPrintsHelpAndReturnsZero(t *testing.T) {
	dir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	}()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := runMain([]string{"--help"}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("expected help to exit 0, got %d", exitCode)
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr for help, got %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Usage: xxx-code [flags] [prompt]") {
		t.Fatalf("expected usage header in help output, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "-provider") {
		t.Fatalf("expected flags in help output, got %q", stdout.String())
	}
}
