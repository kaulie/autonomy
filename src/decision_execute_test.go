package autonomy

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability"
)

// recordingAction records that it ran, so a test can see which actions of a plan
// the runtime executed, in which order, and what input/output each report.
type recordingAction struct {
	name  string
	calls *[]string
	err   error
	out   map[string]string
}

func (a recordingAction) Execute(DecisionContext) (ActionResult, error) {
	*a.calls = append(*a.calls, a.name)
	out := a.out
	if out == nil {
		out = map[string]string{"ran_" + a.name: a.name}
	}
	return ActionResult{
		Capability: a.name,
		Input:      map[string]string{"for": a.name},
		Output:     out,
	}, a.err
}

// TestRuntimeExecutesEveryPlanActionInOrder: a decision carries the whole plan,
// so the runtime runs all of its steps in order, not just the first one.
func TestRuntimeExecutesEveryPlanActionInOrder(t *testing.T) {
	var calls []string
	rt := NewRuntime(NewAgentFactory())
	result, err := rt.Execute(Decision{
		Type: "plan",
		Actions: []Action{
			recordingAction{name: "a", calls: &calls},
			recordingAction{name: "b", calls: &calls},
			recordingAction{name: "c", calls: &calls},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(calls, ","); got != "a,b,c" {
		t.Fatalf("executed %q, want a,b,c", got)
	}
	if !strings.Contains(result.Message, "3 action") {
		t.Fatalf("result=%+v, want it to report 3 actions", result)
	}
	if len(result.Actions) != 3 {
		t.Fatalf("result.Actions=%+v, want one record per action", result.Actions)
	}
	for i, name := range []string{"a", "b", "c"} {
		record := result.Actions[i]
		if record.Capability != name || record.Input["for"] != name || record.Output["ran_"+name] != name {
			t.Fatalf("result.Actions[%d]=%+v, want that action's own input and output", i, record)
		}
	}
	if !strings.Contains(result.Message, "executed 3 action(s)") {
		t.Fatalf("result.Message=%q, want it to count what it ran", result.Message)
	}
}

// TestRuntimeStopsAtTheFirstFailingAction: a failed step ends the cycle, so the
// next decision observes the world it left behind.
func TestRuntimeStopsAtTheFirstFailingAction(t *testing.T) {
	var calls []string
	boom := errors.New("boom")
	rt := NewRuntime(NewAgentFactory())
	_, err := rt.Execute(Decision{
		Type: "plan",
		Actions: []Action{
			recordingAction{name: "a", calls: &calls},
			recordingAction{name: "b", calls: &calls, err: boom},
			recordingAction{name: "c", calls: &calls},
		},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err=%v, want the action's own error", err)
	}
	if got := strings.Join(calls, ","); got != "a,b" {
		t.Fatalf("executed %q, want a,b (c must not run)", got)
	}
}

// TestRuntimeExecuteWithoutActions: blocked / need_input decide nothing, which is not
// an error — with the decision's own contract satisfied (they say what is missing:
// src/decision_rules.go). A `done` is the one answer the runtime verifies, so with no
// Completion Contract pinned there is nothing to verify it against and it is refused
// rather than taken on the model's word (src/verification.go).
func TestRuntimeExecuteWithoutActions(t *testing.T) {
	rt := NewRuntime(NewAgentFactory())
	decisions := []Decision{
		{Type: "blocked", Need: Need{Type: "capability", Description: "no capability can do this"}},
		{Type: "need_input", Need: Need{Type: "decision", Description: "which environment?"}},
	}
	for _, decision := range decisions {
		result, err := rt.Execute(decision)
		if err != nil {
			t.Fatalf("%s: %v", decision.Type, err)
		}
		if !strings.Contains(result.Message, decision.Type) {
			t.Fatalf("%s: result=%+v", decision.Type, result)
		}
	}

	result, err := rt.Execute(Decision{
		Type:     "done",
		Evidence: []Evidence{{ID: "E1", Source: "observation", Fact: "the goal is satisfied"}},
	})
	if err == nil {
		t.Fatalf("a done with no pinned contract was accepted: result=%+v", result)
	}
	if !strings.Contains(err.Error(), "no completion contract") {
		t.Fatalf("err=%v, want the missing contract", err)
	}
}

// TestExecuteDecisionFailureDoesNotFailTheTask: a plan that stops at a failing
// action leaves the task alive — the result carries the error, and it is the
// task's *last* cycle that decides the final status (see Autonomy.Run).
func TestExecuteDecisionFailureDoesNotFailTheTask(t *testing.T) {
	var calls []string
	boom := errors.New("action exploded")
	rt := &Autonomy{Runtime: NewRuntime(NewAgentFactory()), MaxSteps: 2}
	result := rt.executeDecision(&Agent{ID: 7001, Name: "agent-7001"}, Decision{
		Type:    "plan",
		Actions: []Action{recordingAction{name: "a", calls: &calls, err: boom}},
	})
	if !errors.Is(result.Err, boom) {
		t.Fatalf("result.Err=%v, want the action's error", result.Err)
	}
	if !strings.Contains(result.Message, "1/1") {
		t.Fatalf("result=%+v, want it to name the failing action", result)
	}
	if got := strings.Join(calls, ","); got != "a" {
		t.Fatalf("executed %q", got)
	}
}

// TestMaxStepsComesFromTheEnvironment: the cycle budget can be raised without a
// rebuild (AUTONOMY_MAX_STEPS), and falls back to the struct/default otherwise.
func TestMaxStepsComesFromTheEnvironment(t *testing.T) {
	rt := &Autonomy{}
	if got := rt.maxSteps(); got != DefaultMaxSteps {
		t.Fatalf("default maxSteps=%d, want %d", got, DefaultMaxSteps)
	}
	rt.MaxSteps = 5
	if got := rt.maxSteps(); got != 5 {
		t.Fatalf("maxSteps=%d, want the struct's 5", got)
	}
	t.Setenv("AUTONOMY_MAX_STEPS", "3")
	if got := rt.maxSteps(); got != 3 {
		t.Fatalf("maxSteps=%d, want the environment's 3", got)
	}
	t.Setenv("AUTONOMY_MAX_STEPS", "nonsense")
	if got := rt.maxSteps(); got != 5 {
		t.Fatalf("maxSteps=%d, want the struct's 5 again", got)
	}
}

// TestRunKeepsDecidingAfterAFailedCycle drives the real loop with the local
// reasoner (whose plan names asset.change without supplying the target the
// capability requires, so every cycle is refused before it runs): with a budget of
// two cycles the task must decide twice instead of aborting after the first
// failure, and end reporting the failure.
func TestRunKeepsDecidingAfterAFailedCycle(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prevStore, prevAuto, prevFlag := _store, _autonomy, bootstrapFlag
	t.Cleanup(func() { _store, _autonomy, bootstrapFlag = prevStore, prevAuto, prevFlag })
	_store = store
	f := capability.NewFactory()
	capability.RegisterDefaults(f, capability.Deps{Assets: worldAssetMutator()})
	_autonomy = &Autonomy{CapabilityFactory: f, Store: store}
	bootstrapFlag = true
	t.Setenv("AUTONOMY_MAX_STEPS", "2")
	t.Setenv("AUTONOMY_REASONER", "local")

	rt := &Autonomy{AgentFactory: NewAgentFactory(), Runtime: NewRuntime(NewAgentFactory()), Store: store}
	task := &Task{ID: "t-fail-loop", Description: "d", Domain: TaskDomainServer, GoalType: GoalType_FEATURE, Status: "pending"}
	if err := rt.Run(task); err == nil {
		t.Fatal("Run returned nil; the last cycle failed")
	}
	var turns int
	if err := store.db.QueryRow(`SELECT count(*) FROM reason_turns WHERE task_id = ?`, task.ID).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if turns != 2 {
		t.Fatalf("reason_turns=%d, want 2 (the loop must decide again after a failed cycle)", turns)
	}

	// The row says why, not only that: the terminal that printed the failure is
	// not a record anyone can look at later.
	var status, reason string
	if err := store.db.QueryRow(`SELECT status, error FROM tasks WHERE id = ?`, task.ID).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "error" {
		t.Fatalf("status=%q want error", status)
	}
	if !strings.Contains(reason, "asset.change") || !strings.Contains(reason, "does not supply") {
		t.Fatalf("tasks.error=%q, want the plan refused for the input it never supplied", reason)
	}
}

// TestRunRecordsAnEmptyAnswerInTheStreamAndOnTheTask: the whole chain, from the
// provider ending a run with no answer to what a person can look up later — the
// task says why it failed, and the run's stream carries the reason too (the
// client stores the events itself, so a reason that only the result knew would be
// missing from the record).
func TestRunRecordsAnEmptyAnswerInTheStreamAndOnTheTask(t *testing.T) {
	installFakeClineClient(t)
	// The fake bridge answers an empty "finished" run to a prompt that says it is
	// out of balance; the task description travels in the delta, so it reaches it.
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	t.Setenv("AUTONOMY_REASONER", "llm")
	t.Setenv("PROJECT_ROOT", filepath.Join("..")) // the shipped agent policy

	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prevStore, prevAuto, prevFlag := _store, _autonomy, bootstrapFlag
	t.Cleanup(func() { _store, _autonomy, bootstrapFlag = prevStore, prevAuto, prevFlag })
	_store = store
	f := capability.NewFactory()
	capability.RegisterDefaults(f, capability.Deps{Assets: worldAssetMutator()})
	_autonomy = &Autonomy{CapabilityFactory: f, Store: store}
	bootstrapFlag = true

	rt := &Autonomy{AgentFactory: NewAgentFactory(), Runtime: NewRuntime(NewAgentFactory()), Store: store}
	task := &Task{ID: "t-out-of-balance", Description: "out of balance", Domain: TaskDomainServer, GoalType: GoalType_FEATURE, Status: "pending"}
	err = rt.Run(task)
	if err == nil || !strings.Contains(err.Error(), "empty model response") {
		t.Fatalf("Run=%v, want the empty answer to have failed the decide", err)
	}

	var status, reason string
	if err := store.db.QueryRow(`SELECT status, error FROM tasks WHERE id = ?`, task.ID).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "error" || !strings.Contains(reason, "Insufficient Balance") {
		t.Fatalf("task status/error=%q/%q, want the failure and its reason", status, reason)
	}

	// The reason is in the recorded stream as well, marked as sourced from the run
	// result: it arrived after the provider's own error event said nothing.
	var events int
	if err := store.db.QueryRow(`
SELECT count(*) FROM llm_events e JOIN reason_turns r ON r.id = e.turn_id
WHERE r.task_id = ? AND json_extract(e.payload, '$.source') = 'run_result'
  AND json_extract(e.payload, '$.error.message') = 'Insufficient Balance'`, task.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("run_result error events=%d, want exactly one carrying the reason", events)
	}
}

// nothing else to explain the failure — the reason the decide could not happen is
// the whole of it, and it has to reach the row.
func TestRunRecordsWhyItCouldNotDecide(t *testing.T) {
	// The session attaches (so the run gets as far as deciding); the prompt cannot
	// be built, which is what a deployment missing its agent policy looks like.
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	t.Setenv("AUTONOMY_REASONER", "llm")
	t.Setenv("PROJECT_ROOT", "")

	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prevStore, prevAuto, prevFlag := _store, _autonomy, bootstrapFlag
	t.Cleanup(func() { _store, _autonomy, bootstrapFlag = prevStore, prevAuto, prevFlag })
	_store = store
	f := capability.NewFactory()
	capability.RegisterDefaults(f, capability.Deps{Assets: worldAssetMutator()})
	_autonomy = &Autonomy{CapabilityFactory: f, Store: store}
	bootstrapFlag = true

	rt := &Autonomy{AgentFactory: NewAgentFactory(), Runtime: NewRuntime(NewAgentFactory()), Store: store}
	task := &Task{ID: "t-no-decide", Description: "d", Domain: TaskDomainServer, GoalType: GoalType_FEATURE, Status: "pending"}
	err = rt.Run(task)
	if err == nil || !strings.Contains(err.Error(), "decide:") {
		t.Fatalf("Run=%v, want the decide to have failed", err)
	}

	var status, reason string
	if err := store.db.QueryRow(`SELECT status, error FROM tasks WHERE id = ?`, task.ID).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "error" {
		t.Fatalf("status=%q want error", status)
	}
	for _, want := range []string{"decide:", "PROJECT_ROOT"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("tasks.error=%q, want it to name %q", reason, want)
		}
	}
}
