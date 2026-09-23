package autonomy

import (
	"context"
	"testing"
)

// TestRunPausesOnAPlanUntilApproved: an agent initialized with RequirePlanApproval
// plans first and the run pauses on that plan (status awaiting_approval, the plan on
// record, nothing implemented); only the confirmation releases it, and then the plan
// — not a fresh one — is what runs.
func TestRunPausesOnAPlanUntilApproved(t *testing.T) {
	store := executionTestStore(t)
	factory := NewAgentFactory()
	rt := NewRuntime(factory)
	auto := &Autonomy{AgentFactory: factory, Runtime: rt, Store: store, MaxSteps: 1}

	agent, err := NewAgentInitializer(factory).Initialize(AgentInitOptions{RequirePlanApproval: true})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	script := &scriptedReasoner{decisions: []Decision{{
		Type:    decisionPlanType,
		Reason:  "one step, then wait for the user",
		Actions: []Action{&fakeAction{name: "step-one"}},
	}}}
	agent.DecideMaker = &DecisionMaker{reasoner: script}

	task := &Task{ID: "task-approve", Description: "do the thing", Status: TaskStatusRunning, AgentID: agent.ID}
	agent.CurrentTask = task

	// Run 1: the agent plans, the run pauses, nothing is implemented.
	if err := auto.runLoop(context.Background(), agent, task, "do the thing"); err != nil {
		t.Fatalf("first runLoop: %v", err)
	}
	if task.Status != TaskStatusAwaitingApproval {
		t.Fatalf("status=%q want %q (the run paused for approval)", task.Status, TaskStatusAwaitingApproval)
	}
	if agent.pendingPlan == nil {
		t.Fatal("no plan was held for the user to confirm")
	}
	plans, err := store.ListExecutionPlans(task.ID)
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans=%v err=%v, want the one it paused on", plans, err)
	}
	planID := plans[0].ID
	if steps, err := store.ListExecutionSteps(planID); err != nil || len(steps) != 0 {
		t.Fatalf("steps=%v err=%v, want none: a paused plan has implemented nothing", steps, err)
	}

	// The confirmation releases it (processInstruction sets planApproved; the run for
	// it starts as running, like any instruction).
	agent.planApproved = true
	task.Status = TaskStatusRunning
	if err := auto.runLoop(context.Background(), agent, task, "do the thing"); err != nil {
		t.Fatalf("second runLoop: %v", err)
	}
	if agent.pendingPlan != nil {
		t.Fatal("the pending plan was not cleared")
	}
	if steps, err := store.ListExecutionSteps(planID); err != nil || len(steps) != 1 {
		t.Fatalf("steps=%v err=%v, want the approved plan to have run its one step", steps, err)
	}
	if script.calls != 1 {
		t.Fatalf("reasoner calls=%d want 1: an approved plan is executed, not planned again", script.calls)
	}
	if task.Status != TaskStatusCompleted {
		t.Fatalf("status=%q want %q after the approved plan ran", task.Status, TaskStatusCompleted)
	}
}
