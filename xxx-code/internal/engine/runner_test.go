package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

type stubProvider struct {
	calls int
}

func (p *stubProvider) CreateMessage(ctx context.Context, request CompletionRequest) (CompletionResponse, error) {
	_ = ctx
	p.calls++
	if p.calls == 1 {
		input, _ := json.Marshal(map[string]any{"value": "done"})
		return CompletionResponse{
			Message: Message{
				Role: RoleAssistant,
				Content: []Block{
					{Type: BlockText, Text: "calling tool"},
					{Type: BlockToolUse, ID: "tool-1", Name: "echo_tool", Input: input},
				},
			},
		}, nil
	}
	return CompletionResponse{
		Message: NewTextMessage(RoleAssistant, "final answer"),
	}, nil
}

type echoTool struct{}

type countingTool struct {
	calls int
}

func (t *echoTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        "echo_tool",
		Description: "Echo input",
		InputSchema: map[string]any{"type": "object"},
	}
}

func (t *echoTool) Call(ctx context.Context, exec *ExecutionContext, input json.RawMessage) (ToolResult, error) {
	_ = ctx
	_ = exec
	return ToolResult{Content: string(input)}, nil
}

func (t *countingTool) Definition() ToolDefinition {
	return (&echoTool{}).Definition()
}

func (t *countingTool) Call(ctx context.Context, exec *ExecutionContext, input json.RawMessage) (ToolResult, error) {
	t.calls++
	return (&echoTool{}).Call(ctx, exec, input)
}

func TestRunnerExecutesToolLoop(t *testing.T) {
	provider := &stubProvider{}
	runner := NewRunner(provider, NewRegistry(&echoTool{}), RunnerConfig{
		Model:        "test-model",
		SystemPrompt: "test",
		MaxTurns:     4,
	})
	session := NewSession()

	result, err := runner.RunTurn(context.Background(), session, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "final answer" {
		t.Fatalf("unexpected final text: %q", result.FinalText)
	}
	if provider.calls != 2 {
		t.Fatalf("expected 2 provider calls, got %d", provider.calls)
	}
}

type promptProvider struct{}

func (p *promptProvider) CreateMessage(ctx context.Context, request CompletionRequest) (CompletionResponse, error) {
	_ = ctx
	return CompletionResponse{
		Message: NewTextMessage(RoleAssistant, "reply:"+latestUserText(request.Messages)),
	}, nil
}

func latestUserText(messages []Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == RoleUser {
			return messages[i].Text()
		}
	}
	return ""
}

func TestRunnerCanReuseSpawnedAgent(t *testing.T) {
	runner := NewRunner(&promptProvider{}, NewRegistry(), RunnerConfig{
		Model:        "test-model",
		SystemPrompt: "test",
		MaxTurns:     4,
	})

	first, err := runner.SpawnAgent(nil, SpawnRequest{
		Name:   "worker",
		Prompt: "first task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != AgentIdle {
		t.Fatalf("expected idle status, got %s", first.Status)
	}
	if first.Result != "reply:first task" {
		t.Fatalf("unexpected first result: %q", first.Result)
	}

	second, err := runner.SendAgent(context.Background(), first.ID, "second task", false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != AgentIdle {
		t.Fatalf("expected idle status after send, got %s", second.Status)
	}
	if second.Result != "reply:second task" {
		t.Fatalf("unexpected second result: %q", second.Result)
	}

	agents := runner.ExportAgents()
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if len(agents[0].Session) != 4 {
		t.Fatalf("expected 4 messages in agent session, got %d", len(agents[0].Session))
	}

	gotTexts := make([]string, 0, len(agents[0].Session))
	for _, message := range agents[0].Session {
		gotTexts = append(gotTexts, message.Text())
	}
	joined := strings.Join(gotTexts, " | ")
	if !strings.Contains(joined, "first task") || !strings.Contains(joined, "second task") {
		t.Fatalf("expected preserved agent history, got %q", joined)
	}
}

type blockingProvider struct {
	once    sync.Once
	started chan struct{}
}

func (p *blockingProvider) CreateMessage(ctx context.Context, request CompletionRequest) (CompletionResponse, error) {
	_ = request
	p.once.Do(func() {
		close(p.started)
	})
	<-ctx.Done()
	return CompletionResponse{}, ctx.Err()
}

func TestRunnerCanCancelAgent(t *testing.T) {
	runner := NewRunner(&blockingProvider{
		started: make(chan struct{}),
	}, NewRegistry(), RunnerConfig{
		Model:        "test-model",
		SystemPrompt: "test",
		MaxTurns:     4,
	})

	snapshot, err := runner.SpawnAgent(nil, SpawnRequest{
		Name:       "worker",
		Prompt:     "long task",
		Background: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	blocking := runner.provider.(*blockingProvider)
	select {
	case <-blocking.started:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not start")
	}

	cancelled, err := runner.CancelAgent(context.Background(), snapshot.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != AgentCancelled {
		t.Fatalf("expected cancelled status, got %s", cancelled.Status)
	}
}

func TestRunnerCanCancelQueuedAgent(t *testing.T) {
	provider := newGatedProvider()
	runner := NewRunner(provider, NewRegistry(), RunnerConfig{
		Model:             "test-model",
		SystemPrompt:      "test",
		MaxTurns:          4,
		MaxParallelAgents: 1,
	})

	slowStarted := provider.channelFor(provider.started, "slow task")
	slowRelease := provider.channelFor(provider.release, "slow task")
	queuedStarted := provider.channelFor(provider.started, "queued task")

	slow, err := runner.SpawnAgent(nil, SpawnRequest{
		Name:       "slow",
		Prompt:     "slow task",
		Background: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("slow agent did not start")
	}

	queued, err := runner.SpawnAgent(nil, SpawnRequest{
		Name:       "queued",
		Prompt:     "queued task",
		Background: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != AgentQueued {
		t.Fatalf("expected queued status, got %s", queued.Status)
	}

	cancelled, err := runner.CancelAgent(context.Background(), queued.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != AgentCancelled {
		t.Fatalf("expected cancelled status, got %s", cancelled.Status)
	}

	close(slowRelease)
	if _, err := runner.WaitAgent(context.Background(), slow.ID); err != nil {
		t.Fatal(err)
	}

	select {
	case <-queuedStarted:
		t.Fatal("queued agent started after it was cancelled")
	case <-time.After(150 * time.Millisecond):
	}
}

func TestRunnerCompactsLargeSession(t *testing.T) {
	runner := NewRunner(&promptProvider{}, NewRegistry(), RunnerConfig{
		Model:               "test-model",
		SystemPrompt:        "test",
		MaxTurns:            4,
		ContextBudget:       20,
		CompactKeepMessages: 2,
	})

	session := NewSession(
		NewTextMessage(RoleUser, strings.Repeat("alpha ", 40)),
		NewTextMessage(RoleAssistant, strings.Repeat("beta ", 40)),
		NewTextMessage(RoleUser, strings.Repeat("gamma ", 30)),
		NewTextMessage(RoleAssistant, strings.Repeat("delta ", 30)),
		NewTextMessage(RoleUser, "recent question"),
		NewTextMessage(RoleAssistant, "recent answer"),
	)

	report, changed := runner.CompactSession(session)
	if !changed {
		t.Fatal("expected session compaction")
	}
	if report.AfterMessages >= report.BeforeMessages {
		t.Fatalf("expected fewer messages after compaction, got %+v", report)
	}

	messages := session.Snapshot()
	if len(messages) != 5 {
		t.Fatalf("expected compacted session length 5, got %d", len(messages))
	}
	if !strings.Contains(messages[0].Text(), "[compacted summary]") {
		t.Fatalf("expected summary marker, got %q", messages[0].Text())
	}
}

type recordingHook struct {
	mu     sync.Mutex
	events []HookEvent
	block  map[HookKind]error
}

func (h *recordingHook) HandleHook(ctx context.Context, event HookEvent) error {
	_ = ctx
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, event)
	if h.block != nil {
		if err, ok := h.block[event.Kind]; ok {
			return err
		}
	}
	return nil
}

func (h *recordingHook) count(kind HookKind) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for _, event := range h.events {
		if event.Kind == kind {
			count++
		}
	}
	return count
}

func TestBeforeToolHookCanBlockTool(t *testing.T) {
	provider := &stubProvider{}
	tool := &countingTool{}
	hook := &recordingHook{
		block: map[HookKind]error{
			HookBeforeTool: context.Canceled,
		},
	}

	runner := NewRunner(provider, NewRegistry(tool), RunnerConfig{
		Model:        "test-model",
		SystemPrompt: "test",
		MaxTurns:     4,
		Hooks:        hook,
	})
	session := NewSession()

	result, err := runner.RunTurn(context.Background(), session, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "final answer" {
		t.Fatalf("unexpected final text: %q", result.FinalText)
	}
	if tool.calls != 0 {
		t.Fatalf("expected blocked tool to avoid execution, got %d calls", tool.calls)
	}
	if hook.count(HookBeforeTool) == 0 {
		t.Fatal("expected before_tool hook to run")
	}
}

func TestAfterTurnHookRuns(t *testing.T) {
	hook := &recordingHook{}
	runner := NewRunner(&promptProvider{}, NewRegistry(), RunnerConfig{
		Model:        "test-model",
		SystemPrompt: "test",
		MaxTurns:     4,
		Hooks:        hook,
	})

	_, err := runner.RunTurn(context.Background(), NewSession(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if hook.count(HookAfterTurn) != 1 {
		t.Fatalf("expected 1 after_turn hook call, got %d", hook.count(HookAfterTurn))
	}
}

type gatedProvider struct {
	mu       sync.Mutex
	started  map[string]chan struct{}
	release  map[string]chan struct{}
	startLog []string
}

func newGatedProvider() *gatedProvider {
	return &gatedProvider{
		started: make(map[string]chan struct{}),
		release: make(map[string]chan struct{}),
	}
}

func (p *gatedProvider) channelFor(m map[string]chan struct{}, prompt string) chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	ch, ok := m[prompt]
	if !ok {
		ch = make(chan struct{})
		m[prompt] = ch
	}
	return ch
}

func (p *gatedProvider) markStarted(prompt string) {
	p.mu.Lock()
	p.startLog = append(p.startLog, prompt)
	ch, ok := p.started[prompt]
	if !ok {
		ch = make(chan struct{})
		p.started[prompt] = ch
	}
	p.mu.Unlock()

	select {
	case <-ch:
	default:
		close(ch)
	}
}

func (p *gatedProvider) CreateMessage(ctx context.Context, request CompletionRequest) (CompletionResponse, error) {
	prompt := latestUserText(request.Messages)
	p.markStarted(prompt)

	release := p.channelFor(p.release, prompt)
	select {
	case <-release:
	case <-ctx.Done():
		return CompletionResponse{}, ctx.Err()
	}

	return CompletionResponse{
		Message: NewTextMessage(RoleAssistant, "reply:"+prompt),
	}, nil
}

func TestRunnerQueuesAgentsWhenConcurrencyIsLimited(t *testing.T) {
	provider := newGatedProvider()
	runner := NewRunner(provider, NewRegistry(), RunnerConfig{
		Model:             "test-model",
		SystemPrompt:      "test",
		MaxTurns:          4,
		MaxParallelAgents: 1,
	})

	slowStarted := provider.channelFor(provider.started, "slow task")
	slowRelease := provider.channelFor(provider.release, "slow task")
	fastStarted := provider.channelFor(provider.started, "fast task")
	fastRelease := provider.channelFor(provider.release, "fast task")

	slow, err := runner.SpawnAgent(nil, SpawnRequest{
		Name:       "slow",
		Prompt:     "slow task",
		Background: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if slow.Status != AgentRunning {
		t.Fatalf("expected running slow agent, got %s", slow.Status)
	}

	select {
	case <-slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("slow agent did not start")
	}

	fast, err := runner.SpawnAgent(nil, SpawnRequest{
		Name:       "fast",
		Prompt:     "fast task",
		Background: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fast.Status != AgentQueued {
		t.Fatalf("expected queued fast agent, got %s", fast.Status)
	}

	select {
	case <-fastStarted:
		t.Fatal("fast agent started before slot became available")
	case <-time.After(150 * time.Millisecond):
	}

	close(slowRelease)
	_, err = runner.WaitAgent(context.Background(), slow.ID)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-fastStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("fast agent did not start after slot release")
	}

	close(fastRelease)
	fastDone, err := runner.WaitAgent(context.Background(), fast.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fastDone.Status != AgentIdle {
		t.Fatalf("expected idle fast agent, got %s", fastDone.Status)
	}
}

func TestRunnerPrefersHigherPriorityAgentsWhenQueueing(t *testing.T) {
	provider := newGatedProvider()
	runner := NewRunner(provider, NewRegistry(), RunnerConfig{
		Model:             "test-model",
		SystemPrompt:      "test",
		MaxTurns:          4,
		MaxParallelAgents: 1,
	})

	slowStarted := provider.channelFor(provider.started, "slow task")
	slowRelease := provider.channelFor(provider.release, "slow task")
	lowStarted := provider.channelFor(provider.started, "low task")
	lowRelease := provider.channelFor(provider.release, "low task")
	highStarted := provider.channelFor(provider.started, "high task")
	highRelease := provider.channelFor(provider.release, "high task")

	slow, err := runner.SpawnAgent(nil, SpawnRequest{
		Name:       "slow",
		Prompt:     "slow task",
		Background: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if slow.Status != AgentRunning {
		t.Fatalf("expected running slow agent, got %s", slow.Status)
	}

	select {
	case <-slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("slow agent did not start")
	}

	low, err := runner.SpawnAgent(nil, SpawnRequest{
		Name:       "low",
		Prompt:     "low task",
		Priority:   1,
		Background: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	high, err := runner.SpawnAgent(nil, SpawnRequest{
		Name:       "high",
		Prompt:     "high task",
		Priority:   10,
		Background: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if low.Status != AgentQueued || high.Status != AgentQueued {
		t.Fatalf("expected queued agents, got low=%s high=%s", low.Status, high.Status)
	}

	close(slowRelease)
	if _, err := runner.WaitAgent(context.Background(), slow.ID); err != nil {
		t.Fatal(err)
	}

	select {
	case <-highStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("high-priority agent did not start after slot release")
	}

	select {
	case <-lowStarted:
		t.Fatal("low-priority agent started before higher-priority work completed")
	case <-time.After(150 * time.Millisecond):
	}

	close(highRelease)
	highDone, err := runner.WaitAgent(context.Background(), high.ID)
	if err != nil {
		t.Fatal(err)
	}
	if highDone.Status != AgentIdle {
		t.Fatalf("expected idle high-priority agent, got %s", highDone.Status)
	}

	select {
	case <-lowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("low-priority agent did not start after higher-priority work completed")
	}

	close(lowRelease)
	lowDone, err := runner.WaitAgent(context.Background(), low.ID)
	if err != nil {
		t.Fatal(err)
	}
	if lowDone.Status != AgentIdle {
		t.Fatalf("expected idle low-priority agent, got %s", lowDone.Status)
	}
}

func TestRunnerWaitAgentsUsesAllKnownAgentsWhenIDsEmpty(t *testing.T) {
	runner := NewRunner(&promptProvider{}, NewRegistry(), RunnerConfig{
		Model:             "test-model",
		SystemPrompt:      "test",
		MaxTurns:          4,
		MaxParallelAgents: 2,
	})

	first, err := runner.SpawnAgent(nil, SpawnRequest{
		Name:       "one",
		Prompt:     "first",
		Background: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.SpawnAgent(nil, SpawnRequest{
		Name:       "two",
		Prompt:     "second",
		Background: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	snapshots, err := runner.WaitAgents(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snapshots))
	}

	got := map[string]AgentStatus{}
	for _, snapshot := range snapshots {
		got[snapshot.ID] = snapshot.Status
	}
	if got[first.ID] != AgentIdle || got[second.ID] != AgentIdle {
		t.Fatalf("expected both agents idle, got %+v", got)
	}
}
