package autonomy_test

import (
	"testing"

	"github.com/kaulie/autonomy"
)

type memWorld struct {
	state map[string]string
}

func (w *memWorld) Get(id string) string { return w.state[id] }

type fixedAgent struct {
	step autonomy.Step
}

func (a fixedAgent) Next(autonomy.Task, autonomy.World) (autonomy.Step, error) {
	return a.step, nil
}

type echoCap struct{}

func (echoCap) Name() string { return "service.health_check" }

func (echoCap) Run(in map[string]string) (map[string]string, error) {
	return map[string]string{"status": in["status"]}, nil
}

func TestLoopDoneWhenWorldAlreadyHealthy(t *testing.T) {
	world := &memWorld{state: map[string]string{"svc": "healthy"}}
	loop := autonomy.Loop{
		Agent:    fixedAgent{step: autonomy.Step{Capability: "service.health_check"}},
		Runtime:  autonomy.NewRuntime(echoCap{}),
		World:    world,
		Verifier: autonomy.StateVerifier{},
		MaxSteps: 3,
	}
	task := autonomy.Task{
		ID: "t1", Goal: "service healthy", Asset: "svc",
		Contract: autonomy.Contract{ExpectedState: "healthy"},
	}
	if err := loop.Run(task); err != nil {
		t.Fatal(err)
	}
}

func TestLoopFailsWhenWorldStaysUnhealthy(t *testing.T) {
	world := &memWorld{state: map[string]string{"svc": "unhealthy"}}
	loop := autonomy.Loop{
		Agent: fixedAgent{step: autonomy.Step{
			Capability: "service.health_check",
			Input:      map[string]string{"status": "unhealthy"},
		}},
		Runtime:  autonomy.NewRuntime(echoCap{}),
		World:    world,
		Verifier: autonomy.StateVerifier{},
		MaxSteps: 2,
	}
	task := autonomy.Task{
		ID: "t2", Goal: "service healthy", Asset: "svc",
		Contract: autonomy.Contract{ExpectedState: "healthy"},
	}
	if err := loop.Run(task); err == nil {
		t.Fatal("expected incomplete task error")
	}
}
