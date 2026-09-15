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
// earlier cycles as previous_actions, one entry per cycle and one per action,
// each with its raw input/output (status / error / expected_effect / evidence
// refs included) — the thing AGENT_V2's previous_action evidence source refers
// to, and the consumer decides which of them matters.
func TestRuntimeContextRendersPreviousActions(t *testing.T) {
	type seenAction struct {
		Capability     string            `json:"capability"`
		Input          map[string]string `json:"input"`
		Output         map[string]string `json:"output"`
		Error          string            `json:"error"`
		ExpectedEffect string            `json:"expected_effect"`
		EvidenceRefs   []string          `json:"evidence_refs"`
	}
	type seenStep struct {
		Step    int          `json:"step"`
		Message string       `json:"message"`
		Status  string       `json:"status"`
		Error   string       `json:"error"`
		Actions []seenAction `json:"actions"`
	}
	var dumped struct {
		Previous []seenStep `json:"previous_actions"`
	}

	ctx := DecisionContext{History: []Result{
		{
			Message: "executed 1 action(s): code_edit",
			Actions: []ActionResult{{
				Capability: "code_edit",
				Input:      map[string]string{"instruction": "standardise the events"},
				Output:     map[string]string{"summary": "renamed the events"},
			}},
		},
		{
			Message: "action 1/1 (code_edit) failed: boom",
			Err:     errors.New("boom"),
			Actions: []ActionResult{{
				Capability:     "code_edit",
				Output:         map[string]string{"summary": "half done"},
				Error:          "boom",
				ExpectedEffect: "renames the events",
				EvidenceRefs:   []string{"E1"},
			}},
		},
	}}
	if err := json.Unmarshal(formatRuntimeContextJSON(ctx, ReasoningInput{}), &dumped); err != nil {
		t.Fatal(err)
	}
	if len(dumped.Previous) != 2 {
		t.Fatalf("previous_actions=%+v, want 2 cycles", dumped.Previous)
	}
	first := dumped.Previous[0]
	if first.Step != 1 || first.Status != "ok" || len(first.Actions) != 1 {
		t.Fatalf("previous_actions[0]=%+v", first)
	}
	if first.Actions[0].Input["instruction"] != "standardise the events" ||
		first.Actions[0].Output["summary"] != "renamed the events" {
		t.Fatalf("previous_actions[0].actions[0]=%+v, want its raw input and output", first.Actions[0])
	}
	second := dumped.Previous[1]
	if second.Status != "failed" || second.Error != "boom" {
		t.Fatalf("previous_actions[1]=%+v", second)
	}
	failed := second.Actions[0]
	if failed.Error != "boom" || failed.Output["summary"] != "half done" ||
		failed.ExpectedEffect != "renames the events" || len(failed.EvidenceRefs) != 1 {
		t.Fatalf("previous_actions[1].actions[0]=%+v, want the failed action's record", failed)
	}

	// No earlier cycles: the key stays out entirely.
	if raw := string(formatRuntimeContextJSON(DecisionContext{}, ReasoningInput{})); strings.Contains(raw, "previous_actions") {
		t.Fatalf("empty history leaked previous_actions: %s", raw)
	}
}
