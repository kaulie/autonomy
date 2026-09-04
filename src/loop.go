package autonomy

import "fmt"

// Loop is the goal-driven execution cycle: plan → execute → verify → repeat.
type Loop struct {
	Agent    Agent
	Runtime  *Runtime
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
	for i := 0; i < l.MaxSteps; i++ {
		step, err := l.Agent.Next(task, l.World)
		if err != nil {
			return err
		}
		l.emit("step.planned", step.Capability)

		out, err := l.Runtime.Run(step)
		if err != nil {
			l.emit("step.failed", err.Error())
			return err
		}
		l.emit("step.done", fmt.Sprintf("%s -> %v", step.Capability, out))

		if l.Verifier.Verify(task, l.World) {
			l.emit("task.done", fmt.Sprintf("asset %s meets %s", task.Asset, task.Contract.ExpectedState))
			return nil
		}
		l.emit("task.continue", fmt.Sprintf("asset %s is %s", task.Asset, l.World.Get(task.Asset)))
	}
	return fmt.Errorf("task %s not completed after %d steps", task.ID, l.MaxSteps)
}
