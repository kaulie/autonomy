package autonomy

import (
	"encoding/json"
	"strings"
	"testing"
)

// A caller can pin the Completion Contract at accept: the first write is the
// standard the task is judged by, so a later planner answer restating it
// changes nothing. Malformed JSON is a refused request, not a task with
// nothing to verify against.

func TestAcceptPinsCallerCompletionContract(t *testing.T) {
	store := resumeTestStore(t)
	f := NewAgentFactory()
	auto := &Autonomy{AgentFactory: f, Runtime: NewRuntime(f), Store: store}

	raw := json.RawMessage(`{"steps":[{"name":"merged_to_main","requirement":"merge to main; do not deploy"}]}`)
	accepted, err := auto.AcceptTask(AcceptTaskRequest{
		ID:                  "task-caller-contract",
		Description:         "ship it",
		GoalType:            GoalType_FEATURE,
		CompletionContracts: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.TaskID != "task-caller-contract" {
		t.Fatalf("task_id=%q", accepted.TaskID)
	}

	task, err := store.GetTask("task-caller-contract")
	if err != nil || task == nil {
		t.Fatalf("task=%v err=%v", task, err)
	}
	if task.GoalType != GoalType_FEATURE {
		t.Fatalf("goal_type=%q, want the type the request named", task.GoalType)
	}

	got, err := store.ListCompletionContract("task-caller-contract")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("pinned %d criteria, want 1", len(got))
	}
	if got[0].Idx != 1 || got[0].Name != "merged_to_main" || got[0].PlanID != 0 {
		t.Fatalf("criterion = %+v, want idx 1 / name merged_to_main / plan 0", got[0])
	}
	if !strings.Contains(got[0].Criterion, "merge to main") {
		t.Fatalf("criterion raw = %q, want the caller's words", got[0].Criterion)
	}

	// A later instruction restating a weaker contract does not replace the pin.
	if _, err := auto.AcceptTask(AcceptTaskRequest{
		ID:                  "task-caller-contract",
		Description:         "and again",
		CompletionContracts: json.RawMessage(`{"steps":["just open a PR"]}`),
	}); err != nil {
		t.Fatal(err)
	}
	again, err := store.ListCompletionContract("task-caller-contract")
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].Name != "merged_to_main" {
		t.Fatalf("restated contract replaced the pin: %+v", again)
	}
}

func TestAcceptRejectsMalformedCompletionContract(t *testing.T) {
	store := resumeTestStore(t)
	f := NewAgentFactory()
	auto := &Autonomy{AgentFactory: f, Runtime: NewRuntime(f), Store: store}

	_, err := auto.AcceptTask(AcceptTaskRequest{
		ID:                  "task-bad-contract",
		Description:         "ship it",
		CompletionContracts: json.RawMessage(`{"steps":1}`),
	})
	if err == nil || !strings.Contains(err.Error(), "completion_contracts") {
		t.Fatalf("err=%v, want a refused completion_contracts", err)
	}
	task, err := store.GetTask("task-bad-contract")
	if err != nil {
		t.Fatal(err)
	}
	if task != nil {
		t.Fatalf("malformed contract still wrote a task: %+v", task)
	}
}

func TestAcceptWithoutContractLeavesPinToPlanner(t *testing.T) {
	store := resumeTestStore(t)
	f := NewAgentFactory()
	auto := &Autonomy{AgentFactory: f, Runtime: NewRuntime(f), Store: store}

	if _, err := auto.AcceptTask(AcceptTaskRequest{
		ID:          "task-no-contract",
		Description: "ship it",
		GoalType:    GoalType_Resolve_ISSUE,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListCompletionContract("task-no-contract")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("pinned %d criteria, want none so the first planner answer still can", len(got))
	}
}
