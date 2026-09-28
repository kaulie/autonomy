package autonomy

import (
	"github.com/kaulie/autonomy/src/llmbackend/cursor"
)

import "github.com/kaulie/autonomy/src/llmbackend"

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability/broker"
)

func TestRuntimeAcquireLocalAgentRegistersInFactory(t *testing.T) {
	t.Parallel()
	f := NewAgentFactory()
	rt := NewRuntime(f)
	requested := t.TempDir()
	sess, err := rt.AcquireAgent(context.Background(), broker.AcquireAgentOpts{
		Purpose:   "test",
		Workspace: requested,
		Backend:   string(llmbackend.Local),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := sess.ID()
	if id == "" || !strings.HasPrefix(id, "agent-") {
		t.Fatalf("id=%q", id)
	}
	if got := sess.Workspace(); got != requested {
		t.Fatalf("session workspace=%q, want the requested %q", got, requested)
	}
	if f.Get(id) == nil {
		t.Fatal("agent not registered in factory")
	}
	if _, err := sess.Prompt(context.Background(), "hi"); err == nil {
		t.Fatal("expected prompt unsupported for local backend")
	}
	if err := sess.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.Get(id) == nil {
		t.Fatal("an agent is kept after its session is released; nothing asked for a throwaway one")
	}
}

// TestAReleasedWorkerIsReacquiredForTheSameTaskAndPurpose: a persistent worker is
// the specialist for one (task, purpose). Release parks it; the next acquisition
// of that pair is the same agent, in the same workspace — not a newly registered
// one. That is what "kept so it is worth coming back to" actually does.
func TestAReleasedWorkerIsReacquiredForTheSameTaskAndPurpose(t *testing.T) {
	ctx := context.Background()
	f := NewAgentFactory()
	rt := NewRuntime(f)
	opts := broker.AcquireAgentOpts{
		Purpose: "code_edit", TaskID: "task-reuse", Backend: string(llmbackend.Local),
	}
	first, err := rt.AcquireAgent(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	id, workspace := first.ID(), first.Workspace()
	if err := first.Release(ctx); err != nil {
		t.Fatal(err)
	}
	parked := f.Get(id)
	if parked == nil {
		t.Fatal("the worker was dropped after Release")
	}
	if parked.IsRunning() {
		t.Fatal("a parked worker must be idle")
	}

	second, err := rt.AcquireAgent(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Release(ctx) }()
	if second.ID() != id {
		t.Fatalf("second acquire id=%q want the parked worker %q", second.ID(), id)
	}
	if second.Workspace() != workspace {
		t.Fatalf("workspace=%q want the parked worker's %q", second.Workspace(), workspace)
	}
	if names := workerNames(f, "task-reuse", "code_edit"); len(names) != 1 {
		t.Fatalf("workers for this job=%v, want exactly the one specialist", names)
	}
}

// A worker still on loan is not stolen: a second acquire for the same job while
// the first has not been released registers a new agent, so two overlapping
// steps do not share a conversation.
func TestABusyWorkerIsNotReused(t *testing.T) {
	ctx := context.Background()
	f := NewAgentFactory()
	rt := NewRuntime(f)
	opts := broker.AcquireAgentOpts{
		Purpose: "code_edit", TaskID: "task-busy", Backend: string(llmbackend.Local),
	}
	first, err := rt.AcquireAgent(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Release(ctx) }()
	second, err := rt.AcquireAgent(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Release(ctx) }()
	if first.ID() == second.ID() {
		t.Fatalf("overlapping loans shared %s", first.ID())
	}
}

func TestAWorkerForADifferentPurposeOrTaskIsNotReused(t *testing.T) {
	ctx := context.Background()
	f := NewAgentFactory()
	rt := NewRuntime(f)
	code, err := rt.AcquireAgent(ctx, broker.AcquireAgentOpts{
		Purpose: "code_edit", TaskID: "task-a", Backend: string(llmbackend.Local),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := code.Release(ctx); err != nil {
		t.Fatal(err)
	}

	monitor, err := rt.AcquireAgent(ctx, broker.AcquireAgentOpts{
		Purpose: "deployment.monitor", TaskID: "task-a", Backend: string(llmbackend.Local),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = monitor.Release(ctx) }()
	if monitor.ID() == code.ID() {
		t.Fatal("a different purpose must not take the code_edit specialist")
	}

	otherTask, err := rt.AcquireAgent(ctx, broker.AcquireAgentOpts{
		Purpose: "code_edit", TaskID: "task-b", Backend: string(llmbackend.Local),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = otherTask.Release(ctx) }()
	if otherTask.ID() == code.ID() {
		t.Fatal("a different task must not take the other task's specialist")
	}
}

func TestAnEphemeralWorkerIsNeverReused(t *testing.T) {
	ctx := context.Background()
	f := NewAgentFactory()
	rt := NewRuntime(f)
	opts := broker.AcquireAgentOpts{
		Purpose: "one_shot", TaskID: "task-eph", Backend: string(llmbackend.Local), Ephemeral: true,
	}
	first, err := rt.AcquireAgent(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	name := first.ID()
	if err := first.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if f.Get(name) != nil {
		t.Fatal("an ephemeral worker must be dropped on Release")
	}
	second, err := rt.AcquireAgent(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Release(ctx) }()
	if second.ID() == name {
		t.Fatal("an ephemeral worker must not be reacquired")
	}
}

func workerNames(f *AgentFactory, taskID, purpose string) []string {
	var names []string
	for _, agent := range f.snapshot() {
		if agent == nil || agent.Role != AgentRoleWorker || agent.Purpose != purpose {
			continue
		}
		if agent.CurrentTask == nil || agent.CurrentTask.ID != taskID {
			continue
		}
		names = append(names, agent.Name)
	}
	return names
}

// A parked worker keeps the provider session the last loan attached: Release does
// not tear it down, so the next acquire for that job does not re-attach and does
// not resend the frame. Its own rounds keep counting.
func TestAParkedWorkerKeepsItsProviderSessionAndRound(t *testing.T) {
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	t.Setenv("PROJECT_ROOT", preparePolicyRoot(t))

	store, err := openStore(t, filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	t.Cleanup(func() { _store = prev })
	_store = store

	ctx := context.Background()
	f := NewAgentFactory()
	rt := NewRuntime(f)
	opts := broker.AcquireAgentOpts{Purpose: "code_edit", TaskID: "task-park"}
	first, err := rt.AcquireAgent(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Prompt(ctx, "hello"); err != nil {
		t.Fatalf("first prompt: %v", err)
	}
	id := first.ID()
	worker := f.Get(id)
	if worker == nil || worker.llm == nil || worker.llm.ProviderSessionID() == "" {
		t.Fatal("the first loan did not attach a provider session")
	}
	sessionID := worker.llm.ProviderSessionID()
	if err := first.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if worker.llm == nil || worker.llm.ProviderSessionID() != sessionID {
		t.Fatal("Release tore down the provider session of a persistent worker")
	}
	if worker.needsLLMFrame() {
		t.Fatal("a parked worker must not forget that the frame was sent")
	}

	second, err := rt.AcquireAgent(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Release(ctx) }()
	if second.ID() != id {
		t.Fatalf("second acquire id=%q want %q", second.ID(), id)
	}
	if worker.llm == nil || worker.llm.ProviderSessionID() != sessionID {
		t.Fatal("the second loan re-attached instead of keeping the parked session")
	}
	if _, err := second.Prompt(ctx, "again"); err != nil {
		t.Fatalf("second prompt: %v", err)
	}

	rows, err := store.RawDB().Query(`SELECT cycle FROM reason_turns WHERE task_id = 'task-park' AND input NOT LIKE '%Autonomy Bootstrap Prompt%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cycles []int
	for rows.Next() {
		var cycle int
		if err := rows.Scan(&cycle); err != nil {
			t.Fatal(err)
		}
		cycles = append(cycles, cycle)
	}
	if len(cycles) != 2 || cycles[0] != 1 || cycles[1] != 2 {
		t.Fatalf("cycles=%v, want the worker's rounds to continue [1 2] across loans", cycles)
	}
}

// TestAcquiredWorkerIsKeptUnlessAskedToBeThrowaway: a worker is kept like any
// other agent — its row, its resumable provider session — unless the capability
// asked for a throwaway one. The opt-out deletes; the default does not.
func TestAcquiredWorkerIsKeptUnlessAskedToBeThrowaway(t *testing.T) {
	ctx := context.Background()
	f := NewAgentFactory()
	rt := NewRuntime(f)

	kept, err := rt.AcquireAgent(ctx, broker.AcquireAgentOpts{Purpose: "code_edit", Backend: string(llmbackend.Local)})
	if err != nil {
		t.Fatal(err)
	}
	if got := kept.(*LLMSession).agent.Lifecycle; got != AgentLifecyclePersistent {
		t.Fatalf("acquired agent lifecycle=%q, want %q", got, AgentLifecyclePersistent)
	}
	keptName := kept.ID()
	if err := kept.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if f.Get(keptName) == nil {
		t.Fatalf("a kept worker %s must stay in the factory for its next instruction", keptName)
	}

	throwaway, err := rt.AcquireAgent(ctx, broker.AcquireAgentOpts{
		Purpose: "one_shot", Backend: string(llmbackend.Local), Ephemeral: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := throwaway.(*LLMSession).agent.Lifecycle; got != AgentLifecycleEphemeral {
		t.Fatalf("throwaway lifecycle=%q, want %q", got, AgentLifecycleEphemeral)
	}
	name := throwaway.ID()
	if err := throwaway.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if f.Get(name) != nil {
		t.Fatal("a throwaway agent must be dropped when its session is released")
	}
}

// TestADelegatedWorkerCycleCountsItsOwnInteractionRounds: cycle is the round with
// an LLM, and a worker is having its own conversation — its first prompt is cycle
// 1, its second cycle 2. It is not the delegating task's decision cycle, and it is
// not 0 either: the worker did interact.
func TestADelegatedWorkerCycleCountsItsOwnInteractionRounds(t *testing.T) {
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")

	store, err := openStore(t, filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	t.Cleanup(func() { _store = prev })
	_store = store

	rt := NewRuntime(NewAgentFactory())
	sess, err := rt.AcquireAgent(context.Background(), broker.AcquireAgentOpts{
		Purpose: "code_edit",
		TaskID:  "task-9",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Release(context.Background()) }()

	// A session renders its first prompt (the frame) from the policy files.
	t.Setenv("PROJECT_ROOT", preparePolicyRoot(t))

	for i := 1; i <= 2; i++ {
		if _, err := sess.Prompt(context.Background(), "hello"); err != nil {
			t.Fatalf("prompt %d: %v", i, err)
		}
	}

	rows, err := store.RawDB().Query(`SELECT id, cycle, mode FROM reason_turns WHERE task_id = 'task-9' AND input NOT LIKE '%Autonomy Bootstrap Prompt%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cycles []int
	var turnIDs []int64
	for rows.Next() {
		var id int64
		var cycle int
		var mode string
		if err := rows.Scan(&id, &cycle, &mode); err != nil {
			t.Fatal(err)
		}
		if mode != string(ReasonModeAgent) {
			t.Errorf("mode=%q, want %q", mode, ReasonModeAgent)
		}
		cycles = append(cycles, cycle)
		turnIDs = append(turnIDs, id)
	}
	if len(cycles) != 2 || cycles[0] != 1 || cycles[1] != 2 {
		t.Fatalf("cycles=%v, want the worker's own interaction rounds [1 2]", cycles)
	}
	// The messages of a run carry the same cycle, so the round is readable per row.
	messages, err := store.ListLLMMessages(turnIDs[1])
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) == 0 || messages[0].Cycle != 2 {
		t.Fatalf("messages=%+v, want cycle 2 on the second round's rows", messages)
	}
}

// purpose the capability named is what the agent's own prompt says it is (## Agent
// — "role", "purpose"). The agent the runtime creates for a task is the other
// kind: its planner.
func TestRuntimeAcquireAgentKnowsWhatItIsFor(t *testing.T) {
	t.Parallel()
	f := NewAgentFactory()
	if got := f.Create(&Task{ID: "task-9"}).Role; got != AgentRolePlanner {
		t.Fatalf("the task's own agent role=%q, want %q", got, AgentRolePlanner)
	}

	rt := NewRuntime(f)
	sess, err := rt.AcquireAgent(context.Background(), broker.AcquireAgentOpts{
		Purpose: "code_edit",
		Backend: string(llmbackend.Local),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Release(context.Background()) }()

	acquired, ok := sess.(*LLMSession)
	if !ok {
		t.Fatalf("acquired session is %T, want the runtime's own", sess)
	}
	if acquired.agent.Role != AgentRoleWorker || acquired.agent.Purpose != "code_edit" {
		t.Fatalf("role/purpose=%q/%q, want worker/code_edit", acquired.agent.Role, acquired.agent.Purpose)
	}
	for _, want := range []string{`"role": "worker"`, `"purpose": "code_edit"`, `"code_edit"`} {
		if got := string(formatAgentIdentityJSON(acquired.agent)); !strings.Contains(got, want) {
			t.Errorf("acquired agent identity missing %s:\n%s", want, got)
		}
	}
}

// TestRuntimeAcquireAgentWithoutWorkspaceUsesItsOwn pins what a delegating
// capability relies on: acquire without a workspace and the agent runs in its own
// AGENT_WORKSPACE, which the session reports back.
func TestRuntimeAcquireAgentWithoutWorkspaceUsesItsOwn(t *testing.T) {
	t.Parallel()
	f := NewAgentFactory()
	rt := NewRuntime(f)
	sess, err := rt.AcquireAgent(context.Background(), broker.AcquireAgentOpts{
		Purpose: "code_edit",
		Backend: string(llmbackend.Local),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Release(context.Background()) }()
	ws := sess.Workspace()
	if ws == "" || !strings.Contains(ws, sess.ID()) {
		t.Fatalf("session workspace=%q, want the agent's own sandbox for %q", ws, sess.ID())
	}
	if want := AgentWorkspacePath(sess.ID()); ws != want {
		t.Fatalf("session workspace=%q, want %q", ws, want)
	}
}

// TestRuntimeAcquireAgentRecordsTheDelegatedTask: a capability-acquired agent
// works on the same task as its caller, so its agents row must carry that
// current_task_id (it used to stay empty while its runs already recorded the
// task).
func TestRuntimeAcquireAgentRecordsTheDelegatedTask(t *testing.T) {
	store, err := openStore(t, filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	_store = store
	t.Cleanup(func() { _store = prev })

	rt := NewRuntime(NewAgentFactory())
	sess, err := rt.AcquireAgent(context.Background(), broker.AcquireAgentOpts{
		Purpose: "code_edit",
		TaskID:  "task-9",
		Backend: string(llmbackend.Local),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Release(context.Background()) }()

	var taskID, state string
	if err := store.RawDB().QueryRow(`SELECT current_task_id, state FROM agents WHERE name = ?`, sess.ID()).
		Scan(&taskID, &state); err != nil {
		t.Fatal(err)
	}
	if taskID != "task-9" {
		t.Fatalf("agents.current_task_id=%q, want the delegated task %q", taskID, "task-9")
	}
	if state != "running" {
		t.Fatalf("agents.state=%q, want running", state)
	}
}

func TestNewCursorClientIsSingleEntry(t *testing.T) {
	t.Parallel()
	// Smoke: constructor returns a client; real bridge not required.
	c := cursor.NewCursorClient(t.TempDir(), "")
	if c == nil {
		t.Fatal("nil client")
	}
	_ = c.Close()
}
