package autonomy_test

import (
	"errors"
	"testing"

	"github.com/kaulie/autonomy/src"
)

type memWorld struct {
	state map[string]string
}

func (w *memWorld) Get(id string) string { return w.state[id] }

func (w *memWorld) Set(id, state string) { w.state[id] = state }

type fixedDecide struct {
	decision autonomy.Decision
	err      error
}

func (d fixedDecide) Decide(autonomy.DecisionContext) (autonomy.Decision, error) {
	return d.decision, d.err
}

type echoCap struct{}

func (echoCap) Name() string { return "service.health_check" }

func (echoCap) Run(in map[string]string) (map[string]string, error) {
	return map[string]string{"status": in["status"]}, nil
}

type failCap struct{}

func (failCap) Name() string { return "service.health_check" }

func (failCap) Run(map[string]string) (map[string]string, error) {
	return nil, errors.New("probe failed")
}

func TestLoopDoneWhenWorldAlreadyHealthy(t *testing.T) {
	world := &memWorld{state: map[string]string{"svc": "healthy"}}
	loop := autonomy.Loop{
		Agent: autonomy.Agent{ID: "owner-1"},
		Decide: fixedDecide{decision: autonomy.Decision{Action: autonomy.Action{
			Capability: "service.health_check", Target: "svc",
		}}},
		Runtime:  autonomy.NewRuntime(echoCap{}),
		World:    world,
		Verifier: autonomy.StateVerifier{},
		MaxSteps: 3,
	}
	task := autonomy.Task{
		ID: "t1", Goal: "service healthy", Target: "svc",
		Contract: autonomy.Contract{ExpectedState: "healthy"},
	}
	if err := loop.Run(task); err != nil {
		t.Fatal(err)
	}
	if loop.Agent.State != "idle" {
		t.Fatalf("agent state = %q, want idle", loop.Agent.State)
	}
	if len(loop.History) != 1 {
		t.Fatalf("history len = %d, want 1", len(loop.History))
	}
}

func TestLoopFailsWhenWorldStaysUnhealthy(t *testing.T) {
	world := &memWorld{state: map[string]string{"svc": "unhealthy"}}
	loop := autonomy.Loop{
		Agent: autonomy.Agent{ID: "owner-1"},
		Decide: fixedDecide{decision: autonomy.Decision{Action: autonomy.Action{
			Capability: "service.health_check",
			Target:     "svc",
			Input:      map[string]string{"status": "unhealthy"},
		}}},
		Runtime:  autonomy.NewRuntime(echoCap{}),
		World:    world,
		Verifier: autonomy.StateVerifier{},
		MaxSteps: 2,
	}
	task := autonomy.Task{
		ID: "t2", Goal: "service healthy", Target: "svc",
		Contract: autonomy.Contract{ExpectedState: "healthy"},
	}
	if err := loop.Run(task); err == nil {
		t.Fatal("expected incomplete task error")
	}
}

func TestLoopTerminatesOnExecuteFailure(t *testing.T) {
	world := &memWorld{state: map[string]string{"svc": "unhealthy"}}
	loop := autonomy.Loop{
		Agent: autonomy.Agent{ID: "owner-1"},
		Decide: fixedDecide{decision: autonomy.Decision{Action: autonomy.Action{
			Capability: "service.health_check", Target: "svc",
		}}},
		Runtime:  autonomy.NewRuntime(failCap{}),
		World:    world,
		Verifier: autonomy.StateVerifier{},
		MaxSteps: 3,
	}
	task := autonomy.Task{
		ID: "t3", Goal: "service healthy", Target: "svc",
		Contract: autonomy.Contract{ExpectedState: "healthy"},
	}
	err := loop.Run(task)
	if err == nil {
		t.Fatal("expected task failure")
	}
	if len(loop.History) != 1 {
		t.Fatalf("history len = %d, want 1", len(loop.History))
	}
	if loop.History[0].Err == nil {
		t.Fatal("expected result error in history")
	}
}
