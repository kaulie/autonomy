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

// TestRuntimeExecuteWithoutActions: done / blocked / need_input decide nothing,
// which is not an error.
func TestRuntimeExecuteWithoutActions(t *testing.T) {
	rt := NewRuntime(NewAgentFactory())
	for _, typ := range []string{"done", "blocked", "need_input"} {
		result, err := rt.Execute(Decision{Type: typ})
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if !strings.Contains(result.Message, typ) {
			t.Fatalf("%s: result=%+v", typ, result)
		}
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
// reasoner (whose plan names asset.change without a target, so every cycle
// fails): with a budget of two cycles the task must decide twice instead of
// aborting after the first failure, and end reporting the failure.
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
}
