package autonomy

import (
	"fmt"
	"testing"
)

func TestAgentDefaultLifecycleEphemeral(t *testing.T) {
	t.Parallel()
	f := NewAgentFactory()
	a := f.NewAgent("a1")
	if a.Lifecycle != AgentLifecycleEphemeral {
		t.Fatalf("Lifecycle=%q want %q", a.Lifecycle, AgentLifecycleEphemeral)
	}
	if !a.IsEphemeral() {
		t.Fatal("expected IsEphemeral")
	}
}

func TestFinishAgentDeletesEphemeralFromFactory(t *testing.T) {
	t.Parallel()
	auto := &Autonomy{AgentFactory: NewAgentFactory()}
	task := &Task{ID: "t-ephemeral"}
	agent := auto.AgentFactory.Create(task)
	id := agent.ID
	// No Cursor session attached — disposeCursorSession is a no-op; factory still drops ephemeral.
	auto.finishAgent(agent)
	if got := auto.AgentFactory.Get(id); got != nil {
		t.Fatalf("ephemeral agent still cached: %+v", got)
	}
}

func TestFinishAgentKeepsPersistentInFactory(t *testing.T) {
	t.Parallel()
	auto := &Autonomy{AgentFactory: NewAgentFactory()}
	task := &Task{ID: "t-persistent"}
	agent := auto.AgentFactory.Create(task)
	agent.Lifecycle = AgentLifecyclePersistent
	agent.CursorAgentID = "cursor-keep-me"
	id := agent.ID
	auto.finishAgent(agent)
	got := auto.AgentFactory.Get(id)
	if got == nil {
		t.Fatal("persistent agent was deleted from factory")
	}
	if got.State != "idle" {
		t.Fatalf("State=%q want idle", got.State)
	}
	if got.CursorAgentID != "cursor-keep-me" {
		t.Fatalf("CursorAgentID=%q want retained for Resume", got.CursorAgentID)
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
	return ReasoningResult{Decision: Decision{Reason: "ok", Action: NothingAction{}, Ctx: ctx}}, nil
}
