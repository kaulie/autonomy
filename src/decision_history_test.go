package autonomy

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// captureHistoryReasoner records the DecisionContext it was asked to decide in.
type captureHistoryReasoner struct {
	got *DecisionContext
}

func (r captureHistoryReasoner) Reason(ctx DecisionContext, _ ReasoningInput) (ReasoningResult, error) {
	*r.got = ctx
	return ReasoningResult{Decision: Decision{Type: "plan", Reason: "ok", Ctx: ctx}}, nil
}

// TestDecideAtStepPassesTheHistory: a decision sees what the task's earlier
// cycles did, so re-planning is not blind.
func TestDecideAtStepPassesTheHistory(t *testing.T) {
	agent := NewAgentFactory().Create(nil)
	var got DecisionContext
	agent.DecideMaker = &DecisionMaker{reasoner: captureHistoryReasoner{got: &got}}

	history := []Result{
		{Message: "executed 1 action(s)"},
		{Message: "action 1/1 failed: boom", Err: errors.New("boom")},
	}
	if _, err := agent.DecideAtStep(2, history); err != nil {
		t.Fatal(err)
	}
	if got.Step != 2 {
		t.Fatalf("step=%d, want 2", got.Step)
	}
	if len(got.History) != 2 {
		t.Fatalf("history=%+v, want the two earlier cycles", got.History)
	}
	if got.History[1].Err == nil || !strings.Contains(got.History[1].Message, "failed") {
		t.Fatalf("history[1]=%+v", got.History[1])
	}
}

// TestRuntimeContextRendersPreviousActions: the planner prompt carries the
// earlier cycles as previous_actions (status / error / output), which is what
// AGENT_V2's previous_action evidence source refers to.
func TestRuntimeContextRendersPreviousActions(t *testing.T) {
	type previousAction struct {
		Step    int               `json:"step"`
		Message string            `json:"message"`
		Status  string            `json:"status"`
		Error   string            `json:"error"`
		Output  map[string]string `json:"output"`
	}
	var dumped struct {
		Previous []previousAction `json:"previous_actions"`
	}

	ctx := DecisionContext{History: []Result{
		{Message: "executed 1 action(s)"},
		{Message: "action 1/1 failed: boom", Err: errors.New("boom"), Output: map[string]string{"summary": "half done"}},
	}}
	if err := json.Unmarshal(formatRuntimeContextJSON(ctx, ReasoningInput{}), &dumped); err != nil {
		t.Fatal(err)
	}
	if len(dumped.Previous) != 2 {
		t.Fatalf("previous_actions=%+v, want 2", dumped.Previous)
	}
	if dumped.Previous[0].Step != 1 || dumped.Previous[0].Status != "ok" {
		t.Fatalf("previous_actions[0]=%+v", dumped.Previous[0])
	}
	if dumped.Previous[1].Status != "failed" || dumped.Previous[1].Error != "boom" {
		t.Fatalf("previous_actions[1]=%+v", dumped.Previous[1])
	}
	if dumped.Previous[1].Output["summary"] != "half done" {
		t.Fatalf("previous_actions[1].output=%+v", dumped.Previous[1].Output)
	}

	// No earlier cycles: the key stays out entirely.
	if raw := string(formatRuntimeContextJSON(DecisionContext{}, ReasoningInput{})); strings.Contains(raw, "previous_actions") {
		t.Fatalf("empty history leaked previous_actions: %s", raw)
	}
}
