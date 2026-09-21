package autonomy

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaulie/autonomy/src/capability"
)

// blockingAction parks inside Execute until release is closed, so a test can prove
// the planner has already returned from dispatch while the action is still running.
type blockingAction struct {
	started chan struct{}
	release chan struct{}
}

func (a blockingAction) Execute(DecisionContext) (ActionResult, error) {
	close(a.started)
	<-a.release
	return ActionResult{
		Capability: "blocking",
		Input:      map[string]string{"ok": "1"},
		Output:     map[string]string{"ok": "1"},
	}, nil
}

// TestDispatchExecuteRunsOffThePlannerLoop: after Decide, Execute must not hold the
// caller's goroutine — dispatch returns while the action is still parked, and the
// Result arrives later as a cycleDone event.
func TestDispatchExecuteRunsOffThePlannerLoop(t *testing.T) {
	rt := &Autonomy{Runtime: NewRuntime(NewAgentFactory())}
	events := make(chan cycleDone, 1)
	started := make(chan struct{})
	release := make(chan struct{})

	rt.dispatchExecute(events, Decision{
		Type:    "plan",
		Actions: []Action{blockingAction{started: started, release: release}},
	})

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute never started on the worker")
	}

	select {
	case <-events:
		t.Fatal("cycleDone arrived before the action was released; Execute is still synchronous on the caller")
	default:
	}

	close(release)

	var ev cycleDone
	select {
	case ev = <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("cycleDone never arrived")
	}
	if ev.result.Err != nil {
		t.Fatalf("result.Err=%v", ev.result.Err)
	}
	if !strings.Contains(ev.result.Message, "blocking") && !strings.Contains(ev.result.Message, "1 action") {
		t.Fatalf("result=%+v, want the dispatched plan's outcome", ev.result)
	}
}

// TestRunObservesAfterCycleDone keeps the decide → execute → observe order for one
// task: a failed cycle is observed and the planner decides again (budget 2).
func TestRunObservesAfterCycleDone(t *testing.T) {
	store, err := openStore(filepath.Join(t.TempDir(), "autonomy.db"))
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
	req := AcceptTaskRequest{ID: "t-event-loop", Description: "d", Domain: string(TaskDomainServer), GoalType: GoalType_FEATURE}
	if _, err := rt.Run(req); err == nil {
		t.Fatal("Run returned nil; the last cycle failed")
	}
	var turns int
	if err := store.RawDB().QueryRow(`SELECT count(*) FROM reason_turns WHERE task_id = ?`, req.ID).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if turns != 2 {
		t.Fatalf("reason_turns=%d, want 2 (Result events must drive the next Decide)", turns)
	}
}
