package autonomy

// Agent is the subject that owns decision authority for a task.
// It is an entity, not a behavior interface.
type Agent struct {
	ID    string
	State string
}

// DecisionMaker chooses the next decision given a decision context.
type DecisionMaker interface {
	Decide(ctx DecisionContext) (Decision, error)
}
