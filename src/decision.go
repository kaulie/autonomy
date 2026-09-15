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
	Step  int // 1-based loop step when set by Autonomy.Run
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

// Result is what happened after executing a decision.
type Result struct {
	Message    string
	Output     map[string]string
	Err        error
	WorldState map[string]string // optional patches applied by UpdateWorld
}
