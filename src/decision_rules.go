package autonomy

import (
	"fmt"
	"strings"
)

// Decision types, as AGENT_V2.md §Output Schema names them.
const (
	decisionPlan      = "plan"
	decisionDone      = "done"
	decisionBlocked   = "blocked"
	decisionNeedInput = "need_input"
)

// isDone reports whether a decision is the completion claim — the one decision the
// runtime verifies against the task's Completion Contract (src/verification.go).
func isDone(decision Decision) bool {
	return strings.EqualFold(strings.TrimSpace(decision.Type), decisionDone)
}

// The type-specific requirements AGENT_V2 states for each decision type, enforced by
// the runtime.
//
// The prompt asks for them; a prompt is a request, and it cannot be the only gate: a
// `done` that proves nothing would complete a task on the model's word, and a `plan`
// with no steps (or whose steps do not say what they are for) would burn a cycle
// executing nothing. So a decision that breaks its contract does not run at all, and
// the cycle fails with a reason the planner can act on: the next decision sees it in
// `previous_actions` and re-plans against the same goal.
//
// The requirements are rules per decision type, not branches in the loop: when the
// prompt states a new one — or the runtime needs one — it is a rule in decisionRules.
// A plan's input lineage is checked next to this, in src/plan_lineage.go.

// DecisionViolation is a decision that breaks a type-specific requirement. Rule is its
// stable name (e.g. "plan.not_empty") so a reader can find the requirement it is in
// AGENT_V2.md, and Detail says what in this decision broke it.
type DecisionViolation struct {
	// DecisionType is the answer the planner gave: plan | done | blocked | need_input.
	DecisionType string
	// Rule is the requirement's name.
	Rule string
	// Detail says what about this decision broke the rule.
	Detail string
}

func (v *DecisionViolation) Error() string {
	return fmt.Sprintf("decision %q violates %s: %s (AGENT_V2.md §Type-specific Requirements)",
		v.DecisionType, v.Rule, v.Detail)
}

// decisionRule is one requirement: check returns what broke it, or "" when the
// decision satisfies it.
type decisionRule struct {
	name  string
	check func(Decision) string
}

// decisionRules are the requirements of each decision type, in the order they are
// checked (the first violation is the one reported).
var decisionRules = map[string][]decisionRule{
	decisionPlan: {
		{name: "plan.not_empty", check: planHasSteps},
		{name: "plan.steps_say_what_they_are_for", check: planStepsSayWhatTheyAreFor},
		{name: "plan.evidence_refs_exist", check: planEvidenceRefsExist},
	},
	decisionDone: {
		{name: "done.plan_empty", check: noSteps},
		{name: "done.need_empty", check: noNeed},
		{name: "done.evidence_present", check: doneNamesEvidence},
	},
	decisionBlocked: {
		{name: "blocked.plan_empty", check: noSteps},
		{name: "blocked.need_describes_what_is_missing", check: needDescribesWhatIsMissing},
		{name: "blocked.need_options_are_choices", check: needOptionsAreChoices},
	},
	decisionNeedInput: {
		{name: "need_input.plan_empty", check: noSteps},
		{name: "need_input.need_describes_what_is_missing", check: needDescribesWhatIsMissing},
		{name: "need_input.need_options_are_choices", check: needOptionsAreChoices},
	},
}

// validateDecision runs the requirements of the decision's type and returns the first
// violation. A type with no rules (empty or unknown) is left to the other gates — the
// parser refuses an unknown type before a decision ever gets here.
func validateDecision(decision Decision) error {
	for _, rule := range decisionRules[strings.ToLower(strings.TrimSpace(decision.Type))] {
		if detail := rule.check(decision); detail != "" {
			return &DecisionViolation{DecisionType: decision.Type, Rule: rule.name, Detail: detail}
		}
	}
	return nil
}

// planHasSteps: a plan must execute something. An empty plan is not "no work needed"
// — done / blocked / need_input are how a decision says that.
func planHasSteps(decision Decision) string {
	if len(decision.Actions) > 0 {
		return ""
	}
	return "the plan has no steps: a plan executes something — when there is nothing to execute, the answer is done (with the evidence), blocked or need_input"
}

// planStepsSayWhatTheyAreFor: every step names the World State change it expects, so
// a step is a plan and not a gesture.
func planStepsSayWhatTheyAreFor(decision Decision) string {
	for i, action := range decision.Actions {
		step, ok := capabilityStep(action)
		if !ok {
			continue
		}
		if strings.TrimSpace(step.ExpectedEffect) == "" {
			return fmt.Sprintf("%s does not say what it is for: every step names the World State change it expects (expected_effect)", stepLabel(step, i))
		}
	}
	return ""
}

// planEvidenceRefsExist: a step's evidence_refs have to be evidence this decision
// carries. A dangling reference is a claim without its support.
func planEvidenceRefsExist(decision Decision) string {
	known := map[string]bool{}
	for _, evidence := range decision.Evidence {
		if id := strings.TrimSpace(evidence.ID); id != "" {
			known[id] = true
		}
	}
	for i, action := range decision.Actions {
		step, ok := capabilityStep(action)
		if !ok {
			continue
		}
		for _, ref := range step.EvidenceRefs {
			if ref = strings.TrimSpace(ref); ref == "" || known[ref] {
				continue
			}
			return fmt.Sprintf("%s cites evidence %q, which this decision does not carry (%s)",
				stepLabel(step, i), ref, evidenceIDs(decision.Evidence))
		}
	}
	return ""
}

// noSteps: done / blocked / need_input decide the task; they do not execute.
func noSteps(decision Decision) string {
	if len(decision.Actions) == 0 {
		return ""
	}
	return fmt.Sprintf("the answer carries %d step(s): %q decides the task and does not execute anything", len(decision.Actions), strings.TrimSpace(decision.Type))
}

// noNeed: a done decision is not also asking for something.
func noNeed(decision Decision) string {
	if strings.TrimSpace(decision.Need.Type) == "" && strings.TrimSpace(decision.Need.Description) == "" &&
		len(decision.Need.Options) == 0 {
		return ""
	}
	return "the answer also says something is missing (need): done carries no need"
}

// doneNamesEvidence: completing a task is a claim about the world, and the claim has
// to carry what proves it — the runtime cannot see whether the goal is satisfied, but
// it can see whether the decision pointed at anything.
func doneNamesEvidence(decision Decision) string {
	if len(decision.Evidence) > 0 {
		return ""
	}
	return "the answer carries no evidence: done must name the observation or World State that proves the Goal is satisfied"
}

// needDescribesWhatIsMissing: for blocked / need_input the description *is* the
// answer — "blocked" with nothing missing is a dead end for whoever reads it.
func needDescribesWhatIsMissing(decision Decision) string {
	if strings.TrimSpace(decision.Need.Description) != "" {
		return ""
	}
	return "the answer does not say what is missing (need.description): describing it is the whole answer here"
}

// maxNeedOptions bounds the list: the choices a person is asked to pick between are
// readable at a glance, and the UI numbers them 1..N. A longer list is not a choice, it is
// a menu — and a menu is what `need.description` is for.
const maxNeedOptions = 9

// needOptionsAreChoices: `need.options` is optional — an open question has no options and
// the owner answers in their own words. When it *is* given it must be a real choice: at
// least two, each non-empty, none repeating, and no longer than maxNeedOptions. One option
// is not a choice: that is the answer, and it belongs in need.description.
//
// It exists because the alternative is guessing: the planner used to write its choices as
// prose ("(1) Scope decision: … (2) Approval/action to land: …") and every reader — a UI, a
// person — had to parse a paragraph to find them.
func needOptionsAreChoices(decision Decision) string {
	options := decision.Need.Options
	if len(options) == 0 {
		return ""
	}
	if len(options) < 2 {
		return "need.options carries a single option: one option is not a choice — write it in need.description and leave need.options empty"
	}
	if len(options) > maxNeedOptions {
		return fmt.Sprintf("need.options carries %d options: a choice is at most %d (list only what a person can pick between)",
			len(options), maxNeedOptions)
	}
	seen := make(map[string]bool, len(options))
	for i, option := range options {
		text := strings.TrimSpace(option)
		if text == "" {
			return fmt.Sprintf("need.options[%d] is empty: every option must say what choosing it does", i)
		}
		if seen[text] {
			return fmt.Sprintf("need.options repeats %q: two options that say the same thing are not two choices", text)
		}
		seen[text] = true
	}
	return ""
}

// evidenceIDs renders the ids a decision carries, for an error message.
func evidenceIDs(evidence []Evidence) string {
	if len(evidence) == 0 {
		return "it carries no evidence"
	}
	ids := make([]string, 0, len(evidence))
	for _, item := range evidence {
		if id := strings.TrimSpace(item.ID); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return "its evidence items have no ids"
	}
	return strings.Join(ids, ", ")
}
