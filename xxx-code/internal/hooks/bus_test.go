package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
)

func TestBusFansOutToScriptAndEventFile(t *testing.T) {
	dir := t.TempDir()
	scriptOutput := filepath.Join(dir, "script.json")
	eventFile := filepath.Join(dir, "events.jsonl")

	handler := NewBus(Config{
		AfterTurn: "cat > " + scriptOutput,
		EventFile: eventFile,
	})
	if handler == nil {
		t.Fatal("expected hook bus to be created")
	}

	event := engine.HookEvent{
		Kind:       engine.HookAfterTurn,
		WorkingDir: dir,
		AgentID:    "agent_1",
		Status:     "completed",
		FinalText:  "done",
	}
	if err := handler.HandleHook(context.Background(), event); err != nil {
		t.Fatal(err)
	}

	scriptData, err := os.ReadFile(scriptOutput)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(scriptData), `"kind":"after_turn"`) {
		t.Fatalf("expected script hook payload, got %s", string(scriptData))
	}

	logData, err := os.ReadFile(eventFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), `"final_text":"done"`) {
		t.Fatalf("expected event file payload, got %s", string(logData))
	}
}

func TestEventFileSinkAppendsJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.jsonl")
	sink := NewEventFileSink(path)
	if sink == nil {
		t.Fatal("expected event file sink to be created")
	}

	if err := sink.HandleHook(context.Background(), engine.HookEvent{Kind: engine.HookAfterTurn, Status: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := sink.HandleHook(context.Background(), engine.HookEvent{Kind: engine.HookAfterTurn, Status: "two"}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 jsonl lines, got %d in %q", len(lines), string(data))
	}
	if !strings.Contains(lines[0], `"status":"one"`) || !strings.Contains(lines[1], `"status":"two"`) {
		t.Fatalf("unexpected jsonl payload: %q", string(data))
	}
}
