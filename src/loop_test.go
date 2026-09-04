package autonomy_test

import (
	"testing"

	"github.com/kaulie/autonomy/src"
)

type memWorld struct {
	state map[string]string
}

func (w *memWorld) Get(id string) string { return w.state[id] }

type fixedDecide struct {
	action autonomy.Action
}

func (d fixedDecide) Decide(autonomy.Task, autonomy.Agent, autonomy.World) (autonomy.Action, error) {
	return d.action, nil
}

type echoCap struct{}

func (echoCap) Name() string { return "service.health_check" }

func (echoCap) Run(in map[string]string) (map[string]string, error) {
	return map[string]string{"status": in["status"]}, nil
}

func TestLoopDoneWhenWorldAlreadyHealthy(t *testing.T) {
	world := &memWorld{state: map[string]string{"svc": "healthy"}}
	loop := autonomy.Loop{
		Agent:    autonomy.Agent{ID: "owner-1"},
		Decide:   fixedDecide{action: autonomy.Action{Capability: "service.health_check", Target: "svc"}},
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
}

func TestLoopFailsWhenWorldStaysUnhealthy(t *testing.T) {
	world := &memWorld{state: map[string]string{"svc": "unhealthy"}}
	loop := autonomy.Loop{
		Agent: autonomy.Agent{ID: "owner-1"},
		Decide: fixedDecide{action: autonomy.Action{
			Capability: "service.health_check",
			Target:     "svc",
			Input:      map[string]string{"status": "unhealthy"},
		}},
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
