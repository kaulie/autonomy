package autonomy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestTaskProgressIncludesPlanStepsAndExecution(t *testing.T) {
	store, err := openStore(t, filepath.Join(t.TempDir(), "progress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpsertTask(&Task{ID: "task-p", Description: "d", Status: TaskStatusRunning, Domain: TaskDomainServer}); err != nil {
		t.Fatal(err)
	}
	planID, err := store.CreateExecutionPlan(ExecutionPlan{
		TaskID: "task-p", Cycle: 1, DecisionType: "plan", Reason: "do it", StepCount: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendExecutionStepPlans([]ExecutionStepPlan{
		{PlanID: planID, Idx: 1, Name: "edit", Capability: "code_edit", Input: `{"instruction":"add endpoint"}`, ExpectedEffect: "code changed"},
		{PlanID: planID, Idx: 2, Name: "review", Capability: "pull_request.review", Input: `{"from":"feature"}`, ExpectedEffect: "reviewed"},
	}); err != nil {
		t.Fatal(err)
	}
	planned, err := store.ListExecutionStepPlan(planID)
	if err != nil || len(planned) != 2 {
		t.Fatalf("planned=%v err=%v", planned, err)
	}
	if _, err := store.AppendExecutionStep(ExecutionStep{
		PlanID: planID, PlanStepID: planned[0].ID, TaskID: "task-p", Idx: 1, Name: "edit",
		Capability: "code_edit", Status: "ok", Input: `{"instruction":"add endpoint"}`,
		Output: `{"summary":"done"}`, DurationMS: 12,
	}); err != nil {
		t.Fatal(err)
	}

	rt := &Autonomy{Store: store}
	progress, err := rt.TaskProgress("task-p")
	if err != nil || progress == nil || len(progress.Plans) != 1 {
		t.Fatalf("progress=%+v err=%v", progress, err)
	}
	plan := progress.Plans[0]
	if plan.DecisionType != "plan" || plan.StepCount != 2 || len(plan.Steps) != 2 {
		t.Fatalf("plan=%+v", plan)
	}
	if plan.Steps[0].Status != "ok" || plan.Steps[0].Capability != "code_edit" || !json.Valid(plan.Steps[0].Output) {
		t.Fatalf("step0=%+v", plan.Steps[0])
	}
	if plan.Steps[1].Status != "pending" || plan.Steps[1].Name != "review" {
		t.Fatalf("step1=%+v, want pending review", plan.Steps[1])
	}
}

func TestStopTaskCancelsTheRun(t *testing.T) {
	store, err := openStore(t, filepath.Join(t.TempDir(), "stop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	t.Cleanup(func() { _store = prev })
	_store = store

	if err := store.UpsertTask(&Task{ID: "task-stop", Description: "d", Status: TaskStatusRunning}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	inFlightTasks.Store("task-stop", cancel)
	t.Cleanup(func() {
		cancel()
		inFlightTasks.Delete("task-stop")
	})

	rt := &Autonomy{Store: store, AgentFactory: NewAgentFactory(), Runtime: NewRuntime(NewAgentFactory())}
	stopped, err := rt.StopTask("task-stop")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != TaskStatusStopped {
		t.Fatalf("status=%q", stopped.Status)
	}
	if ctx.Err() == nil {
		t.Fatal("stop did not cancel the run context")
	}

	agent := rt.AgentFactory.Create(&Task{ID: "task-stop", Description: "d"})
	agent.Session = NewLLMSession(rt.Runtime, agent, SessionOpts{TaskID: "task-stop"})
	err = rt.runLoop(ctx, agent, &Task{ID: "task-stop", Description: "d", Status: TaskStatusRunning, Domain: TaskDomainServer, GoalType: GoalType_FEATURE}, "d")
	if err == nil {
		t.Fatal("run returned nil after stop")
	}
	got, err := store.GetTask("task-stop")
	if err != nil || got == nil || got.Status != TaskStatusStopped {
		t.Fatalf("stored=%+v err=%v, want stopped", got, err)
	}

	srv := NewHTTPServer(rt)
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/missing/stop", nil)
	req.SetPathValue("taskID", "missing")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing stop status=%d body=%s", rec.Code, rec.Body.String())
	}
}
