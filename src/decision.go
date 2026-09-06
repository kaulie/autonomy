package autonomy

import "context"

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
	// reasoner := NewLLMReasoner("gpt-4o-mini")
	reasoner := NewLocalReasoner("local-reasoner")
	return &DecisionMaker{
		reasoner: reasoner,
	}
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
