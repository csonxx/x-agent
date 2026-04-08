package diag

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"strings"
)

const TraceHeader = "X-Trace-ID"

type Level int

const (
	LevelError Level = iota
	LevelInfo
	LevelDebug
)

func ParseLevel(raw string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "info":
		return LevelInfo, nil
	case "debug":
		return LevelDebug, nil
	case "error":
		return LevelError, nil
	default:
		return LevelInfo, fmt.Errorf("invalid log level: %s", raw)
	}
}

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelError:
		return "error"
	default:
		return "info"
	}
}

type Logger struct {
	level  Level
	logger *log.Logger
}

func New(out io.Writer, level Level) *Logger {
	if out == nil {
		out = io.Discard
	}
	return &Logger{
		level:  level,
		logger: log.New(out, "xxx-code ", log.LstdFlags|log.Lmicroseconds|log.LUTC),
	}
}

func (l *Logger) Enabled(level Level) bool {
	if l == nil {
		return false
	}
	return l.level >= level
}

func (l *Logger) Debugf(format string, args ...any) {
	l.logf(LevelDebug, "DEBUG", format, args...)
}

func (l *Logger) Infof(format string, args ...any) {
	l.logf(LevelInfo, "INFO", format, args...)
}

func (l *Logger) Errorf(format string, args ...any) {
	l.logf(LevelError, "ERROR", format, args...)
}

func (l *Logger) logf(level Level, label, format string, args ...any) {
	if !l.Enabled(level) {
		return
	}
	l.logger.Printf("[%s] %s", label, fmt.Sprintf(format, args...))
}

func NewTraceID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "trace_unknown"
	}
	return "trace_" + hex.EncodeToString(buf)
}
