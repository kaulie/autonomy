package autonomy

import "github.com/kaulie/autonomy/src/llmbackend"

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability"
)

// The execution tables: a plan is written before its steps run, is never rewritten,
// and its outcome is derived from the steps. These tests pin the writing order, the
// immutability and the traceability — not the storage (that is the engine's job).

// executionTestStore opens a store and points the package's active store at it, so
// persistTask / saveExecutionPlan write here.
func executionTestStore(t *testing.T) rawStore {
	t.Helper()
	store, err := openStore(t, filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	prev := _store
	t.Cleanup(func() {
		_store = prev
		_ = store.Close()
	})
	_store = store
	return store
}

// fakeAction is a plan step that does nothing but say how it ended, so a test can
// drive Runtime.Execute without a capability.
type fakeAction struct {
	name string
	err  error
	ran  bool
}

func (a *fakeAction) Execute(DecisionContext) (ActionResult, error) {
	a.ran = true
	// A failed step keeps what it produced before failing, so the record of a step
	// that went wrong is as complete as the one of a step that went right.
	return ActionResult{
		Capability: a.name,
		Input:      map[string]string{"in": "1"},
		Output:     map[string]string{"out": "2"},
	}, a.err
}

func executionContext() DecisionContext {
	return DecisionContext{
		Task:  &Task{ID: "task-exec"},
		Agent: &Agent{ID: 7, Name: "agent-7"},
		Cycle: 1,
	}
}

func TestExecuteWritesThePlanBeforeItsSteps(t *testing.T) {
	store := executionTestStore(t)
	rt := NewRuntime(NewAgentFactory())
	first := &fakeAction{name: "first", err: errors.New("boom")}
	second := &fakeAction{name: "second"}

	result, err := rt.Execute(Decision{
		Type:    "plan",
		Reason:  "two steps, the first one fails",
		Actions: []Action{first, second},
		Ctx:     executionContext(),
	})
	if err == nil {
		t.Fatal("Execute succeeded; the first step failed")
	}
	if len(result.Actions) != 1 || result.Actions[0].ExecutionStepID == 0 || result.Actions[0].PlanStepID == 0 {
		t.Fatalf("the failed action's record=%+v, want ids of the rows it is", result.Actions)
	}
	if second.ran {
		t.Fatal("the step after the failing one ran; a plan stops at its first failure")
	}

	plans, err := store.ListExecutionPlans("task-exec")
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans=%v err=%v, want one", plans, err)
	}
	plan := plans[0]
	if plan.StepCount != 2 || plan.DecisionType != "plan" || plan.Reason == "" {
		t.Fatalf("plan=%+v", plan)
	}

	// Both steps were planned before either ran; only the first one has a record of
	// having run, which is exactly how "planned but never executed" reads.
	planned, err := store.ListExecutionStepPlan(plan.ID)
	if err != nil || len(planned) != 2 {
		t.Fatalf("planned=%v err=%v, want both steps", planned, err)
	}
	executed, err := store.ListExecutionSteps(plan.ID)
	if err != nil || len(executed) != 1 {
		t.Fatalf("executed=%v err=%v, want only the first step", executed, err)
	}
	if executed[0].PlanStepID != planned[0].ID || executed[0].Status != "failed" {
		t.Fatalf("step=%+v, want it linked to the first planned step and failed", executed[0])
	}
	if executed[0].Error != "boom" || executed[0].Input != `{"in":"1"}` || executed[0].Output != `{"out":"2"}` {
		t.Fatalf("step=%+v, want the reason and the actual input/output", executed[0])
	}

	// The plan's outcome is derived, not stored: the last step that ran, and how
	// much of the plan that was.
	outcome, found, err := store.ExecutionPlanOutcome(plan.ID)
	if err != nil || !found {
		t.Fatalf("outcome=%+v found=%v err=%v", outcome, found, err)
	}
	if outcome.Status != "failed" || outcome.Planned != 2 || outcome.Executed != 1 || outcome.StepID != executed[0].ID {
		t.Fatalf("outcome=%+v, want failed, 1 of 2 steps, from step %d", outcome, executed[0].ID)
	}
}

func TestAPlanWithNothingToExecuteIsStillWritten(t *testing.T) {
	store := executionTestStore(t)
	rt := NewRuntime(NewAgentFactory())

	if _, err := rt.Execute(Decision{Type: "need_input", Reason: "no credential", Need: Need{Type: "decision", Description: "set GITHUB_TOKEN"}, Ctx: executionContext()}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	plans, err := store.ListExecutionPlans("task-exec")
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans=%v err=%v, want the decision recorded too", plans, err)
	}
	if plans[0].DecisionType != "need_input" || plans[0].StepCount != 0 {
		t.Fatalf("plan=%+v", plans[0])
	}
	if !strings.Contains(plans[0].Need, "GITHUB_TOKEN") {
		t.Fatalf("plan.need=%q, want what it asked for", plans[0].Need)
	}
	if outcome, found, _ := store.ExecutionPlanOutcome(plans[0].ID); found {
		t.Fatalf("outcome=%+v, want none: nothing ran", outcome)
	}
}

// planFailStore is a store that cannot write a plan, to pin what that means: a plan
// is authoritative, so nothing runs when it cannot be recorded.
type planFailStore struct{ Store }

func (planFailStore) CreateExecutionPlan(ExecutionPlan) (int64, error) {
	return 0, errors.New("the plan could not be recorded")
}

func TestExecuteDoesNotRunWhenThePlanCannotBeWritten(t *testing.T) {
	store := executionTestStore(t)
	prev := _store
	t.Cleanup(func() { _store = prev })
	_store = planFailStore{store}

	action := &fakeAction{name: "first"}
	rt := NewRuntime(NewAgentFactory())
	if _, err := rt.Execute(Decision{Type: "plan", Actions: []Action{action}, Ctx: executionContext()}); err == nil {
		t.Fatal("Execute succeeded although the plan could not be written")
	}
	if action.ran {
		t.Fatal("the step ran although the plan it was supposed to carry out was never recorded")
	}
}

func TestAPlanIsOneShotAndItsRowsNeverChange(t *testing.T) {
	store := executionTestStore(t)
	rt := NewRuntime(NewAgentFactory())

	run := func() int64 {
		t.Helper()
		if _, err := rt.Execute(Decision{
			Type:    "plan",
			Reason:  "the same intention twice",
			Actions: []Action{&fakeAction{name: "asset.change"}},
			Ctx:     executionContext(),
		}); err != nil {
			t.Fatal(err)
		}
		plans, err := store.ListExecutionPlans("task-exec")
		if err != nil {
			t.Fatal(err)
		}
		return plans[len(plans)-1].ID
	}

	firstID := run()
	firstPlan, err := store.ListExecutionStepPlan(firstID)
	if err != nil {
		t.Fatal(err)
	}
	secondID := run()

	plans, err := store.ListExecutionPlans("task-exec")
	if err != nil || len(plans) != 2 {
		t.Fatalf("plans=%v err=%v, want two plans: a re-plan is a new plan", plans, err)
	}
	if plans[0].ID == plans[1].ID {
		t.Fatal("the second plan reused the first plan's id")
	}
	// Planning exactly the same thing is still a new plan — and the equal hash is
	// what says it was the same intention (a loop, not progress).
	if plans[0].PlanHash == "" || plans[0].PlanHash != plans[1].PlanHash {
		t.Fatalf("hashes=%q/%q, want the same fingerprint for the same steps", plans[0].PlanHash, plans[1].PlanHash)
	}
	// The first plan's rows are untouched: nothing rewrites a plan.
	again, err := store.ListExecutionStepPlan(firstID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(firstPlan) || again[0].ID != firstPlan[0].ID || again[0].Input != firstPlan[0].Input {
		t.Fatalf("the first plan changed: %+v (was %+v)", again, firstPlan)
	}
	if secondPlan, err := store.ListExecutionStepPlan(secondID); err != nil || secondPlan[0].ID == firstPlan[0].ID {
		t.Fatalf("the second plan reused the first plan's step rows (err=%v)", err)
	}
}

// plannerRun wires a real Run against the fake bridge, so the loop, the planner's
// reply and the execution records are all exercised together.
func plannerRun(t *testing.T, description string) (rawStore, *Autonomy) {
	t.Helper()
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	t.Setenv("AUTONOMY_REASONER", "llm")
	t.Setenv("AUTONOMY_MAX_STEPS", "2")
	t.Setenv("PROJECT_ROOT", filepath.Join(".."))

	store := executionTestStore(t)
	asset := Asset{ID: "asset-1", Kind: "repo", State: "healthy"}
	_world = &World{assetManager: &AssetManager{
		assets:     []Asset{asset},
		assetsByID: map[string]Asset{asset.ID: asset},
	}}
	t.Cleanup(func() { _world = nil })

	factory := capability.NewFactory()
	capability.RegisterDefaults(factory, capability.Deps{Assets: worldAssetMutator()})
	_autonomy = &Autonomy{CapabilityFactory: factory, Store: store}
	rtu := NewRuntime(NewAgentFactory(), factory.GetAll()...)
	return store, &Autonomy{AgentFactory: NewAgentFactory(), Runtime: rtu, Store: store}
}

// TestRunVerifiesADoneAgainstTheCompletionContract drives the whole chain: the planner
// answers `done` with the contract it declared, the runtime pins that contract, and the
// verdict is recorded against the world — so the task is `completed` with something
// behind it, not on the model's word (docs/verification.md).
func TestRunVerifiesADoneAgainstTheCompletionContract(t *testing.T) {
	store, rt := plannerRun(t, "answer done")

	req := AcceptTaskRequest{ID: "task-verifiable", Description: "answer done", Domain: string(TaskDomainServer), GoalType: GoalType_FEATURE}
	if _, err := rt.Run(req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var status string
	if err := store.RawDB().QueryRow(`SELECT status FROM tasks WHERE id = ?`, req.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != TaskStatusCompleted {
		t.Fatalf("status=%q, want %q", status, TaskStatusCompleted)
	}
	contract, err := store.ListCompletionContract(req.ID)
	if err != nil || len(contract) != 1 {
		t.Fatalf("completion_contract=%v err=%v, want the criterion the first answer declared", contract, err)
	}
	verdicts, err := store.ListVerifications(req.ID)
	if err != nil || len(verdicts) != 1 {
		t.Fatalf("verification=%v err=%v, want one verdict", verdicts, err)
	}
	verdict := verdicts[0]
	if verdict.Result != verificationPass || verdict.Method != "world_model" {
		t.Fatalf("verdict=%+v, want the World Model to have passed it", verdict)
	}
	if !strings.Contains(verdict.Observed, "healthy") {
		t.Fatalf("verdict=%+v, want what the world said", verdict)
	}
}

// TestRunEndsUnverifiedWhenTheContractDoesNotHold: the world does not satisfy the
// contract the planner declared, so every `done` is refused, the planner re-plans, and
// the run ends `unverified` — never `completed`. The refusals are on record: one plan row
// and one verdict per cycle (docs/verification.md).
func TestRunEndsUnverifiedWhenTheContractDoesNotHold(t *testing.T) {
	store, rt := plannerRun(t, "unverified answer")
	t.Setenv("AUTONOMY_MAX_STEPS", "4")

	req := AcceptTaskRequest{ID: "task-unverified", Description: "unverified answer", Domain: string(TaskDomainServer), GoalType: GoalType_FEATURE}
	_, err := rt.Run(req)
	if err == nil {
		t.Fatal("Run succeeded; the world never satisfied the contract")
	}
	if !strings.Contains(err.Error(), "verification refused the done") {
		t.Fatalf("err=%v, want the refusal", err)
	}

	verdicts, err := store.ListVerifications(req.ID)
	if err != nil || len(verdicts) != 4 {
		t.Fatalf("verification=%v err=%v, want one verdict per refused done (budget was 4)", verdicts, err)
	}
	for _, verdict := range verdicts {
		if verdict.Result != verificationFail {
			t.Fatalf("verdict=%+v, want every one of them failed", verdict)
		}
		if !strings.Contains(verdict.Reason, "healthy") {
			t.Fatalf("verdict=%+v, want what the world actually said", verdict)
		}
	}

	var status, taskError string
	if err := store.RawDB().QueryRow(`SELECT status, error FROM tasks WHERE id = ?`, req.ID).Scan(&status, &taskError); err != nil {
		t.Fatal(err)
	}
	if status != TaskStatusUnverified {
		t.Fatalf("status=%q, want %q: the run ended on a done that never held up", status, TaskStatusUnverified)
	}
	if !strings.Contains(taskError, "asset_degraded") {
		t.Fatalf("tasks.error=%q, want why the done did not hold up", taskError)
	}
}

// TestRunStopsWhenTheDecisionConcludesTheTask: done / blocked / need_input are answers,
// not plans — the run ends on them instead of spending the rest of its budget asking the
// planner the same question again (task-26 asked three times after its four steps had all
// succeeded), and the status the task keeps is the decision that concluded it.
func TestRunStopsWhenTheDecisionConcludesTheTask(t *testing.T) {
	cases := []struct {
		phrase string
		status string
	}{
		{"answer done", TaskStatusCompleted},
		{"answer blocked", TaskStatusBlocked},
		{"answer need_input", TaskStatusNeedInput},
	}
	for _, tc := range cases {
		t.Run(tc.phrase, func(t *testing.T) {
			store, rt := plannerRun(t, tc.phrase)
			t.Setenv("AUTONOMY_MAX_STEPS", "4")

			taskID := "task-" + strings.ReplaceAll(tc.phrase, " ", "-")
			req := AcceptTaskRequest{ID: taskID, Description: tc.phrase, Domain: string(TaskDomainServer), GoalType: GoalType_FEATURE}
			if _, err := rt.Run(req); err != nil {
				t.Fatalf("Run: %v", err)
			}

			var turns int
			if err := store.RawDB().QueryRow(`SELECT count(*) FROM reason_turns WHERE task_id = ?`, taskID).Scan(&turns); err != nil {
				t.Fatal(err)
			}
			if turns != 1 {
				t.Fatalf("reason_turns=%d, want the run to stop at the decision that concluded it (budget was 4)", turns)
			}
			plans, err := store.ListExecutionPlans(taskID)
			if err != nil || len(plans) != 1 {
				t.Fatalf("plans=%v err=%v, want the one decision recorded", plans, err)
			}
			concluded := strings.TrimPrefix(tc.phrase, "answer ")
			if plans[0].DecisionType != concluded || plans[0].StepCount != 0 {
				t.Fatalf("plan=%+v, want the %s decision and no steps", plans[0], concluded)
			}
			// Why a task is blocked is the decision's own need, on its plan row — the
			// status does not have to carry the reason.
			if concluded != "done" && !strings.Contains(plans[0].Need, "description") {
				t.Fatalf("plan=%+v, want the need the decision was blocked on", plans[0])
			}

			var status, taskError string
			if err := store.RawDB().QueryRow(`SELECT status, error FROM tasks WHERE id = ?`, taskID).Scan(&status, &taskError); err != nil {
				t.Fatal(err)
			}
			if status != tc.status {
				t.Fatalf("status=%q, want %q", status, tc.status)
			}
			if taskError != "" {
				t.Fatalf("tasks.error=%q, want empty: that field is why a run failed", taskError)
			}
		})
	}
}

// TestRunDoesNotStopOnAnAnswerTheRuntimeRefused: an answer stops a run because it is an
// answer, but only once it holds up. A `done` that proves nothing breaks
// done.evidence_present, which is the type's requirement, so that cycle fails like any
// other: nothing is written for it, nothing is executed, and the planner re-plans from
// the reason — the task is never completed on the model's word, and a run that ends with
// an answer that never held up ends `unverified`, not `completed`
// (docs/execution-loop.md, docs/verification.md).
func TestRunDoesNotStopOnAnAnswerTheRuntimeRefused(t *testing.T) {
	store, rt := plannerRun(t, "unproven answer")
	t.Setenv("AUTONOMY_MAX_STEPS", "4")

	req := AcceptTaskRequest{ID: "task-unproven", Description: "unproven answer", Domain: string(TaskDomainServer), GoalType: GoalType_FEATURE}
	_, err := rt.Run(req)
	if err == nil {
		t.Fatal("Run succeeded; the planner answered `done` without evidence every cycle")
	}
	// The run ends on the last cycle's failure, and that failure is the rule the answer
	// broke — not the answer's own claim.
	if !strings.Contains(err.Error(), "done.evidence_present") {
		t.Fatalf("err=%v, want the rule the refused answer broke", err)
	}

	var turns int
	if err := store.RawDB().QueryRow(`SELECT count(*) FROM reason_turns WHERE task_id = ?`, req.ID).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if turns != 4 {
		t.Fatalf("reason_turns=%d, want the run to re-plan after every refusal (budget was 4)", turns)
	}
	// A refused decision is neither written nor executed: there is no plan row, so
	// nothing records this task as having concluded anything.
	if plans, err := store.ListExecutionPlans(req.ID); err != nil || len(plans) != 0 {
		t.Fatalf("plans=%v err=%v, want none for a refused decision", plans, err)
	}

	var status, taskError string
	if err := store.RawDB().QueryRow(`SELECT status, error FROM tasks WHERE id = ?`, req.ID).Scan(&status, &taskError); err != nil {
		t.Fatal(err)
	}
	if status != TaskStatusUnverified {
		t.Fatalf("status=%q, want %q: the run ended on a `done` that never held up", status, TaskStatusUnverified)
	}
	if !strings.Contains(taskError, "done.evidence_present") {
		t.Fatalf("tasks.error=%q, want why the answer did not hold up", taskError)
	}
}

// TestRunRecordsThePlanAndWhatItTalkedTo drives the whole chain: the planner answers
// (through the fake bridge), the runtime writes the plan and executes its step, and
// afterwards the plan is traceable to the exact reply — and to the task's own first
// input — by message id rather than by cycle number.
func TestRunRecordsThePlanAndWhatItTalkedTo(t *testing.T) {
	store, rt := plannerRun(t, "give me a plan")
	req := AcceptTaskRequest{ID: "task-e2e", Description: "give me a plan", Domain: string(TaskDomainServer), GoalType: GoalType_FEATURE}
	if _, err := rt.Run(req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	plans, err := store.ListExecutionPlans("task-e2e")
	if err != nil || len(plans) == 0 {
		t.Fatalf("plans=%v err=%v", plans, err)
	}
	plan := plans[0]
	if plan.ReplyMessageID == 0 || plan.InputMessageID == 0 || plan.TaskInputMessageID == 0 || plan.ReasonTurnID == 0 {
		t.Fatalf("plan=%+v, want it traceable to the reply, its input and the task's input", plan)
	}
	// The reply the plan points at is the planner's own answer, and the plan it wrote
	// is what that answer said — checked against the message, not against a re-parse.
	var replyContent string
	if err := store.RawDB().QueryRow(`SELECT content FROM llm_messages WHERE id = ?`, plan.ReplyMessageID).Scan(&replyContent); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(replyContent, "the fake planner decided") {
		t.Fatalf("reply=%q, want the planner's answer the plan came from", replyContent)
	}
	if !strings.Contains(replyContent, `"capability":"asset.change"`) {
		t.Fatalf("reply=%q, want the plan it asked for", replyContent)
	}
	planned, err := store.ListExecutionStepPlan(plan.ID)
	if err != nil || len(planned) != 1 {
		t.Fatalf("planned=%v err=%v", planned, err)
	}
	if planned[0].Capability != "asset.change" || planned[0].Input != `{"target":"asset-1"}` {
		t.Fatalf("planned step=%+v, want what the reply asked for", planned[0])
	}

	executed, err := store.ListExecutionSteps(plan.ID)
	if err != nil || len(executed) != 1 {
		t.Fatalf("executed=%v err=%v", executed, err)
	}
	step := executed[0]
	if step.Status != "ok" || step.Capability != "asset.change" || step.Provider != "autonomy" {
		t.Fatalf("step=%+v, want the capability's own provider and its outcome", step)
	}
	if step.PlanStepID != planned[0].ID || step.Idx != 1 || step.TaskID != "task-e2e" {
		t.Fatalf("step=%+v, want it linked to the planned step and its task", step)
	}
	if outcome, found, err := store.ExecutionPlanOutcome(plan.ID); err != nil || !found || outcome.Status != "ok" {
		t.Fatalf("outcome=%+v found=%v err=%v", outcome, found, err)
	}

	// A second cycle is a second plan, and both point at the same task input.
	if len(plans) < 2 {
		t.Fatalf("plans=%d, want one per cycle", len(plans))
	}
	if plans[1].ID == plan.ID || plans[1].TaskInputMessageID != plan.TaskInputMessageID {
		t.Fatalf("second plan=%+v, want a new plan pointing at the task's same input %d", plans[1], plan.TaskInputMessageID)
	}
}

// TestAStepRecordsTheAgentItAcquiredAsAnInteraction: a step whose capability
// delegates records the worker run as an interaction of that step, so "what did this
// step talk to" is answerable from the execution tables alone.
func TestAStepRecordsTheAgentItAcquiredAsAnInteraction(t *testing.T) {
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	t.Setenv("PROJECT_ROOT", filepath.Join(".."))
	store := executionTestStore(t)

	rtu := NewRuntime(NewAgentFactory())
	factory := capability.NewFactory()
	capability.RegisterDefaults(factory, capability.Deps{Agents: rtu})
	rtu.SetCapabilities(factory.GetAll()...)
	_autonomy = &Autonomy{CapabilityFactory: factory, Store: store}

	result, err := rtu.Execute(Decision{
		Type:   "plan",
		Reason: "hand the work to a coding agent",
		Actions: []Action{CapabilityAction{
			Name:           "code_edit",
			Inputs:         literalInputs(map[string]string{"instruction": "do the thing"}),
			ExpectedEffect: "the change is in the workspace and its pull request is open",
		}},
		Ctx: executionContext(),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Output["summary"] == "" {
		t.Fatalf("result=%+v, want the delegated step to have run", result.Actions)
	}

	plans, err := store.ListExecutionPlans("task-exec")
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans=%v err=%v", plans, err)
	}
	steps, err := store.ListExecutionSteps(plans[0].ID)
	if err != nil || len(steps) != 1 {
		t.Fatalf("steps=%v err=%v", steps, err)
	}
	interactions, err := store.ListExecutionStepInteractions(steps[0].ID)
	if err != nil || len(interactions) != 1 {
		t.Fatalf("interactions=%v err=%v, want the worker run recorded on the step", interactions, err)
	}
	got := interactions[0]
	if got.Kind != InteractionLLM || got.Provider != string(llmbackend.Cline) || got.ReasonTurnID == 0 {
		t.Fatalf("interaction=%+v, want the LLM run this step talked to", got)
	}
	// The run it points at is the worker's own, not the planner's.
	var mode, taskID string
	if err := store.RawDB().QueryRow(`SELECT mode, task_id FROM reason_turns WHERE id = ?`, got.ReasonTurnID).Scan(&mode, &taskID); err != nil {
		t.Fatal(err)
	}
	if mode != string(ReasonModeAgent) || taskID != "task-exec" {
		t.Fatalf("run=%s/%s, want the worker's agent-mode run of this task", mode, taskID)
	}
}
