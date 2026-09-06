package autonomy

import (
	"context"
	"os"
)

// DecisionContext is the input bag for one decision cycle.
type DecisionContext struct {
	context.Context

	Task  *Task
	World World
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

// Decision is the outcome of decision making. V1 wraps a single Action.
type Decision struct {
	Reason string
	Action Action //just for now, may be extended to multiple actions in the future
	Ctx    DecisionContext
}

// Result is what happened after executing a decision.
type Result struct {
	Message    string
	Output     map[string]string
	Err        error
	WorldState map[string]string // optional patches applied by UpdateWorld
}
