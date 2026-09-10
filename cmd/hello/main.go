package main

import (
	autonomy "github.com/kaulie/autonomy/src"
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

func (h *HealthCheck) Domain() string { return string(autonomy.TaskDomainServer) }

func (h *HealthCheck) Provider() string { return "demo" }

func (h *HealthCheck) Description() string {
	return "probe fake service health; returns status healthy|unhealthy"
}

func (h *HealthCheck) Run(map[string]string) (map[string]string, error) {
	return map[string]string{"status": h.Service.Get(h.Service.ID)}, nil
}

// fixedHealthCheck always selects service.health_check — V1 decision making is intentionally fixed.
type fixedHealthCheck struct{}

func (fixedHealthCheck) Decide(ctx autonomy.DecisionContext) (autonomy.Decision, error) {
	return autonomy.Decision{}, nil
}

func main() {
	svc := &FakeService{ID: "demo-api", Healthy: true}
	_ = autonomy.Task{
		ID:      "hello-health",
		Domain:  autonomy.TaskDomainServer,
		Context: "demo",
		Target:  svc.ID,
		Goal:    "demo-api is healthy",
	}
	_ = &HealthCheck{Service: svc}
}
