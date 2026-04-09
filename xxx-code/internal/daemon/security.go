package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/config"
)

const (
	daemonModeSessionsRead  = "sessions_read"
	daemonModeSessionsWrite = "sessions_write"
	daemonModeTurns         = "turns"
	daemonModeIntrospection = "introspection"
	daemonModePlugins       = "plugins"
	daemonModeMCP           = "mcp"
	daemonModeAgents        = "agents"
	daemonModeWorkflows     = "workflows"
	daemonModeAudit         = "audit"
	daemonModeSave          = "save"
)

type requestMeta struct {
	TraceID    string
	RemoteAddr string
}

type requestMetaKey struct{}

type AuditEvent struct {
	Timestamp         time.Time      `json:"timestamp"`
	TraceID           string         `json:"trace_id,omitempty"`
	RemoteAddr        string         `json:"remote_addr,omitempty"`
	SessionID         string         `json:"session_id,omitempty"`
	Action            string         `json:"action"`
	Mode              string         `json:"mode,omitempty"`
	Method            string         `json:"method,omitempty"`
	Path              string         `json:"path,omitempty"`
	AgentID           string         `json:"agent_id,omitempty"`
	AgentName         string         `json:"agent_name,omitempty"`
	ToolName          string         `json:"tool_name,omitempty"`
	StatusCode        int            `json:"status_code,omitempty"`
	Outcome           string         `json:"outcome,omitempty"`
	Code              string         `json:"code,omitempty"`
	Message           string         `json:"message,omitempty"`
	RetryAfterSeconds int            `json:"retry_after_seconds,omitempty"`
	Details           map[string]any `json:"details,omitempty"`
}

type auditLogger struct {
	path string
	mu   sync.Mutex
}

type daemonAccessPolicy struct {
	allowedModes         map[string]struct{}
	deniedModes          map[string]struct{}
	allowedSessionPrefix []string
	deniedSessionPrefix  []string
}

type requestRateLimiter struct {
	ratePerSecond float64
	capacity      float64

	mu      sync.Mutex
	clients map[string]rateBucket
}

type rateBucket struct {
	Tokens    float64
	UpdatedAt time.Time
}

func newAuditLogger(path string) *auditLogger {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	return &auditLogger{path: path}
}

func (l *auditLogger) Enabled() bool {
	return l != nil && strings.TrimSpace(l.path) != ""
}

func (l *auditLogger) Log(event AuditEvent) error {
	if !l.Enabled() {
		return nil
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if strings.TrimSpace(event.Action) == "" {
		event.Action = "event"
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	file, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	return encoder.Encode(event)
}

func (l *auditLogger) List(limit int, sessionID string) ([]AuditEvent, error) {
	if !l.Enabled() {
		return nil, nil
	}
	file, err := os.Open(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	sessionID = strings.TrimSpace(sessionID)
	events := make([]AuditEvent, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var event AuditEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if sessionID != "" && event.SessionID != sessionID {
			continue
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	return events, nil
}

func newDaemonAccessPolicy(cfg config.Config) daemonAccessPolicy {
	return daemonAccessPolicy{
		allowedModes:         normalizeModeSet(cfg.DaemonAllowModes),
		deniedModes:          normalizeModeSet(cfg.DaemonDenyModes),
		allowedSessionPrefix: normalizeTrimmedValues(cfg.DaemonAllowSessionPrefixes),
		deniedSessionPrefix:  normalizeTrimmedValues(cfg.DaemonDenySessionPrefixes),
	}
}

func (p daemonAccessPolicy) Allow(mode, sessionID string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	sessionID = strings.TrimSpace(sessionID)

	if mode != "" {
		if _, blocked := p.deniedModes[mode]; blocked {
			return fmt.Errorf("daemon access to mode %s is blocked by ACL", mode)
		}
		if len(p.allowedModes) > 0 {
			if _, allowed := p.allowedModes[mode]; !allowed {
				return fmt.Errorf("daemon access to mode %s is not allowed by ACL", mode)
			}
		}
	}

	if sessionID != "" {
		if matchesPrefix(sessionID, p.deniedSessionPrefix) {
			return fmt.Errorf("daemon access to session %s is blocked by ACL", sessionID)
		}
		if len(p.allowedSessionPrefix) > 0 && !matchesPrefix(sessionID, p.allowedSessionPrefix) {
			return fmt.Errorf("daemon access to session %s is not allowed by ACL", sessionID)
		}
	}
	return nil
}

func newRequestRateLimiter(limitPerMinute, burst int) *requestRateLimiter {
	if limitPerMinute <= 0 {
		return nil
	}
	if burst <= 0 {
		burst = limitPerMinute
	}
	if burst <= 0 {
		burst = 1
	}
	return &requestRateLimiter{
		ratePerSecond: float64(limitPerMinute) / 60.0,
		capacity:      float64(burst),
		clients:       make(map[string]rateBucket),
	}
}

func (l *requestRateLimiter) Allow(key string, now time.Time) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	key = strings.TrimSpace(key)
	if key == "" {
		key = "unknown"
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	bucket := l.clients[key]
	if bucket.UpdatedAt.IsZero() {
		bucket = rateBucket{
			Tokens:    l.capacity,
			UpdatedAt: now,
		}
	}

	elapsed := now.Sub(bucket.UpdatedAt).Seconds()
	if elapsed > 0 {
		bucket.Tokens += elapsed * l.ratePerSecond
		if bucket.Tokens > l.capacity {
			bucket.Tokens = l.capacity
		}
		bucket.UpdatedAt = now
	}

	if bucket.Tokens >= 1 {
		bucket.Tokens--
		l.clients[key] = bucket
		l.compact(now)
		return true, 0
	}

	l.clients[key] = bucket
	if l.ratePerSecond <= 0 {
		return false, time.Minute
	}
	retryAfter := time.Duration((1 - bucket.Tokens) / l.ratePerSecond * float64(time.Second))
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	l.compact(now)
	return false, retryAfter
}

func (l *requestRateLimiter) compact(now time.Time) {
	if len(l.clients) <= 256 {
		return
	}
	cutoff := now.Add(-10 * time.Minute)
	for key, bucket := range l.clients {
		if bucket.UpdatedAt.Before(cutoff) {
			delete(l.clients, key)
		}
	}
}

func classifyDaemonRoute(path, method string) (string, string) {
	path = strings.TrimSpace(path)
	if path == "" || !strings.HasPrefix(path, "/v1/") {
		return "", ""
	}
	path = strings.Trim(strings.TrimPrefix(path, "/v1/"), "/")
	if path == "" {
		return "", ""
	}
	if path == "audit" {
		return daemonModeAudit, ""
	}
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] != "sessions" {
		return "", ""
	}
	if len(parts) == 1 {
		if method == http.MethodPost {
			return daemonModeSessionsWrite, ""
		}
		return daemonModeSessionsRead, ""
	}

	sessionID := strings.TrimSpace(parts[1])
	if len(parts) == 2 {
		return daemonModeSessionsRead, sessionID
	}

	switch parts[2] {
	case "messages":
		return daemonModeSessionsRead, sessionID
	case "turns":
		return daemonModeTurns, sessionID
	case "save":
		return daemonModeSave, sessionID
	case "policy", "hooks":
		return daemonModeIntrospection, sessionID
	case "mcp":
		return daemonModeMCP, sessionID
	case "agents":
		return daemonModeAgents, sessionID
	case "workflows":
		return daemonModeWorkflows, sessionID
	case "audit":
		return daemonModeAudit, sessionID
	default:
		return "", sessionID
	}
}

func withRequestMeta(ctx context.Context, meta requestMeta) context.Context {
	return context.WithValue(ctx, requestMetaKey{}, meta)
}

func requestMetaFromContext(ctx context.Context) requestMeta {
	if ctx == nil {
		return requestMeta{}
	}
	meta, _ := ctx.Value(requestMetaKey{}).(requestMeta)
	return meta
}

func clientAddress(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := strings.TrimSpace(r.RemoteAddr)
	if host == "" {
		return ""
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		return parsedHost
	}
	return host
}

func matchesPrefix(value string, prefixes []string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, prefix := range prefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix != "" && strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func normalizeModeSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		set[value] = struct{}{}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

func normalizeTrimmedValues(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}
