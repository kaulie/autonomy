package autonomy

import "testing"

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

func TestFinishAgentDeletesEphemeral(t *testing.T) {
	t.Parallel()
	auto := &Autonomy{AgentFactory: NewAgentFactory()}
	task := &Task{ID: "t-ephemeral"}
	agent := auto.AgentFactory.Create(task)
	id := agent.ID
	auto.finishAgent(agent)
	if got := auto.AgentFactory.Get(id); got != nil {
		t.Fatalf("ephemeral agent still cached: %+v", got)
	}
}

func TestFinishAgentKeepsPersistent(t *testing.T) {
	t.Parallel()
	auto := &Autonomy{AgentFactory: NewAgentFactory()}
	task := &Task{ID: "t-persistent"}
	agent := auto.AgentFactory.Create(task)
	agent.Lifecycle = AgentLifecyclePersistent
	id := agent.ID
	auto.finishAgent(agent)
	if got := auto.AgentFactory.Get(id); got == nil {
		t.Fatal("persistent agent was deleted")
	}
	if got := auto.AgentFactory.Get(id); got.State != "idle" {
		t.Fatalf("State=%q want idle", got.State)
	}
}
