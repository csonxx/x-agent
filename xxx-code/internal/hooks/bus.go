package hooks

import (
	"context"
	"errors"
	"strings"

	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
)

type Bus struct {
	handlers []engine.HookHandler
}

func NewBus(cfg Config) engine.HookHandler {
	bus := &Bus{}
	if hasScriptHooks(cfg) {
		bus.handlers = append(bus.handlers, NewScriptManager(cfg))
	}
	if strings.TrimSpace(cfg.EventFile) != "" {
		bus.handlers = append(bus.handlers, NewEventFileSink(cfg.EventFile))
	}
	if len(bus.handlers) == 0 {
		return nil
	}
	return bus
}

func (b *Bus) HandleHook(ctx context.Context, event engine.HookEvent) error {
	if b == nil {
		return nil
	}
	var errs []error
	for _, handler := range b.handlers {
		if handler == nil {
			continue
		}
		if err := handler.HandleHook(ctx, event); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func hasScriptHooks(cfg Config) bool {
	return strings.TrimSpace(cfg.BeforeTool) != "" ||
		strings.TrimSpace(cfg.AfterTool) != "" ||
		strings.TrimSpace(cfg.AfterTurn) != "" ||
		strings.TrimSpace(cfg.AgentEvent) != ""
}
