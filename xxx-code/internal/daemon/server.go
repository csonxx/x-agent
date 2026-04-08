package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caowenhua/x-agent/xxx-code/internal/config"
	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	"github.com/caowenhua/x-agent/xxx-code/internal/hooks"
	mcpruntime "github.com/caowenhua/x-agent/xxx-code/internal/mcp"
	"github.com/caowenhua/x-agent/xxx-code/internal/persist"
	"github.com/caowenhua/x-agent/xxx-code/internal/provider/anthropic"
	"github.com/caowenhua/x-agent/xxx-code/internal/tools"
)

type ProviderFactory func(config.Config) engine.Provider

type Server struct {
	config          config.Config
	providerFactory ProviderFactory
	out             io.Writer
	errOut          io.Writer

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
	out             io.Writer
	errOut          io.Writer
	sessionFile     string
	loadedAt        time.Time

	saveMu sync.Mutex
	runMu  sync.Mutex
	subMu  sync.Mutex
	subSeq int
	subs   map[int]chan engine.Event
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
	Timeout    string `json:"timeout,omitempty"`
}

type turnStreamEvent struct {
	Type      string           `json:"type"`
	AgentID   string           `json:"agent_id,omitempty"`
	AgentName string           `json:"agent_name,omitempty"`
	ToolName  string           `json:"tool_name,omitempty"`
	Text      string           `json:"text,omitempty"`
	Result    *streamRunResult `json:"result,omitempty"`
	Session   *sessionSummary  `json:"session,omitempty"`
	Error     string           `json:"error,omitempty"`
}

type streamRunResult struct {
	FinalText string           `json:"final_text"`
	Usage     map[string]int   `json:"usage"`
	Messages  []engine.Message `json:"messages"`
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
			return anthropic.NewClient(cfg.APIKey, cfg.BaseURL, cfg.Version)
		}
	}
	return &Server{
		config:          cfg,
		providerFactory: providerFactory,
		out:             out,
		errOut:          errOut,
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

	fmt.Fprintf(s.errOut, "xxx-code daemon listening on http://%s\n", s.config.DaemonListenAddr)
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
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	s.mu.Unlock()

	var errs []error
	for _, session := range sessions {
		if err := session.close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/sessions", s.handleSessions)
	mux.HandleFunc("/v1/sessions/", s.handleSessionRoutes)
	return mux
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

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
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
	path := strings.TrimPrefix(r.URL.Path, "/v1/sessions/")
	path = strings.Trim(path, "/")
	if path == "" {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(path, "/")
	sessionID := parts[0]

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
		case event := <-subscription.events:
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
				_ = writeSSE(w, "error", turnStreamEvent{
					Type:  "error",
					Error: outcome.err.Error(),
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
	if len(parts) == 2 && parts[1] == "resume" {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		var req waitRequest
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
		workflow, tasks, agents, err := session.resumeWorkflow(runCtx, workflowID, req.TimeoutSeconds)
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
	if session.mcpManager == nil {
		writeError(w, http.StatusBadRequest, errors.New("MCP is not configured"))
		return
	}

	serverName := strings.TrimSpace(r.URL.Query().Get("server"))

	switch parts[0] {
	case "resources":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
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
		out:         s.out,
		errOut:      s.errOut,
		sessionFile: file,
		loadedAt:    time.Now().UTC(),
		subs:        make(map[int]chan engine.Event),
	}
	ms.workflowManager = tools.NewWorkflowManager()
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
		Hooks: hooks.NewScriptManager(hooks.Config{
			BeforeTool: cfg.HookBeforeTool,
			AfterTool:  cfg.HookAfterTool,
			AfterTurn:  cfg.HookAfterTurn,
			AgentEvent: cfg.HookAgentEvent,
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
		Timeout:    m.config.HookTimeout.String(),
	}
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

	result, err := m.runner.RunTurn(ctx, m.session, prompt)
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

func (m *managedSession) resumeWorkflow(ctx context.Context, workflowID string, timeoutSeconds int) (tools.WorkflowSnapshot, []tools.FanoutTaskResultAlias, []engine.AgentSnapshot, error) {
	m.runMu.Lock()
	defer m.runMu.Unlock()

	workflow, tasks, agents, err := m.workflowManager.ResumeWorkflow(ctx, workflowID, &engine.ExecutionContext{
		Runner:     m.runner,
		Session:    m.session,
		WorkingDir: m.config.WorkingDir,
	}, timeoutSeconds)
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
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	if m.mcpManager != nil {
		return m.mcpManager.Close()
	}
	return nil
}

func (m *managedSession) handleEvent(event engine.Event) {
	m.publishEvent(event)
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
	ch := make(chan engine.Event, 512)
	m.subMu.Lock()
	id := m.subSeq
	m.subSeq++
	m.subs[id] = ch
	m.subMu.Unlock()
	return eventSubscription{
		events: ch,
		close: func() {
			m.subMu.Lock()
			delete(m.subs, id)
			m.subMu.Unlock()
		},
	}
}

func (m *managedSession) publishEvent(event engine.Event) {
	m.subMu.Lock()
	subscribers := make([]chan engine.Event, 0, len(m.subs))
	for _, ch := range m.subs {
		subscribers = append(subscribers, ch)
	}
	m.subMu.Unlock()
	for _, ch := range subscribers {
		ch <- event
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
	writeJSON(w, status, map[string]any{
		"error": err.Error(),
	})
}

func writeMethodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
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
