package autonomy

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability"
)

// fakeCapability stands in for a real one (code_edit, asset.change, …) and reports
// an output map, like they do.
type fakeCapability struct {
	name string
	out  map[string]string
	err  error
	in   map[string]string
}

func (c *fakeCapability) Name() string        { return c.name }
func (c *fakeCapability) Domain() string      { return "test" }
func (c *fakeCapability) Provider() string    { return "test" }
func (c *fakeCapability) Description() string { return "test capability" }

func (c *fakeCapability) Run(in map[string]string) (map[string]string, error) {
	c.in = in
	return c.out, c.err
}

func withFakeCapability(t *testing.T, c *fakeCapability) {
	t.Helper()
	prev := _autonomy
	f := capability.NewFactory()
	f.Register(c)
	_autonomy = &Autonomy{CapabilityFactory: f}
	t.Cleanup(func() { _autonomy = prev })
}

// TestCapabilityOutputReachesTheCycleResult: an action hands its capability's
// output to the cycle result, which is what the next decision reads as
// previous_actions -> output.
func TestCapabilityOutputReachesTheCycleResult(t *testing.T) {
	fake := &fakeCapability{name: "fake", out: map[string]string{"status": "ok", "summary": "renamed the events"}}
	withFakeCapability(t, fake)

	rt := NewRuntime(NewAgentFactory())
	task := &Task{ID: "task-1", Description: "standardise events"}
	result, err := rt.Execute(Decision{
		Type:    "plan",
		Ctx:     DecisionContext{Task: task},
		Actions: []Action{CapabilityAction{Name: "fake", Input: map[string]string{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 1 {
		t.Fatalf("result.Actions=%+v, want one record", result.Actions)
	}
	record := result.Actions[0]
	if record.Capability != "fake" {
		t.Fatalf("record.Capability=%q", record.Capability)
	}
	// The record keeps the input the capability was actually called with (task
	// defaults included) and its output verbatim.
	if record.Input["task_id"] != "task-1" || record.Input["instruction"] != "standardise events" {
		t.Fatalf("record.Input=%v, want the effective input", record.Input)
	}
	if record.Output["summary"] != "renamed the events" || record.Output["status"] != "ok" {
		t.Fatalf("record.Output=%v, want the capability's own output", record.Output)
	}
	if record.Error != "" {
		t.Fatalf("record.Error=%q, want empty", record.Error)
	}

	// …and it is what the next decision sees.
	raw := string(formatRuntimeContextJSON(DecisionContext{History: []Result{result}}, ReasoningInput{}))
	var dumped struct {
		Previous []struct {
			Status  string `json:"status"`
			Actions []struct {
				Capability string            `json:"capability"`
				Input      map[string]string `json:"input"`
				Output     map[string]string `json:"output"`
			} `json:"actions"`
		} `json:"previous_actions"`
	}
	if err := json.Unmarshal([]byte(raw), &dumped); err != nil {
		t.Fatal(err)
	}
	if len(dumped.Previous) != 1 || dumped.Previous[0].Status != "ok" || len(dumped.Previous[0].Actions) != 1 {
		t.Fatalf("previous_actions=%s", raw)
	}
	seen := dumped.Previous[0].Actions[0]
	if seen.Capability != "fake" || seen.Output["summary"] != "renamed the events" || seen.Input["task_id"] != "task-1" {
		t.Fatalf("previous_actions action=%+v, want the raw input and output", seen)
	}
}

// TestFailedCapabilityOutputIsKept: a capability that produced something before
// failing still reports it, so a failed cycle is diagnosable from the prompt.
func TestFailedCapabilityOutputIsKept(t *testing.T) {
	fake := &fakeCapability{name: "fake", out: map[string]string{"summary": "half done"}, err: errors.New("boom")}
	withFakeCapability(t, fake)

	result, err := NewRuntime(NewAgentFactory()).Execute(Decision{
		Type:    "plan",
		Actions: []Action{CapabilityAction{Name: "fake"}},
	})
	if err == nil {
		t.Fatal("expected the capability's error")
	}
	if len(result.Actions) != 1 {
		t.Fatalf("result.Actions=%+v, want the failed action's record", result.Actions)
	}
	if result.Actions[0].Output["summary"] != "half done" {
		t.Fatalf("record.Output=%v, want the partial output of the failed action", result.Actions[0].Output)
	}
	if result.Actions[0].Error != "boom" {
		t.Fatalf("record.Error=%q, want the failure", result.Actions[0].Error)
	}
	if !strings.Contains(result.Message, "fake") {
		t.Fatalf("result.Message=%q, want it to name the capability", result.Message)
	}
}
