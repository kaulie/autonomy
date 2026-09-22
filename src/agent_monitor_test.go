package autonomy

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAgentsOverviewAggregatesEveryStatus drives the monitoring aggregation over
// a store holding one agent in each projected state: a planner running a task, an
// idle worker, a worker with work queued behind it, and an agent that was let go.
// It asserts both the per-agent statuses and the summary tally, plus the runtime
// enrichment (role / backend / workspace) the factory adds to a stored row.
func TestAgentsOverviewAggregatesEveryStatus(t *testing.T) {
	store, err := openStore(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// A running planner: its task names it as the agent, and it has an in-flight
	// reason turn.
	task := &Task{ID: "task-run", Description: "d", Domain: TaskDomainServer, Status: TaskStatusRunning, AgentID: 10001}
	if err := store.UpsertTask(task); err != nil {
		t.Fatal(err)
	}
	planner := &Agent{ID: 10001, Name: "agent-10001", State: "running", Lifecycle: AgentLifecyclePersistent,
		CurrentTask: task, LLMProvider: LLMProviderCline, Model: "deepseek-v4-flash"}
	if err := store.UpsertAgent(planner); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginReasonTurn(ReasonTurn{
		TaskID: "task-run", AgentID: 10001, Cycle: 1, Mode: ReasonModePlan,
		Input: "go", Status: string(LLMStatusRunning), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	// An idle worker: no task, nothing queued.
	idle := &Agent{ID: 10002, Name: "agent-10002", State: "idle", Lifecycle: AgentLifecyclePersistent}
	if err := store.UpsertAgent(idle); err != nil {
		t.Fatal(err)
	}

	// A blocked worker: not running, but a message waits behind it.
	blocked := &Agent{ID: 10003, Name: "agent-10003", State: "idle", Lifecycle: AgentLifecycleEphemeral}
	if err := store.UpsertAgent(blocked); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueMessage(AgentMessage{
		AgentID: 10003, TaskID: "task-run", Sender: MessageSenderAgent, SenderID: "agent-10001",
		Kind: MessageKindDelegation, Content: "do the thing", Status: MessageStatusQueued, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	// A done agent: it was let go.
	done := &Agent{ID: 10004, Name: "agent-10004", State: "running", Lifecycle: AgentLifecycleEphemeral}
	if err := store.UpsertAgent(done); err != nil {
		t.Fatal(err)
	}
	if err := store.SoftDeleteAgent(10004); err != nil {
		t.Fatal(err)
	}

	// The factory holds the planner live, so its runtime-only fields show up.
	factory := NewAgentFactory()
	factory.Adopt(&Agent{ID: 10001, Name: "agent-10001", Role: AgentRolePlanner,
		Backend: AgentBackendCline, State: "running", Workspace: "/tmp/agent-10001/"})

	auto := &Autonomy{AgentFactory: factory, Store: store}
	overview, err := auto.AgentsOverview()
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Agents) != 4 {
		t.Fatalf("agents=%d want 4: %+v", len(overview.Agents), overview.Agents)
	}
	byID := map[int64]AgentMonitorEntry{}
	for _, a := range overview.Agents {
		byID[a.ID] = a
	}
	assertStatus := func(id int64, want string) {
		if got := byID[id].Status; got != want {
			t.Errorf("agent %d status=%q want %q (%+v)", id, got, want, byID[id])
		}
	}
	assertStatus(10001, AgentMonitorRunning)
	assertStatus(10002, AgentMonitorIdle)
	assertStatus(10003, AgentMonitorBlocked)
	assertStatus(10004, AgentMonitorDone)

	want := AgentMonitorSummary{Total: 4, Running: 1, Idle: 1, Blocked: 1, Done: 1}
	if overview.Summary != want {
		t.Errorf("summary=%+v want %+v", overview.Summary, want)
	}

	// Role / backend / workspace: the planner is enriched from the live handle;
	// the stored-only worker falls back to the row.
	if e := byID[10001]; e.Role != string(AgentRolePlanner) || e.Backend != string(AgentBackendCline) {
		t.Errorf("planner enrichment = role %q backend %q", e.Role, e.Backend)
	}
	if e := byID[10002]; e.Role != string(AgentRoleWorker) || e.Backend != string(AgentBackendLocal) {
		t.Errorf("idle worker fallback = role %q backend %q", e.Role, e.Backend)
	}
	if e := byID[10001]; e.Workspace != "/tmp/agent-10001/" {
		t.Errorf("planner workspace = %q", e.Workspace)
	}
	if e := byID[10002]; e.Workspace != AgentWorkspacePath("agent-10002") {
		t.Errorf("stored workspace = %q want %q", e.Workspace, AgentWorkspacePath("agent-10002"))
	}
	if !strings.Contains(overview.Sources[0], AgentMonitorSourceAutonomy) {
		t.Errorf("sources = %v want autonomy", overview.Sources)
	}
}

func TestAgentsOverviewWithoutStore(t *testing.T) {
	if _, err := (&Autonomy{}).AgentsOverview(); err == nil {
		t.Fatal("expected an error when no store is wired in")
	}
}

// TestAgentMonitorHTTPEndpoints exercises the three routes the panel is built
// on: the JSON aggregation, the single-page dashboard, and the SSE stream — the
// last read over a real connection so the flush per frame is what is tested.
func TestAgentMonitorHTTPEndpoints(t *testing.T) {
	store, err := openStore(filepath.Join(t.TempDir(), "monitor-http.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	prevStore, prevAuto, prevFlag := _store, _autonomy, bootstrapFlag
	t.Cleanup(func() { _store, _autonomy, bootstrapFlag = prevStore, prevAuto, prevFlag })
	_store = store
	bootstrapFlag = true

	agent := &Agent{ID: 10001, Name: "agent-10001", State: "running", Lifecycle: AgentLifecyclePersistent}
	if err := store.UpsertAgent(agent); err != nil {
		t.Fatal(err)
	}
	auto := &Autonomy{AgentFactory: NewAgentFactory(), Store: store}
	_autonomy = auto
	srv := NewHTTPServer(auto)

	// GET /api/agents
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/agents status=%d body=%s", rec.Code, rec.Body.String())
	}
	var overview AgentMonitorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &overview); err != nil {
		t.Fatalf("decode /api/agents: %v (%s)", err, rec.Body.String())
	}
	if overview.Summary.Total != 1 || overview.Summary.Running != 1 {
		t.Fatalf("summary=%+v body=%s", overview.Summary, rec.Body.String())
	}
	if len(overview.Agents) != 1 || overview.Agents[0].Name != "agent-10001" {
		t.Fatalf("agents=%+v", overview.Agents)
	}

	// GET /monitor — the embedded single page.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/monitor", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /monitor status=%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("GET /monitor content-type=%q", ct)
	}
	page := rec.Body.String()
	for _, want := range []string{"Agent Monitor", "/api/agents", "EventSource"} {
		if !strings.Contains(page, want) {
			t.Errorf("dashboard HTML missing %q", want)
		}
	}

	// GET /api/agents/stream — read one frame and stop.
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/agents/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("stream content-type=%q", ct)
	}
	reader := bufio.NewReader(resp.Body)
	var event, data string
	for i := 0; i < 20; i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read stream: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		if event != "" && data != "" {
			break
		}
	}
	if event != "agents" {
		t.Fatalf("first SSE event=%q want agents", event)
	}
	var frame AgentMonitorResponse
	if err := json.Unmarshal([]byte(data), &frame); err != nil {
		t.Fatalf("decode SSE data: %v (%s)", err, data)
	}
	if frame.Summary.Total != 1 {
		t.Fatalf("SSE frame summary=%+v", frame.Summary)
	}
}
