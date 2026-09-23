package autonomy

import "github.com/kaulie/autonomy/src/llmbackend"

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// TestListAgentsReadsEveryRowInOrder: the dashboard enumerates the fleet through
// AgentStore.ListAgents, so that read has to give back every agent row — oldest
// first, soft-deleted ones included — or the page silently loses agents.
func TestListAgentsReadsEveryRowInOrder(t *testing.T) {
	store, err := openStore(t, filepath.Join(t.TempDir(), "agents.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	first := &Agent{State: "idle", Lifecycle: AgentLifecyclePersistent}
	if err := store.UpsertAgent(first); err != nil {
		t.Fatal(err)
	}
	second := &Agent{State: "running", Lifecycle: AgentLifecycleEphemeral, CurrentTask: &Task{ID: "task-x"}}
	if err := store.UpsertAgent(second); err != nil {
		t.Fatal(err)
	}
	if err := store.SoftDeleteAgent(second.ID); err != nil {
		t.Fatal(err)
	}

	agents, err := store.ListAgents()
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 {
		t.Fatalf("ListAgents = %d rows, want 2 (soft-deleted included)", len(agents))
	}
	if agents[0].ID != first.ID || agents[1].ID != second.ID {
		t.Fatalf("ListAgents order = [%d %d], want [%d %d] (oldest first)", agents[0].ID, agents[1].ID, first.ID, second.ID)
	}
	if agents[1].DeletedAt.IsZero() {
		t.Fatalf("soft-deleted agent %d has no deleted_at", agents[1].ID)
	}
	if agents[1].CurrentTask == nil || agents[1].CurrentTask.ID != "task-x" {
		t.Fatalf("ListAgents lost the current task: %+v", agents[1].CurrentTask)
	}
}

// TestAgentStatusListMergesStoreAndLiveHandle: the feed is the store rows (what
// exists) merged with the live runtime handle (role / purpose, which the row does
// not persist), and it reports a run in flight as working. A soft-deleted agent is
// left out by default and only shown when asked for.
func TestAgentStatusListMergesStoreAndLiveHandle(t *testing.T) {
	store, err := openStore(t, filepath.Join(t.TempDir(), "status.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// One task with a running agent: the planner at work.
	if err := store.UpsertTask(&Task{ID: "task-live", Description: "watch me", Status: TaskStatusRunning}); err != nil {
		t.Fatal(err)
	}
	live := &Agent{State: "running", Lifecycle: AgentLifecyclePersistent, CurrentTask: &Task{ID: "task-live"}}
	if err := store.UpsertAgent(live); err != nil {
		t.Fatal(err)
	}
	// The run in flight: its header carries the run id the page shows.
	if _, err := store.BeginReasonTurn(ReasonTurn{
		TaskID: "task-live", AgentID: live.ID, Cycle: 1, Mode: ReasonModeAgent,
		Input: "go", RunID: "run-abc", Status: string(llmbackend.StatusRunning),
	}); err != nil {
		t.Fatal(err)
	}
	// A second agent that has been let go.
	gone := &Agent{State: "idle", Lifecycle: AgentLifecycleEphemeral}
	if err := store.UpsertAgent(gone); err != nil {
		t.Fatal(err)
	}
	if err := store.SoftDeleteAgent(gone.ID); err != nil {
		t.Fatal(err)
	}

	// The live handle: this is where role / purpose live, since the row does not
	// persist them.
	factory := NewAgentFactory()
	factory.Adopt(&Agent{ID: live.ID, Name: "live-planner", Role: AgentRolePlanner, Purpose: "the plan", Backend: llmbackend.Local})
	auto := &Autonomy{Store: store, AgentFactory: factory}

	list, err := auto.AgentStatusList(false)
	if err != nil {
		t.Fatal(err)
	}
	if list.Count != 1 || len(list.Agents) != 1 {
		t.Fatalf("live feed = %+v, want exactly the one live agent", list)
	}
	got := list.Agents[0]
	if got.AgentID != live.ID {
		t.Fatalf("agent id = %d, want %d", got.AgentID, live.ID)
	}
	if got.Role != string(AgentRolePlanner) || got.Purpose != "the plan" {
		t.Fatalf("role/purpose not merged from the live handle: %+v", got)
	}
	if got.Lifecycle != string(AgentLifecyclePersistent) {
		t.Fatalf("lifecycle = %q, want persistent", got.Lifecycle)
	}
	if got.CurrentTask != "task-live" {
		t.Fatalf("current task = %q, want task-live", got.CurrentTask)
	}
	if !got.Working || got.AgentRunID == "" {
		t.Fatalf("agent with an in-flight run not reported working: %+v", got)
	}
	if got.Health != AgentHealthOK {
		t.Fatalf("health = %q, want ok", got.Health)
	}

	// Asked for, the deleted agent comes back — as deleted.
	withDeleted, err := auto.AgentStatusList(true)
	if err != nil {
		t.Fatal(err)
	}
	if withDeleted.Count != 2 {
		t.Fatalf("include-deleted feed = %d agents, want 2", withDeleted.Count)
	}
	var foundDeleted bool
	for _, a := range withDeleted.Agents {
		if a.AgentID == gone.ID {
			foundDeleted = true
			if a.Health != AgentHealthDeleted || a.Working {
				t.Fatalf("deleted agent reported as %+v", a)
			}
		}
	}
	if !foundDeleted {
		t.Fatalf("deleted agent %d missing from the include-deleted feed", gone.ID)
	}
}

// TestAgentDashboardEndpoints: the two routes the page is built from. GET /api/agents
// answers the JSON feed, and GET /dashboard answers the page that polls it — the page
// must actually talk to /api/agents, or "auto-refresh" is a comment.
func TestAgentDashboardEndpoints(t *testing.T) {
	store, err := openStore(t, filepath.Join(t.TempDir(), "http-dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	agent := &Agent{State: "idle", Lifecycle: AgentLifecyclePersistent}
	if err := store.UpsertAgent(agent); err != nil {
		t.Fatal(err)
	}
	auto := &Autonomy{Store: store, AgentFactory: NewAgentFactory()}
	srv := NewHTTPServer(auto)

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/agents = %d body=%s", rec.Code, rec.Body.String())
	}
	var list AgentStatusListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Count != 1 || list.Agents[0].AgentID != agent.ID {
		t.Fatalf("feed = %+v, want the one agent %d", list, agent.ID)
	}
	if list.GeneratedAt.IsZero() {
		t.Fatalf("feed has no generated_at")
	}

	req = httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /dashboard = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("GET /dashboard content-type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"Agent Status", "/api/agents", "setInterval", "<table"} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard page missing %q", want)
		}
	}
}

// TestAgentStatusListWithoutStore: a runtime with no store answers an error rather
// than an empty fleet — an empty page has to mean "no agents", never "the read
// failed".
func TestAgentStatusListWithoutStore(t *testing.T) {
	auto := &Autonomy{AgentFactory: NewAgentFactory()}
	if _, err := auto.AgentStatusList(false); err == nil {
		t.Fatal("AgentStatusList without a store returned no error")
	}
}
