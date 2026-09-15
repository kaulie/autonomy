package autonomy

import (
	"errors"
	"strings"
	"testing"
)

// recordingAction records that it ran, so a test can see which actions of a plan
// the runtime executed and in which order.
type recordingAction struct {
	name  string
	calls *[]string
	err   error
}

func (a recordingAction) Execute(DecisionContext) error {
	*a.calls = append(*a.calls, a.name)
	return a.err
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
