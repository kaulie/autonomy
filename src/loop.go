package autonomy

import "fmt"

// Loop is the goal-driven cycle: decide → execute → verify → repeat.
type Loop struct {
	Agent    Agent
	Decide   DecisionMaker
	Runtime  Runtime
	World    World
	Verifier Verifier
	MaxSteps int
	OnEvent  func(Event)
}

func (l *Loop) emit(typ, msg string) {
	if l.OnEvent != nil {
		l.OnEvent(Event{Type: typ, Message: msg})
	}
}

// Run drives one task until the contract is met or MaxSteps is exhausted.
func (l *Loop) Run(task Task) error {
	if l.MaxSteps <= 0 {
		l.MaxSteps = 8
	}
	l.Agent.State = "running"
	for i := 0; i < l.MaxSteps; i++ {
		action, err := l.Decide.Decide(task, l.Agent, l.World)
		if err != nil {
			return err
		}
		l.emit("action.decided", action.Capability)

		out, err := l.Runtime.Execute(action)
		if err != nil {
			l.emit("action.failed", err.Error())
			return err
		}
		l.emit("action.done", fmt.Sprintf("%s -> %v", action.Capability, out))

		if l.Verifier.Verify(task, l.World) {
			l.Agent.State = "idle"
			l.emit("task.done", fmt.Sprintf("asset %s meets %s", task.Target, task.Contract.ExpectedState))
			return nil
		}
		l.emit("task.continue", fmt.Sprintf("asset %s is %s", task.Target, l.World.Get(task.Target)))
	}
	l.Agent.State = "idle"
	return fmt.Errorf("task %s not completed after %d steps", task.ID, l.MaxSteps)
}
