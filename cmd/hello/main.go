package main

import (
	"fmt"
	"os"

	"github.com/kaulie/autonomy/src"
)

// FakeService is a demo asset with a simple healthy/unhealthy state.
type FakeService struct {
	ID      string
	Healthy bool
}

func (s *FakeService) Get(assetID string) string {
	if assetID != s.ID {
		return "unknown"
	}
	if s.Healthy {
		return "healthy"
	}
	return "unhealthy"
}

// HealthCheck observes the fake service. Success of the call is not task completion.
type HealthCheck struct {
	Service *FakeService
}

func (h *HealthCheck) Name() string { return "service.health_check" }

func (h *HealthCheck) Run(map[string]string) (map[string]string, error) {
	return map[string]string{"status": h.Service.Get(h.Service.ID)}, nil
}

// Owner always picks service.health_check — V1 planning is intentionally fixed.
type Owner struct{}

func (Owner) Next(task autonomy.Task, _ autonomy.World) (autonomy.Step, error) {
	return autonomy.Step{
		Capability: "service.health_check",
		Input:      map[string]string{"asset": task.Asset},
	}, nil
}

func main() {
	svc := &FakeService{ID: "demo-api", Healthy: true}
	task := autonomy.Task{
		ID:      "hello-health",
		Goal:    "demo-api is healthy",
		Context: "demo",
		Asset:   svc.ID,
		Contract: autonomy.Contract{
			ExpectedState: "healthy",
		},
	}

	loop := autonomy.Loop{
		Agent:    Owner{},
		Runtime:  autonomy.NewRuntime(&HealthCheck{Service: svc}),
		World:    svc,
		Verifier: autonomy.StateVerifier{},
		MaxSteps: 3,
		OnEvent: func(e autonomy.Event) {
			fmt.Printf("[%s] %s\n", e.Type, e.Message)
		},
	}

	if err := loop.Run(task); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("completion contract satisfied")
}
