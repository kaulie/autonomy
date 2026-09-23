package autonomy

import (
	"fmt"
	"path/filepath"
	"testing"
)

// TestAgentDefaultLifecycleIsPersistent: the default is to keep an agent — its
// row, its task binding, its recorded provider session — so a later instruction
// for the same task can find it and resume it (see resumeAgentForTask).
func TestAgentDefaultLifecycleIsPersistent(t *testing.T) {
	t.Parallel()
	f := NewAgentFactory()
	a := f.NewAgent()
	if a.Lifecycle != AgentLifecyclePersistent {
		t.Fatalf("Lifecycle=%q want %q", a.Lifecycle, AgentLifecyclePersistent)
	}
	if a.IsEphemeral() {
		t.Fatal("an agent is kept by default: IsEphemeral must be false")
	}
}

func TestCreateAgentIDIndependentOfTask(t *testing.T) {
	t.Parallel()
	f := NewAgentFactory()
	task := &Task{ID: "1"}
	a1 := f.Create(task)
	a2 := f.Create(task)
	if a1.ID == 0 || a2.ID == 0 {
		t.Fatal("empty agent id")
	}
	if a1.ID == a2.ID {
		t.Fatalf("expected distinct agent ids, both %d", a1.ID)
	}
	if a1.Name != fmt.Sprintf("agent-%d", a1.ID) || a2.Name != fmt.Sprintf("agent-%d", a2.ID) {
		t.Fatalf("agent names must follow agent-{id}; got %q %q", a1.Name, a2.Name)
	}
	if a1.CurrentTask != task || a2.CurrentTask != task {
		t.Fatal("CurrentTask not bound")
	}
	if task.AgentID != a2.ID {
		t.Fatalf("task.AgentID=%d want last created agent %d", task.AgentID, a2.ID)
	}
}

func TestFinishAgentDeletesEphemeralFromFactory(t *testing.T) {
	t.Parallel()
	auto := &Autonomy{AgentFactory: NewAgentFactory()}
	task := &Task{ID: "t-ephemeral"}
	agent := auto.AgentFactory.Create(task)
	// A throwaway agent is one somebody asked for; it is not the default.
	agent.Lifecycle = AgentLifecycleEphemeral
	name := agent.Name
	// No Cursor session attached — disposeCursorSession is a no-op; factory still drops ephemeral.
	auto.finishAgent(agent)
	if got := auto.AgentFactory.Get(name); got != nil {
		t.Fatalf("ephemeral agent still cached: %+v", got)
	}
}

func TestFinishAgentKeepsPersistentInFactory(t *testing.T) {
	t.Parallel()
	auto := &Autonomy{AgentFactory: NewAgentFactory()}
	task := &Task{ID: "t-persistent"}
	agent := auto.AgentFactory.Create(task)
	agent.Lifecycle = AgentLifecyclePersistent
	agent.LLMAgentID = "cursor-keep-me"
	name := agent.Name
	auto.finishAgent(agent)
	got := auto.AgentFactory.Get(name)
	if got == nil {
		t.Fatal("persistent agent was deleted from factory")
	}
	if got.State != "idle" {
		t.Fatalf("State=%q want idle", got.State)
	}
	if got.LLMAgentID != "cursor-keep-me" {
		t.Fatalf("LLMAgentID=%q want retained for Resume", got.LLMAgentID)
	}
}

// TestFinishAgentKeepsTheDefaultAgentInTheStore: an agent that ran is still
// there afterwards — the row a later instruction (in this process or in the one
// after a restart) finds it by.
func TestFinishAgentKeepsTheDefaultAgentInTheStore(t *testing.T) {
	store, err := openStore(t, filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	t.Cleanup(func() { _store = prev })
	_store = store

	auto := &Autonomy{AgentFactory: NewAgentFactory(), Store: store}
	task := &Task{ID: "t-kept"}
	agent := auto.AgentFactory.Create(task)
	auto.finishAgent(agent)

	stored, err := store.GetAgent(agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil {
		t.Fatal("the agent a task was paired with was let go")
	}
	if !stored.DeletedAt.IsZero() {
		t.Fatalf("agents.deleted_at=%v, want a live row", stored.DeletedAt)
	}
	if stored.Lifecycle != AgentLifecyclePersistent {
		t.Fatalf("stored lifecycle=%q, want %q", stored.Lifecycle, AgentLifecyclePersistent)
	}
}

func TestDecidePassesAgentInContext(t *testing.T) {
	t.Parallel()
	f := NewAgentFactory()
	task := &Task{ID: "t-ctx"}
	agent := f.Create(task)
	agent.DecideMaker = &DecisionMaker{reasoner: captureAgentReasoner{}}
	d, err := agent.Decide()
	if err != nil {
		t.Fatal(err)
	}
	if d.Ctx.Agent != agent {
		t.Fatal("DecisionContext.Agent not set")
	}
}

type captureAgentReasoner struct{}

func (captureAgentReasoner) Reason(ctx DecisionContext, _ ReasoningInput) (ReasoningResult, error) {
	if ctx.Agent == nil {
		return ReasoningResult{}, fmt.Errorf("missing agent")
	}
	return ReasoningResult{Decision: Decision{Type: "plan", Reason: "ok", Actions: []Action{NothingAction{}}, Ctx: ctx}}, nil
}
