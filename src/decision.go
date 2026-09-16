package autonomy

import (
	"context"
	"os"
)

// DecisionContext is the input bag for one decision cycle.
type DecisionContext struct {
	context.Context

	Task  *Task
	Agent *Agent
	World World
	Cycle int // 1-based decision cycle when set by Autonomy.Run
	// History is what the previous cycles of this task did, oldest first: the
	// planner sees it as previous_actions and can base its evidence on it (see
	// AGENT_V2 evidence source previous_action).
	History []Result
}

type DecisionMaker struct {
	reasoner Reasoner
}

func NewDecideMaker() *DecisionMaker {
	// Default stays local for offline tests. Set AUTONOMY_REASONER=llm to use Cursor SDK Bridge.
	reasoner := Reasoner(NewLocalReasoner("local-reasoner"))
	if os.Getenv("AUTONOMY_REASONER") == "llm" {
		model := os.Getenv("AUTONOMY_LLM_MODEL")
		if model == "" {
			model = "composer-2"
		}
		reasoner = NewLLMReasoner(model)
	}
	return &DecisionMaker{reasoner: reasoner}
}

func (d *DecisionMaker) Decide(ctx DecisionContext) (Decision, error) {
	reasonningResult, err := d.reasoner.Reason(ctx, ReasoningInput{})
	if err != nil {
		return Decision{}, err
	}
	return reasonningResult.Decision, nil
}

// Decision is the outcome of one decision cycle: the AGENT_V2 answer the planner
// returns, as a Go value (see src/agent_policy/AGENT_V2.md §Output Schema — the raw
// JSON it comes from is in reason_turns.raw_output).
//
// A plan carries *every* step it wants executed, in order: the runtime executes
// them all and the agent observes afterwards, so a multi-step plan is not
// reduced to its first action.
type Decision struct {
	// Type is plan | done | blocked | need_input.
	Type   string
	Reason string
	// Evidence are the facts the decision rests on; plan steps reference them by
	// ID via the step's evidence_refs.
	Evidence []Evidence
	// Actions are the plan's steps in execution order (empty for
	// done/blocked/need_input).
	Actions []Action
	// Need is what is missing when the decision is blocked or needs input.
	Need Need
	// Deliverables is what the task hands over and Presentation is how the result
	// should be expressed to its consumer (the Owner's final decision, see
	// docs/completion-contract.md).
	Deliverables []Deliverable
	Presentation []Presentation
	Ctx          DecisionContext
}

// Evidence is one fact supporting a decision (AGENT_V2 "evidence").
type Evidence struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Reference string `json:"reference"`
	Fact      string `json:"fact"`
}

// Need says what is missing when the decision is blocked or needs input.
type Need struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

// Deliverable is the concrete result a task hands over: a system-defined Asset
// or a task-produced Artifact.
type Deliverable struct {
	// Type is asset | artifact.
	Type string `json:"type"`
	// ConcreteType names the recognised asset/artifact (Repository, Service, File, …).
	ConcreteType string         `json:"concrete_type"`
	Detail       map[string]any `json:"detail"`
}

// Presentation is how the task result should be expressed to its consumer.
type Presentation struct {
	// Type is summary | report | status | decision | finding | artifact | question.
	Type    string `json:"type"`
	Content string `json:"content"`
}

// ActionResult is one action's record in a cycle: what ran, the input it was
// actually called with, what it produced, and how it ended. Input and output are
// kept verbatim per action — never merged or summarised — so the consumer (the
// next decision, a UI) decides which entry matters.
type ActionResult struct {
	// Capability is the capability the step named ("nothing" for a skipped step).
	Capability string
	// Input is the input the capability was called with, after the runtime's task
	// defaults, verbatim.
	Input map[string]string
	// Output is what the capability returned, verbatim; a capability that failed
	// keeps whatever it produced before failing.
	Output map[string]string
	// Error is empty when the action succeeded (filled in by Runtime.Execute).
	Error string
	// ExpectedEffect and EvidenceRefs are the plan step's own metadata
	// (AGENT_V2: what the step was meant to change, and on which evidence).
	ExpectedEffect string
	EvidenceRefs   []string
}

// Result is what happened after executing a decision.
type Result struct {
	Message string
	// Actions holds one record per executed action, in plan order, with each
	// action's raw input and output. Nothing is merged away: a consumer that only
	// wants "the" output picks the entry it cares about.
	Actions []ActionResult
	Err     error
	// WorldState optionally patches asset state (applied by UpdateWorld).
	WorldState map[string]string
}
