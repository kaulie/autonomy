package autonomy

// DecisionContext is the input bag for one decision cycle.
type DecisionContext struct {
	Task  Task
	Agent Agent
	World World
}

// Decision is the outcome of decision making. V1 wraps a single Action.
type Decision struct {
	Action Action
}

// Result is what happened after executing a decision.
type Result struct {
	Decision   Decision
	Output     map[string]string
	Err        error
	WorldState map[string]string // optional patches applied by UpdateWorld
}
