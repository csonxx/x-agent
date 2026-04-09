package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
)

type EventFileSink struct {
	path string
	mu   sync.Mutex
}

func NewEventFileSink(path string) *EventFileSink {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	return &EventFileSink{path: path}
}

func (s *EventFileSink) HandleHook(ctx context.Context, event engine.HookEvent) error {
	_ = ctx
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}

	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.Write(data)
	return err
}
