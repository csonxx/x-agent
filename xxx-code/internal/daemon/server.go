package daemon

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/auth"
	"github.com/caowenhua/x-agent/xxx-code/internal/config"
	"github.com/caowenhua/x-agent/xxx-code/internal/diag"
	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	"github.com/caowenhua/x-agent/xxx-code/internal/hooks"
	mcpruntime "github.com/caowenhua/x-agent/xxx-code/internal/mcp"
	"github.com/caowenhua/x-agent/xxx-code/internal/persist"
	"github.com/caowenhua/x-agent/xxx-code/internal/provider"
	"github.com/caowenhua/x-agent/xxx-code/internal/tools"
)

type ProviderFactory func(config.Config) engine.Provider

type Server struct {
	config          config.Config
	providerFactory ProviderFactory
	out             io.Writer
	errOut          io.Writer
	logger          *diag.Logger
	audit           *auditLogger
	access          daemonAccessPolicy
	rateLimiter     *requestRateLimiter

	mu       sync.Mutex
	sessions map[string]*managedSession
}

type managedSession struct {
	id              string
	config          config.Config
	registry        *engine.Registry
	runner          *engine.Runner
	session         *engine.Session
	workflowManager *tools.WorkflowManager
	mcpManager      *mcpruntime.Manager
	audit           *auditLogger
	out             io.Writer
	errOut          io.Writer
	sessionFile     string
	loadedAt        time.Time

	saveMu sync.Mutex
	runMu  sync.Mutex
	subMu  sync.Mutex
	subSeq int
	subs   map[int]*eventSubscriber

	lifecycleMu      sync.Mutex
	closed           bool
	activeTurnCancel context.CancelFunc
}

type sessionSummary struct {
	ID            string    `json:"id"`
	SessionFile   string    `json:"session_file"`
	WorkingDir    string    `json:"working_dir"`
	Loaded        bool      `json:"loaded"`
	LoadedAt      time.Time `json:"loaded_at,omitempty"`
	MessageCount  int       `json:"message_count"`
	ApproxTokens  int       `json:"approx_tokens"`
	AgentCount    int       `json:"agent_count"`
	WorkflowCount int       `json:"workflow_count"`
	SavedAt       time.Time `json:"saved_at,omitempty"`
}

type createSessionRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Resume    bool   `json:"resume,omitempty"`
}

type turnRequest struct {
	Prompt         string `json:"prompt"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type waitRequest struct {
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

type workflowResumeRequest struct {
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
	OnlyFailed     bool     `json:"only_failed,omitempty"`
	TaskNames      []string `json:"task_names,omitempty"`
}

type sendAgentRequest struct {
	Prompt     string `json:"prompt"`
	Background bool   `json:"background,omitempty"`
}

type cancelAgentRequest struct {
	Recursive bool `json:"recursive,omitempty"`
}

type mcpReadResourceRequest struct {
	Server string `json:"server"`
	URI    string `json:"uri"`
}

type mcpGetPromptRequest struct {
	Server    string            `json:"server"`
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

type mcpValidateRequest struct {
	ConfigFile string `json:"config_file,omitempty"`
}

type sessionMCPSummary struct {
	ConfigPath  string                    `json:"config_path,omitempty"`
	ServerCount int                       `json:"server_count"`
	ToolCount   int                       `json:"tool_count"`
	Statuses    []mcpruntime.ServerStatus `json:"statuses"`
}

type sessionHookConfig struct {
	BeforeTool string `json:"before_tool,omitempty"`
	AfterTool  string `json:"after_tool,omitempty"`
	AfterTurn  string `json:"after_turn,omitempty"`
	AgentEvent string `json:"agent_event,omitempty"`
	EventFile  string `json:"event_file,omitempty"`
	Timeout    string `json:"timeout,omitempty"`
}

type turnStreamEvent struct {
	Type         string           `json:"type"`
	AgentID      string           `json:"agent_id,omitempty"`
	AgentName    string           `json:"agent_name,omitempty"`
	ToolName     string           `json:"tool_name,omitempty"`
	Text         string           `json:"text,omitempty"`
	Result       *streamRunResult `json:"result,omitempty"`
	Session      *sessionSummary  `json:"session,omitempty"`
	Error        string           `json:"error,omitempty"`
	ErrorCode    string           `json:"error_code,omitempty"`
	ErrorRetryOK bool             `json:"retryable,omitempty"`
}

type streamRunResult struct {
	FinalText string           `json:"final_text"`
	Usage     map[string]int   `json:"usage"`
	Messages  []engine.Message `json:"messages"`
}

type eventSubscriber struct {
	ch chan engine.Event
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func New(cfg config.Config, out, errOut io.Writer, providerFactory ProviderFactory) *Server {
	if out == nil {
		out = io.Discard
	}
	if errOut == nil {
		errOut = io.Discard
	}
	if providerFactory == nil {
		providerFactory = func(cfg config.Config) engine.Provider {
			return provider.New(cfg)
		}
	}
	auditFile := strings.TrimSpace(cfg.DaemonAuditFile)
	if auditFile == "" && strings.TrimSpace(cfg.DaemonDir) != "" {
		auditFile = filepath.Join(cfg.DaemonDir, "audit.jsonl")
	}
	return &Server{
		config:          cfg,
		providerFactory: providerFactory,
		out:             out,
		errOut:          errOut,
		logger:          diag.New(errOut, cfg.LogLevel),
		audit:           newAuditLogger(auditFile),
		access:          newDaemonAccessPolicy(cfg),
		rateLimiter:     newRequestRateLimiter(cfg.DaemonRateLimitPerMinute, cfg.DaemonRateLimitBurst),
		sessions:        make(map[string]*managedSession),
	}
}

func (s *Server) Run(ctx context.Context) error {
	httpServer := &http.Server{
		Addr:              s.config.DaemonListenAddr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	s.logger.Infof("daemon listening on http://%s", s.config.DaemonListenAddr)
	err := httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return s.Close()
	}
	if closeErr := s.Close(); closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return err
}

func (s *Server) Close() error {
	s.mu.Lock()
	sessions := make([]*managedSession, 0, len(s.sessions))
	for id, session := range s.sessions {
		sessions = append(sessions, session)
		delete(s.sessions, id)
	}
	s.mu.Unlock()

	var errs []error
	for _, session := range sessions {
		if err := session.close(); err != nil {
			errs = append(errs, err)
		}
	}
	closeErr := errors.Join(errs...)
	if closeErr != nil {
		s.logger.Errorf("daemon shutdown finished with error: %v", closeErr)
	} else {
		s.logger.Infof("daemon shutdown complete (%d sessions)", len(sessions))
	}
	return closeErr
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/audit", s.handleAudit)
	mux.HandleFunc("/v1/sessions", s.handleSessions)
	mux.HandleFunc("/v1/sessions/", s.handleSessionRoutes)
	return s.withDiagnostics(mux)
}

func (s *Server) withDiagnostics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := strings.TrimSpace(r.Header.Get(diag.TraceHeader))
		if traceID == "" {
			traceID = diag.NewTraceID()
		}
		w.Header().Set(diag.TraceHeader, traceID)
		remoteAddr := clientAddress(r)
		meta := requestMeta{
			TraceID:    traceID,
			RemoteAddr: remoteAddr,
		}
		r = r.WithContext(withRequestMeta(r.Context(), meta))

		if strings.HasPrefix(r.URL.Path, "/v1/") {
			if allowed, retryAfter := s.allowRequest(r); !allowed {
				seconds := int(retryAfter.Round(time.Second) / time.Second)
				if seconds <= 0 {
					seconds = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(seconds))
				err := fmt.Errorf("rate limit exceeded for %s", remoteAddr)
				s.auditLog(r.Context(), AuditEvent{
					Action:            "rate_limit",
					Outcome:           "denied",
					Code:              "rate_limited",
					Message:           err.Error(),
					RetryAfterSeconds: seconds,
					Method:            r.Method,
					Path:              r.URL.Path,
				})
				writeError(w, http.StatusTooManyRequests, err)
				return
			}
		}

		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		mode, sessionID := classifyDaemonRoute(r.URL.Path, r.Method)
		s.auditLog(r.Context(), AuditEvent{
			Action:     "request",
			Mode:       mode,
			Method:     r.Method,
			Path:       r.URL.Path,
			SessionID:  sessionID,
			StatusCode: recorder.status,
			Outcome:    auditOutcomeForStatus(recorder.status),
		})

		s.logger.Debugf(
			"trace=%s method=%s path=%s status=%d duration=%s remote=%s",
			traceID,
			r.Method,
			r.URL.Path,
			recorder.status,
			time.Since(started).Round(time.Millisecond),
			r.RemoteAddr,
		)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
	})
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	if !s.requireAccess(w, r, daemonModeAudit, "") {
		return
	}
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	limit, err := parseAuditLimit(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	events, err := s.listAudit(limit, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !s.requireAccess(w, r, daemonModeSessionsRead, "") {
			return
		}
		summaries, err := s.listSessions()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": summaries})
	case http.MethodPost:
		var req createSessionRequest
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !s.requireAccess(w, r, daemonModeSessionsWrite, strings.TrimSpace(req.SessionID)) {
			return
		}
		session, err := s.openSession(r.Context(), strings.TrimSpace(req.SessionID), req.Resume)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, os.ErrNotExist) {
				status = http.StatusNotFound
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"session": session.summary()})
	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) handleSessionRoutes(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/sessions/")
	path = strings.Trim(path, "/")
	if path == "" {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(path, "/")
	sessionID := parts[0]
	mode := daemonModeSessionsRead
	switch {
	case len(parts) == 1:
		mode = daemonModeSessionsRead
	case parts[1] == "messages":
		mode = daemonModeSessionsRead
	case parts[1] == "turns":
		mode = daemonModeTurns
	case parts[1] == "save":
		mode = daemonModeSave
	case parts[1] == "policy", parts[1] == "hooks":
		mode = daemonModeIntrospection
	case parts[1] == "mcp":
		mode = daemonModeMCP
	case parts[1] == "agents":
		mode = daemonModeAgents
	case parts[1] == "workflows":
		mode = daemonModeWorkflows
	case parts[1] == "audit":
		mode = daemonModeAudit
	}
	if !s.requireAccess(w, r, mode, sessionID) {
		return
	}

	session, err := s.getSession(r.Context(), sessionID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}

	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"session": session.summary()})
		return
	}

	switch parts[1] {
	case "messages":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		limit := 0
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid limit: %s", raw))
				return
			}
			limit = value
		}
		messages := session.messages(limit)
		writeJSON(w, http.StatusOK, map[string]any{"messages": messages})
	case "turns":
		s.handleTurnRoutes(w, r, session, parts[2:])
	case "save":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		if err := session.save(); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"session": session.summary()})
	case "policy":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"policy": session.runner.PermissionPolicy()})
	case "hooks":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"hooks": session.hookConfig()})
	case "mcp":
		s.handleMCPRoutes(w, r, session, parts[2:])
	case "agents":
		s.handleAgentRoutes(w, r, session, parts[2:])
	case "workflows":
		s.handleWorkflowRoutes(w, r, session, parts[2:])
	case "audit":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		limit, err := parseAuditLimit(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		events, err := s.listAudit(limit, sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": events})
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleAgentRoutes(w http.ResponseWriter, r *http.Request, session *managedSession, parts []string) {
	if len(parts) == 0 {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"agents": session.runner.ListAgents()})
		return
	}
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}

	agentID := parts[0]
	switch parts[1] {
	case "send":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		var req sendAgentRequest
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		snapshot, err := session.sendAgent(r.Context(), agentID, req.Prompt, req.Background)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"agent": snapshot})
	case "cancel":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		var req cancelAgentRequest
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		snapshot, err := session.cancelAgent(r.Context(), agentID, req.Recursive)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"agent": snapshot})
	case "wait":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		var req waitRequest
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		waitCtx := r.Context()
		if req.TimeoutSeconds > 0 {
			var cancel context.CancelFunc
			waitCtx, cancel = context.WithTimeout(waitCtx, time.Duration(req.TimeoutSeconds)*time.Second)
			defer cancel()
		}
		snapshot, err := session.waitAgent(waitCtx, agentID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"agent": snapshot})
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleTurnRoutes(w http.ResponseWriter, r *http.Request, session *managedSession, parts []string) {
	if len(parts) == 0 {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		var req turnRequest
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		runCtx := r.Context()
		if req.TimeoutSeconds > 0 {
			var cancel context.CancelFunc
			runCtx, cancel = context.WithTimeout(runCtx, time.Duration(req.TimeoutSeconds)*time.Second)
			defer cancel()
		}
		result, err := session.runTurn(runCtx, req.Prompt)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"result":  toStreamRunResult(result),
			"session": session.summary(),
		})
		return
	}
	if len(parts) == 1 && parts[0] == "stream" {
		s.handleTurnStream(w, r, session)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handleTurnStream(w http.ResponseWriter, r *http.Request, session *managedSession) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming is not supported by this response writer"))
		return
	}

	var req turnRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	subscription := session.subscribeEvents()
	defer subscription.close()

	type runOutcome struct {
		result engine.RunResult
		err    error
	}
	resultCh := make(chan runOutcome, 1)
	runCtx := r.Context()
	if req.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(runCtx, time.Duration(req.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	go func() {
		result, err := session.runTurn(runCtx, req.Prompt)
		resultCh <- runOutcome{result: result, err: err}
	}()

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case event, ok := <-subscription.events:
			if !ok {
				return
			}
			if err := writeSSE(w, "event", turnStreamEvent{
				Type:      string(event.Kind),
				AgentID:   event.AgentID,
				AgentName: event.AgentName,
				ToolName:  event.ToolName,
				Text:      event.Text,
			}); err != nil {
				return
			}
			flusher.Flush()
		case outcome := <-resultCh:
			if outcome.err != nil {
				meta := errorMetaForStatus(http.StatusInternalServerError, outcome.err)
				_ = writeSSE(w, "error", turnStreamEvent{
					Type:         "error",
					Error:        outcome.err.Error(),
					ErrorCode:    meta.Code,
					ErrorRetryOK: meta.Retryable,
				})
				flusher.Flush()
				return
			}
			_ = writeSSE(w, "result", turnStreamEvent{
				Type:    "result",
				Result:  toStreamRunResult(outcome.result),
				Session: ptr(session.summary()),
			})
			flusher.Flush()
			return
		case <-keepAlive.C:
			if err := writeSSEComment(w, "keep-alive"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleWorkflowRoutes(w http.ResponseWriter, r *http.Request, session *managedSession, parts []string) {
	if len(parts) == 0 {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"workflows": session.workflowManager.ListWorkflows()})
		return
	}
	workflowID := parts[0]
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		workflow, ok := session.workflowManager.GetWorkflow(workflowID)
		if !ok {
			writeError(w, http.StatusNotFound, fmt.Errorf("workflow not found: %s", workflowID))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"workflow": workflow})
		return
	}
	if len(parts) == 2 && parts[1] == "tasks" {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		tasks, err := session.workflowManager.ListWorkflowTasks(workflowID, strings.TrimSpace(r.URL.Query().Get("status")), strings.TrimSpace(r.URL.Query().Get("name")))
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				writeError(w, http.StatusNotFound, fmt.Errorf("workflow not found: %s", workflowID))
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
		return
	}
	if len(parts) == 2 && parts[1] == "resume" {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		var req workflowResumeRequest
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		runCtx := r.Context()
		if req.TimeoutSeconds > 0 {
			var cancel context.CancelFunc
			runCtx, cancel = context.WithTimeout(runCtx, time.Duration(req.TimeoutSeconds)*time.Second)
			defer cancel()
		}
		workflow, tasks, agents, err := session.resumeWorkflow(runCtx, workflowID, tools.ResumeWorkflowOptions{
			TimeoutSeconds: req.TimeoutSeconds,
			OnlyFailed:     req.OnlyFailed,
			TaskNames:      append([]string(nil), req.TaskNames...),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"workflow": workflow,
			"tasks":    tasks,
			"agents":   agents,
		})
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handleMCPRoutes(w http.ResponseWriter, r *http.Request, session *managedSession, parts []string) {
	if len(parts) == 0 {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"mcp": session.mcpSummary()})
		return
	}

	switch parts[0] {
	case "health":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		if session.mcpManager == nil {
			writeError(w, http.StatusBadRequest, errors.New("MCP is not configured"))
			return
		}
		serverName := strings.TrimSpace(r.URL.Query().Get("server"))
		health, err := session.mcpHealth(r.Context(), serverName)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"statuses": health})
	case "reload":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		summary, err := session.reloadMCP(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"mcp": summary})
	case "validate":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		var req mcpValidateRequest
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		report := session.validateMCP(strings.TrimSpace(req.ConfigFile))
		writeJSON(w, http.StatusOK, map[string]any{"validation": report})
	case "resources":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		if session.mcpManager == nil {
			writeError(w, http.StatusBadRequest, errors.New("MCP is not configured"))
			return
		}
		serverName := strings.TrimSpace(r.URL.Query().Get("server"))
		resources, err := session.mcpManager.ListResources(r.Context(), serverName)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"resources": resources})
	case "resource-templates":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		if session.mcpManager == nil {
			writeError(w, http.StatusBadRequest, errors.New("MCP is not configured"))
			return
		}
		serverName := strings.TrimSpace(r.URL.Query().Get("server"))
		templates, err := session.mcpManager.ListResourceTemplates(r.Context(), serverName)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"resource_templates": templates})
	case "prompts":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		if session.mcpManager == nil {
			writeError(w, http.StatusBadRequest, errors.New("MCP is not configured"))
			return
		}
		serverName := strings.TrimSpace(r.URL.Query().Get("server"))
		prompts, err := session.mcpManager.ListPrompts(r.Context(), serverName)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"prompts": prompts})
	case "read-resource":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		if session.mcpManager == nil {
			writeError(w, http.StatusBadRequest, errors.New("MCP is not configured"))
			return
		}
		var req mcpReadResourceRequest
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		resource, err := session.mcpManager.ReadResource(r.Context(), strings.TrimSpace(req.Server), strings.TrimSpace(req.URI))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"resource": resource})
	case "get-prompt":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		if session.mcpManager == nil {
			writeError(w, http.StatusBadRequest, errors.New("MCP is not configured"))
			return
		}
		var req mcpGetPromptRequest
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		prompt, err := session.mcpManager.GetPrompt(r.Context(), strings.TrimSpace(req.Server), strings.TrimSpace(req.Name), req.Arguments)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"prompt": prompt})
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) openSession(ctx context.Context, id string, resume bool) (*managedSession, error) {
	if strings.TrimSpace(id) == "" {
		var err error
		id, err = newSessionID()
		if err != nil {
			return nil, err
		}
	}
	file := s.sessionPath(id)
	if resume {
		if _, err := os.Stat(file); err != nil {
			return nil, err
		}
	}
	if !resume {
		if _, err := os.Stat(file); err == nil {
			return nil, fmt.Errorf("session already exists: %s", id)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}

	s.mu.Lock()
	if existing, ok := s.sessions[id]; ok {
		s.mu.Unlock()
		return existing, nil
	}
	s.mu.Unlock()

	session, err := s.newManagedSession(ctx, id, file, resume)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.sessions[id]; ok {
		_ = session.close()
		return existing, nil
	}
	s.sessions[id] = session
	s.logger.Debugf("opened session id=%s file=%s resume=%t", id, file, resume)
	return session, nil
}

func (s *Server) getSession(ctx context.Context, id string) (*managedSession, error) {
	s.mu.Lock()
	session, ok := s.sessions[id]
	s.mu.Unlock()
	if ok {
		return session, nil
	}
	return s.openSession(ctx, id, true)
}

func (s *Server) listSessions() ([]sessionSummary, error) {
	s.mu.Lock()
	loaded := make(map[string]*managedSession, len(s.sessions))
	for id, session := range s.sessions {
		loaded[id] = session
	}
	s.mu.Unlock()

	entries, err := os.ReadDir(s.sessionsDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	byID := make(map[string]sessionSummary)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		state, err := persist.Load(filepath.Join(s.sessionsDir(), entry.Name()))
		if err != nil {
			continue
		}
		byID[id] = sessionSummary{
			ID:            id,
			SessionFile:   filepath.Join(s.sessionsDir(), entry.Name()),
			WorkingDir:    s.config.WorkingDir,
			Loaded:        false,
			MessageCount:  len(state.Main),
			ApproxTokens:  engine.EstimateTokens(state.Main),
			AgentCount:    len(state.Agents),
			WorkflowCount: len(state.Workflows),
			SavedAt:       state.SavedAt,
		}
	}
	for id, session := range loaded {
		byID[id] = session.summary()
	}

	summaries := make([]sessionSummary, 0, len(byID))
	for _, summary := range byID {
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].Loaded != summaries[j].Loaded {
			return summaries[i].Loaded
		}
		if summaries[i].SavedAt.Equal(summaries[j].SavedAt) {
			return summaries[i].ID < summaries[j].ID
		}
		return summaries[i].SavedAt.After(summaries[j].SavedAt)
	})
	return summaries, nil
}

func (s *Server) newManagedSession(ctx context.Context, id, file string, resume bool) (*managedSession, error) {
	cfg := s.config
	cfg.SessionFile = file
	cfg.Resume = resume
	cfg.Print = false
	cfg.Prompt = ""

	ms := &managedSession{
		id:          id,
		config:      cfg,
		session:     engine.NewSession(),
		audit:       s.audit,
		out:         s.out,
		errOut:      s.errOut,
		sessionFile: file,
		loadedAt:    time.Now().UTC(),
		subs:        make(map[int]*eventSubscriber),
	}
	ms.workflowManager = tools.NewWorkflowManager()
	ms.workflowManager.SetArtifactRoot(filepath.Join(cfg.WorkingDir, ".xxx-code", "artifacts", "workflows"))
	ms.registry = engine.NewRegistry(
		&tools.BashTool{},
		&tools.ReadFileTool{},
		&tools.WriteFileTool{},
		&tools.EditFileTool{},
		&tools.GlobTool{},
		&tools.GrepTool{},
		&tools.AgentSpawnTool{},
		&tools.AgentFanoutTool{Manager: ms.workflowManager},
		&tools.AgentSendTool{},
		&tools.AgentCancelTool{},
		&tools.AgentWaitTool{},
		&tools.AgentListTool{},
		&tools.WorkflowListTool{Manager: ms.workflowManager},
		&tools.WorkflowGetTool{Manager: ms.workflowManager},
		&tools.WorkflowTasksTool{Manager: ms.workflowManager},
		&tools.WorkflowResumeTool{Manager: ms.workflowManager},
	)

	ms.runner = engine.NewRunner(s.providerFactory(cfg), ms.registry, engine.RunnerConfig{
		Model:               cfg.Model,
		SystemPrompt:        cfg.SystemPrompt,
		MaxTokens:           cfg.MaxTokens,
		MaxTurns:            cfg.MaxTurns,
		StreamResponses:     true,
		ContextBudget:       cfg.ContextBudget,
		CompactKeepMessages: cfg.CompactKeep,
		WorkingDir:          cfg.WorkingDir,
		ToolTimeout:         cfg.ToolTimeout,
		HookTimeout:         cfg.HookTimeout,
		MaxAgentDepth:       3,
		MaxParallelAgents:   cfg.MaxParallelAgents,
		PermissionPolicy: engine.PermissionPolicy{
			ReadRoots:           cfg.ReadRoots,
			WriteRoots:          cfg.WriteRoots,
			AllowedTools:        cfg.AllowedTools,
			BlockedTools:        cfg.BlockedTools,
			BashAllowedPrefixes: cfg.BashAllowPrefixes,
			BashBlockedPrefixes: cfg.BashDenyPrefixes,
			ReadOnly:            cfg.ReadOnly,
			BashEnabled:         cfg.BashEnabled,
		},
		Hooks: hooks.NewBus(hooks.Config{
			BeforeTool: cfg.HookBeforeTool,
			AfterTool:  cfg.HookAfterTool,
			AfterTurn:  cfg.HookAfterTurn,
			AgentEvent: cfg.HookAgentEvent,
			EventFile:  cfg.HookEventFile,
		}),
		EventHandler: ms.handleEvent,
	})

	ms.workflowManager.SetOnChange(func() {
		if err := ms.save(); err != nil {
			fmt.Fprintf(ms.errOut, "daemon autosave error: %v\n", err)
		}
	})

	manager, err := mcpruntime.Start(ctx, ms.registry, mcpruntime.Options{
		WorkingDir: cfg.WorkingDir,
		ConfigFile: cfg.MCPConfigFile,
	})
	if err != nil {
		return nil, err
	}
	ms.mcpManager = manager

	if resume {
		if err := ms.resume(); err != nil {
			_ = ms.close()
			return nil, err
		}
	}

	if err := ms.save(); err != nil {
		_ = ms.close()
		return nil, err
	}
	s.logger.Debugf("initialized managed session id=%s session_file=%s", id, file)
	return ms, nil
}

func (s *Server) sessionsDir() string {
	return filepath.Join(s.config.DaemonDir, "sessions")
}

func (s *Server) sessionPath(id string) string {
	return filepath.Join(s.sessionsDir(), id+".json")
}

func (m *managedSession) summary() sessionSummary {
	state, _ := os.Stat(m.sessionFile)
	summary := sessionSummary{
		ID:            m.id,
		SessionFile:   m.sessionFile,
		WorkingDir:    m.config.WorkingDir,
		Loaded:        true,
		LoadedAt:      m.loadedAt,
		MessageCount:  len(m.session.Snapshot()),
		ApproxTokens:  engine.EstimateTokens(m.session.Snapshot()),
		AgentCount:    len(m.runner.ListAgents()),
		WorkflowCount: len(m.workflowManager.ListWorkflows()),
	}
	if state != nil {
		summary.SavedAt = state.ModTime().UTC()
	}
	return summary
}

func (m *managedSession) mcpSummary() sessionMCPSummary {
	summary := sessionMCPSummary{
		Statuses: []mcpruntime.ServerStatus{},
	}
	if m == nil || m.mcpManager == nil {
		return summary
	}
	summary.ConfigPath = m.mcpManager.ConfigPath()
	summary.ServerCount = m.mcpManager.ServerCount()
	summary.ToolCount = m.mcpManager.ToolCount()
	summary.Statuses = m.mcpManager.Statuses()
	return summary
}

func (m *managedSession) hookConfig() sessionHookConfig {
	if m == nil {
		return sessionHookConfig{}
	}
	return sessionHookConfig{
		BeforeTool: m.config.HookBeforeTool,
		AfterTool:  m.config.HookAfterTool,
		AfterTurn:  m.config.HookAfterTurn,
		AgentEvent: m.config.HookAgentEvent,
		EventFile:  m.config.HookEventFile,
		Timeout:    m.config.HookTimeout.String(),
	}
}

func (m *managedSession) mcpHealth(ctx context.Context, serverName string) ([]mcpruntime.ServerStatus, error) {
	if m == nil || m.mcpManager == nil {
		return nil, errors.New("MCP is not configured")
	}
	return m.mcpManager.Health(ctx, serverName)
}

func (m *managedSession) reloadMCP(ctx context.Context) (sessionMCPSummary, error) {
	m.runMu.Lock()
	defer m.runMu.Unlock()

	if m.mcpManager == nil {
		manager, err := mcpruntime.Start(ctx, m.registry, mcpruntime.Options{
			WorkingDir: m.config.WorkingDir,
			ConfigFile: m.config.MCPConfigFile,
		})
		if err != nil {
			return sessionMCPSummary{}, err
		}
		m.mcpManager = manager
	} else if err := m.mcpManager.Reload(ctx); err != nil {
		return sessionMCPSummary{}, err
	}

	if err := m.save(); err != nil {
		return sessionMCPSummary{}, err
	}
	return m.mcpSummary(), nil
}

func (m *managedSession) validateMCP(configFile string) mcpruntime.ValidationReport {
	if m == nil {
		return mcpruntime.ValidationReport{}
	}
	options := mcpruntime.Options{
		WorkingDir: m.config.WorkingDir,
		ConfigFile: m.config.MCPConfigFile,
	}
	if strings.TrimSpace(configFile) != "" {
		options.ConfigFile = configFile
	}
	return mcpruntime.ValidateOptions(options)
}

func (m *managedSession) messages(limit int) []engine.Message {
	messages := m.session.Snapshot()
	if limit > 0 && len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}
	return messages
}

func (m *managedSession) runTurn(ctx context.Context, prompt string) (engine.RunResult, error) {
	m.runMu.Lock()
	defer m.runMu.Unlock()

	runCtx, release, err := m.beginTurn(ctx)
	if err != nil {
		return engine.RunResult{}, err
	}
	defer release()

	result, err := m.runner.RunTurn(runCtx, m.session, prompt)
	if err != nil {
		return result, err
	}
	return result, m.save()
}

func (m *managedSession) sendAgent(ctx context.Context, agentID, prompt string, background bool) (engine.AgentSnapshot, error) {
	m.runMu.Lock()
	defer m.runMu.Unlock()

	snapshot, err := m.runner.SendAgent(ctx, agentID, prompt, background)
	if err != nil {
		return engine.AgentSnapshot{}, err
	}
	return snapshot, m.save()
}

func (m *managedSession) cancelAgent(ctx context.Context, agentID string, recursive bool) (engine.AgentSnapshot, error) {
	m.runMu.Lock()
	defer m.runMu.Unlock()

	snapshot, err := m.runner.CancelAgent(ctx, agentID, recursive)
	if err != nil {
		return engine.AgentSnapshot{}, err
	}
	return snapshot, m.save()
}

func (m *managedSession) waitAgent(ctx context.Context, agentID string) (engine.AgentSnapshot, error) {
	snapshot, err := m.runner.WaitAgent(ctx, agentID)
	if err != nil {
		return engine.AgentSnapshot{}, err
	}
	return snapshot, m.save()
}

func (m *managedSession) resumeWorkflow(ctx context.Context, workflowID string, options tools.ResumeWorkflowOptions) (tools.WorkflowSnapshot, []tools.FanoutTaskResultAlias, []engine.AgentSnapshot, error) {
	m.runMu.Lock()
	defer m.runMu.Unlock()

	workflow, tasks, agents, err := m.workflowManager.ResumeWorkflow(ctx, workflowID, &engine.ExecutionContext{
		Runner:     m.runner,
		Session:    m.session,
		WorkingDir: m.config.WorkingDir,
	}, options)
	if err != nil {
		return tools.WorkflowSnapshot{}, nil, nil, err
	}
	if saveErr := m.save(); saveErr != nil {
		return tools.WorkflowSnapshot{}, nil, nil, saveErr
	}
	return workflow, tasks, agents, nil
}

func (m *managedSession) resume() error {
	state, err := persist.Load(m.sessionFile)
	if err != nil {
		return fmt.Errorf("resume session: %w", err)
	}
	m.session.Replace(state.Main)
	if err := m.runner.ImportAgents(state.Agents); err != nil {
		return fmt.Errorf("resume agents: %w", err)
	}
	if err := m.workflowManager.ImportWorkflows(state.Workflows); err != nil {
		return fmt.Errorf("resume workflows: %w", err)
	}
	return nil
}

func (m *managedSession) save() error {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	return persist.Save(m.sessionFile, m.session, m.runner, m.workflowManager)
}

func (m *managedSession) close() error {
	cancel := m.markClosed()
	if cancel != nil {
		cancel()
	}
	m.closeSubscriptions()

	m.runMu.Lock()
	defer m.runMu.Unlock()

	var errs []error
	for _, agent := range m.runner.ListAgents() {
		if agent.Status != engine.AgentRunning && agent.Status != engine.AgentQueued {
			continue
		}
		if _, err := m.runner.CancelAgent(context.Background(), agent.ID, true); err != nil {
			errs = append(errs, err)
		}
	}
	if err := m.save(); err != nil {
		errs = append(errs, err)
	}
	if m.mcpManager != nil {
		if err := m.mcpManager.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *managedSession) handleEvent(event engine.Event) {
	m.publishEvent(event)
	m.auditEvent(event)
	switch event.Kind {
	case engine.EventAgentSpawned, engine.EventAgentCompleted, engine.EventAgentCancelled:
		if err := m.save(); err != nil {
			fmt.Fprintf(m.errOut, "daemon autosave error: %v\n", err)
		}
	}
}

type eventSubscription struct {
	events <-chan engine.Event
	close  func()
}

func (m *managedSession) subscribeEvents() eventSubscription {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	if m.isClosed() {
		ch := make(chan engine.Event)
		close(ch)
		return eventSubscription{
			events: ch,
			close:  func() {},
		}
	}
	ch := make(chan engine.Event, 512)
	id := m.subSeq
	m.subSeq++
	m.subs[id] = &eventSubscriber{ch: ch}
	return eventSubscription{
		events: ch,
		close: func() {
			m.unsubscribeEvent(id)
		},
	}
}

func (m *managedSession) publishEvent(event engine.Event) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for _, subscriber := range m.subs {
		select {
		case subscriber.ch <- event:
		default:
		}
	}
}

func (m *managedSession) auditEvent(event engine.Event) {
	if m == nil || m.audit == nil {
		return
	}
	record := AuditEvent{
		Timestamp: time.Now().UTC(),
		SessionID: m.id,
		Action:    string(event.Kind),
		AgentID:   strings.TrimSpace(event.AgentID),
		AgentName: strings.TrimSpace(event.AgentName),
		ToolName:  strings.TrimSpace(event.ToolName),
		Message:   strings.TrimSpace(event.Text),
		Outcome:   "ok",
	}
	switch event.Kind {
	case engine.EventToolResult:
		if strings.Contains(strings.ToLower(record.Message), "by policy") {
			record.Outcome = "denied"
			record.Code = "policy_block"
		}
	case engine.EventHookError:
		record.Outcome = "error"
		record.Code = "hook_error"
	case engine.EventAgentCancelled:
		record.Outcome = "cancelled"
	case engine.EventSessionCompacted:
		record.Outcome = "compacted"
	}
	if err := m.audit.Log(record); err != nil {
		fmt.Fprintf(m.errOut, "daemon audit error: %v\n", err)
	}
}

func (s *Server) allowRequest(r *http.Request) (bool, time.Duration) {
	if s == nil || s.rateLimiter == nil {
		return true, 0
	}
	return s.rateLimiter.Allow(clientAddress(r), time.Now().UTC())
}

func (s *Server) requireAccess(w http.ResponseWriter, r *http.Request, mode, sessionID string) bool {
	if s == nil {
		return true
	}
	if err := s.access.Allow(mode, sessionID); err != nil {
		s.auditLog(r.Context(), AuditEvent{
			Action:    "acl",
			Mode:      mode,
			SessionID: strings.TrimSpace(sessionID),
			Outcome:   "denied",
			Code:      "forbidden",
			Message:   err.Error(),
			Method:    r.Method,
			Path:      r.URL.Path,
		})
		writeError(w, http.StatusForbidden, err)
		return false
	}
	return true
}

func (s *Server) listAudit(limit int, sessionID string) ([]AuditEvent, error) {
	if s == nil || s.audit == nil {
		return nil, nil
	}
	return s.audit.List(limit, sessionID)
}

func (s *Server) auditLog(ctx context.Context, event AuditEvent) {
	if s == nil || s.audit == nil {
		return
	}
	meta := requestMetaFromContext(ctx)
	if strings.TrimSpace(event.TraceID) == "" {
		event.TraceID = meta.TraceID
	}
	if strings.TrimSpace(event.RemoteAddr) == "" {
		event.RemoteAddr = meta.RemoteAddr
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if err := s.audit.Log(event); err != nil {
		s.logger.Errorf("audit write failed: %v", err)
	}
}

func parseAuditLimit(r *http.Request) (int, error) {
	if r == nil {
		return 50, nil
	}
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return 50, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("invalid limit: %s", raw)
	}
	if value > 1000 {
		value = 1000
	}
	return value, nil
}

func auditOutcomeForStatus(status int) string {
	switch {
	case status >= 500:
		return "error"
	case status >= 400:
		return "denied"
	default:
		return "ok"
	}
}

func toStreamRunResult(result engine.RunResult) *streamRunResult {
	return &streamRunResult{
		FinalText: result.FinalText,
		Usage: map[string]int{
			"input_tokens":  result.Usage.InputTokens,
			"output_tokens": result.Usage.OutputTokens,
		},
		Messages: result.Messages,
	}
}

func decodeBody(r *http.Request, target any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	return json.Unmarshal(data, target)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeSSE(w io.Writer, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	return err
}

func writeSSEComment(w io.Writer, comment string) error {
	_, err := fmt.Fprintf(w, ": %s\n\n", strings.TrimSpace(comment))
	return err
}

func writeError(w http.ResponseWriter, status int, err error) {
	meta := errorMetaForStatus(status, err)
	status = normalizeErrorStatus(status, err)
	writeJSON(w, status, map[string]any{
		"error":     meta.Message,
		"code":      meta.Code,
		"retryable": meta.Retryable,
	})
}

func writeMethodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	tokens, err := auth.CurrentTokens(s.config.DaemonToken, s.config.DaemonTokenFile)
	if err != nil {
		s.auditLog(r.Context(), AuditEvent{
			Action:  "auth",
			Outcome: "error",
			Code:    "token_source_error",
			Message: err.Error(),
			Method:  r.Method,
			Path:    r.URL.Path,
		})
		writeError(w, http.StatusInternalServerError, err)
		return false
	}
	if len(tokens) == 0 {
		return true
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	parts := strings.Fields(auth)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		for _, token := range tokens {
			if subtle.ConstantTimeCompare([]byte(parts[1]), []byte(token)) == 1 {
				return true
			}
		}
	}
	s.auditLog(r.Context(), AuditEvent{
		Action:  "auth",
		Outcome: "denied",
		Code:    "unauthorized",
		Message: "unauthorized",
		Method:  r.Method,
		Path:    r.URL.Path,
	})
	w.Header().Set("WWW-Authenticate", `Bearer realm="xxx-code"`)
	writeError(w, http.StatusUnauthorized, errors.New("unauthorized"))
	return false
}

type apiErrorMeta struct {
	Message   string
	Code      string
	Retryable bool
}

func errorMetaForStatus(status int, err error) apiErrorMeta {
	if err == nil {
		err = errors.New(http.StatusText(status))
	}
	status = normalizeErrorStatus(status, err)
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = http.StatusText(status)
	}

	meta := apiErrorMeta{
		Message: message,
		Code:    "internal_error",
	}

	switch {
	case status == http.StatusConflict && strings.HasPrefix(message, "session already exists:"):
		meta.Code = "session_exists"
	case status == http.StatusConflict && strings.Contains(message, "already in progress"):
		meta.Code = "agent_in_progress"
	case status == http.StatusBadRequest && strings.EqualFold(message, "MCP is not configured"):
		meta.Code = "mcp_not_configured"
	case status == http.StatusNotFound && errors.Is(err, os.ErrNotExist):
		meta.Code = "session_not_found"
	case status == http.StatusNotFound && strings.HasPrefix(message, "workflow not found:"):
		meta.Code = "workflow_not_found"
	case status == http.StatusNotFound && strings.HasPrefix(message, "agent not found"):
		meta.Code = "agent_not_found"
	case status == http.StatusUnauthorized:
		meta.Code = "unauthorized"
	case status == http.StatusForbidden:
		meta.Code = "forbidden"
	case status == http.StatusMethodNotAllowed:
		meta.Code = "method_not_allowed"
	case status == http.StatusTooManyRequests:
		meta.Code = "rate_limited"
		meta.Retryable = true
	case status == http.StatusConflict:
		meta.Code = "conflict"
	case status == http.StatusRequestTimeout:
		meta.Code = "timeout"
		meta.Retryable = true
	case errors.Is(err, context.Canceled):
		meta.Code = "cancelled"
		meta.Retryable = true
	case status == http.StatusBadRequest && isInvalidJSONError(err):
		meta.Code = "invalid_json"
	case status == http.StatusBadRequest:
		meta.Code = "invalid_request"
	}

	return meta
}

func normalizeErrorStatus(status int, err error) int {
	if status != http.StatusInternalServerError || err == nil {
		return status
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusRequestTimeout
	case errors.Is(err, os.ErrNotExist):
		return http.StatusNotFound
	}

	message := strings.TrimSpace(err.Error())
	switch {
	case strings.HasPrefix(message, "session already exists:"):
		return http.StatusConflict
	case strings.HasPrefix(message, "workflow not found:"),
		strings.HasPrefix(message, "agent not found"):
		return http.StatusNotFound
	case strings.Contains(message, "blocked by ACL"),
		strings.Contains(message, "not allowed by ACL"):
		return http.StatusForbidden
	case strings.Contains(message, "rate limit exceeded"):
		return http.StatusTooManyRequests
	case strings.Contains(message, "already in progress"):
		return http.StatusConflict
	case strings.EqualFold(message, "MCP is not configured"),
		strings.EqualFold(message, "prompt is empty"),
		strings.EqualFold(message, "maximum agent depth reached"),
		strings.EqualFold(message, "session is closed"):
		return http.StatusBadRequest
	default:
		return status
	}
}

func isInvalidJSONError(err error) bool {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &syntaxErr) || errors.As(err, &typeErr)
}

func (m *managedSession) beginTurn(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)

	m.lifecycleMu.Lock()
	if m.closed {
		m.lifecycleMu.Unlock()
		cancel()
		return nil, nil, errors.New("session is closed")
	}
	m.activeTurnCancel = cancel
	m.lifecycleMu.Unlock()

	release := func() {
		m.lifecycleMu.Lock()
		if m.activeTurnCancel != nil {
			m.activeTurnCancel = nil
		}
		m.lifecycleMu.Unlock()
		cancel()
	}

	return runCtx, release, nil
}

func (m *managedSession) markClosed() context.CancelFunc {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	cancel := m.activeTurnCancel
	m.activeTurnCancel = nil
	return cancel
}

func (m *managedSession) isClosed() bool {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	return m.closed
}

func (m *managedSession) unsubscribeEvent(id int) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	subscriber, ok := m.subs[id]
	if !ok {
		return
	}
	delete(m.subs, id)
	close(subscriber.ch)
}

func (m *managedSession) closeSubscriptions() {
	m.subMu.Lock()
	subscribers := m.subs
	m.subs = make(map[int]*eventSubscriber)
	m.subMu.Unlock()

	for _, subscriber := range subscribers {
		close(subscriber.ch)
	}
}

func newSessionID() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "session_" + hex.EncodeToString(buf), nil
}

func ptr[T any](value T) *T {
	return &value
}
