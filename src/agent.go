package autonomy

// Agent is the subject that owns a task and decision authority.
// Decision making is a capability of the agent, not of the loop.
type Agent struct {
	ID          string
	State       string // lifecycle: idle | running | ...
	CurrentTask *Task
	Context     string
	Decide      DecisionMaker
}

// DecisionMaker chooses the next decision given a decision context.
type DecisionMaker interface {
	Decide(ctx DecisionContext) (Decision, error)
}
