package autonomy

import (
	"errors"
	"strings"
	"testing"
)

// The type-specific requirements are not only asked for in the prompt: the runtime
// refuses a decision that breaks one, with the rule named, and the cycle fails so the
// planner re-plans. These tests pin each rule, the error type that carries it, and the
// gate: a decision that breaks its contract executes nothing.

func TestValidateDecisionAcceptsWhatTheContractAsksFor(t *testing.T) {
	decisions := []Decision{
		{Type: "plan", Actions: []Action{
			CapabilityAction{Name: "code_edit", StepName: "edit", ExpectedEffect: "the change is in the workspace"},
		}},
		{Type: "plan", Actions: []Action{
			CapabilityAction{Name: "code_edit", ExpectedEffect: "the change is in the workspace", EvidenceRefs: []string{"E1"}},
		}, Evidence: []Evidence{{ID: "E1", Fact: "the task asks for it"}}},
		{Type: "done", Evidence: []Evidence{{ID: "E1", Fact: "the goal is satisfied"}}},
		{Type: "blocked", Need: Need{Type: "capability", Description: "nothing available can do this"}},
		{Type: "need_input", Need: Need{Type: "decision", Description: "which environment?"}},
		// Options are optional: when given they are the choice handed to the owner.
		{Type: "blocked", Need: Need{Type: "capability", Description: "nothing available can do this", Options: []string{"merge PR #136 as it is", "rework step 2 first"}}},
		{Type: "need_input", Need: Need{Type: "decision", Description: "which environment?", Options: []string{"staging", "production"}}},
	}
	for _, decision := range decisions {
		if err := validateDecision(decision); err != nil {
			t.Errorf("decision %+v was refused: %v", decision, err)
		}
	}
}

func TestValidateDecisionNamesTheRuleItBreaks(t *testing.T) {
	cases := []struct {
		name     string
		decision Decision
		rule     string
		detail   string
	}{
		{
			name:     "an empty plan",
			decision: Decision{Type: "plan"},
			rule:     "plan.not_empty",
			detail:   "the plan has no steps",
		},
		{
			name: "a step that does not say what it is for",
			decision: Decision{Type: "plan", Actions: []Action{
				CapabilityAction{Name: "code_edit", StepName: "edit"},
			}},
			rule:   "plan.steps_say_what_they_are_for",
			detail: `step "edit" does not say what it is for`,
		},
		{
			name: "an evidence_ref nobody carries",
			decision: Decision{Type: "plan", Actions: []Action{
				CapabilityAction{Name: "code_edit", ExpectedEffect: "x", EvidenceRefs: []string{"E3"}},
			}, Evidence: []Evidence{{ID: "E1"}}},
			rule:   "plan.evidence_refs_exist",
			detail: `cites evidence "E3"`,
		},
		{
			name: "a done that carries steps",
			decision: Decision{Type: "done", Evidence: []Evidence{{ID: "E1"}},
				Actions: []Action{CapabilityAction{Name: "code_edit", ExpectedEffect: "x"}}},
			rule:   "done.plan_empty",
			detail: "the answer carries 1 step(s)",
		},
		{
			name:     "a done that also asks for something",
			decision: Decision{Type: "done", Evidence: []Evidence{{ID: "E1"}}, Need: Need{Description: "and also…"}},
			rule:     "done.need_empty",
			detail:   "done carries no need",
		},
		{
			name:     "a done that proves nothing",
			decision: Decision{Type: "done"},
			rule:     "done.evidence_present",
			detail:   "carries no evidence",
		},
		{
			name:     "a blocked that says nothing is missing",
			decision: Decision{Type: "blocked"},
			rule:     "blocked.need_describes_what_is_missing",
			detail:   "does not say what is missing",
		},
		{
			name:     "a done that carries options",
			decision: Decision{Type: "done", Evidence: []Evidence{{ID: "E1"}}, Need: Need{Options: []string{"a", "b"}}},
			rule:     "done.need_empty",
			detail:   "done carries no need",
		},
		{
			name:     "a blocked whose options are one option",
			decision: Decision{Type: "blocked", Need: Need{Description: "PR #136 must be merged", Options: []string{"merge it"}}},
			rule:     "blocked.need_options_are_choices",
			detail:   "one option is not a choice",
		},
		{
			name:     "a blocked whose options repeat",
			decision: Decision{Type: "blocked", Need: Need{Description: "PR #136 must be merged", Options: []string{"merge it", "merge it"}}},
			rule:     "blocked.need_options_are_choices",
			detail:   "repeats",
		},
		{
			name:     "a need_input whose option is empty",
			decision: Decision{Type: "need_input", Need: Need{Description: "which environment?", Options: []string{"staging", "  "}}},
			rule:     "need_input.need_options_are_choices",
			detail:   "is empty",
		},
		{
			name: "a need_input with a menu",
			decision: Decision{Type: "need_input", Need: Need{Description: "which environment?",
				Options: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}}},
			rule:   "need_input.need_options_are_choices",
			detail: "at most 9",
		},
		{
			name: "a need_input that executes",
			decision: Decision{Type: "need_input", Need: Need{Description: "which environment?"},
				Actions: []Action{CapabilityAction{Name: "code_edit", ExpectedEffect: "x"}}},
			rule:   "need_input.plan_empty",
			detail: "the answer carries 1 step(s)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDecision(tc.decision)
			if err == nil {
				t.Fatal("the decision was accepted")
			}
			var violation *DecisionViolation
			if !errors.As(err, &violation) {
				t.Fatalf("err=%v (%T), want a *DecisionViolation the caller can classify", err, err)
			}
			if violation.Rule != tc.rule {
				t.Errorf("rule=%q, want %q", violation.Rule, tc.rule)
			}
			if violation.DecisionType != tc.decision.Type {
				t.Errorf("decision type=%q, want %q", violation.DecisionType, tc.decision.Type)
			}
			if !strings.Contains(violation.Detail, tc.detail) {
				t.Errorf("detail=%q, want it to mention %q", violation.Detail, tc.detail)
			}
			if !strings.Contains(err.Error(), tc.rule) {
				t.Errorf("error %q does not name the rule", err.Error())
			}
		})
	}
}

// An unknown or empty type is left to the parser, which refuses it before a decision
// reaches the runtime — the rules are per documented type.
func TestValidateDecisionLeavesUnknownTypesAlone(t *testing.T) {
	for _, typ := range []string{"", "delete_everything"} {
		if err := validateDecision(Decision{Type: typ}); err != nil {
			t.Errorf("type %q: %v", typ, err)
		}
	}
}

// TestARefusedDecisionExecutesNothing: the gate is in Runtime.Execute, ahead of the
// plan's lineage, so a decision that breaks its contract writes no rows and runs no
// capability — and the reason reaches the planner in the cycle's result.
func TestARefusedDecisionExecutesNothing(t *testing.T) {
	store := executionTestStore(t)
	fake := &fakeCapability{name: "fake", out: map[string]string{"state": "changed"}}
	withFakeCapability(t, fake)

	result, err := NewRuntime(NewAgentFactory()).Execute(Decision{
		Type: "plan",
		Ctx:  executionContext(),
		Actions: []Action{
			CapabilityAction{Name: "fake"}, // no expected_effect: the step says nothing about what it is for
		},
	})
	if err == nil {
		t.Fatal("a step that says nothing about its purpose was executed")
	}
	if fake.in != nil {
		t.Fatalf("the capability ran anyway: %v", fake.in)
	}
	if !strings.Contains(result.Message, "plan.steps_say_what_they_are_for") {
		t.Fatalf("result.Message=%q, want the rule that was broken", result.Message)
	}
	plans, err := store.ListExecutionPlans("task-exec")
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 0 {
		t.Fatalf("plans=%+v, want none: a refused decision is not planned", plans)
	}
}
