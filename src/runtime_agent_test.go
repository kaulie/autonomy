package autonomy

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

	store, err := openStore(filepath.Join(t.TempDir(), "autonomy.db"))
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

	for i := 1; i <= 2; i++ {
		if _, err := sess.Prompt(context.Background(), "hello"); err != nil {
			t.Fatalf("prompt %d: %v", i, err)
		}
	}

	rows, err := store.RawDB().Query(`SELECT id, cycle, mode FROM reason_turns WHERE task_id = 'task-9' ORDER BY id`)
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
	store, err := openStore(filepath.Join(t.TempDir(), "autonomy.db"))
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
	c := llmbackend.NewCursorClient(t.TempDir())
	if c == nil {
		t.Fatal("nil client")
	}
	_ = c.Close()
}
