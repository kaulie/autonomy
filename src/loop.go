package autonomy

import "fmt"

// LoopFactory creates and caches loops by id.
type LoopFactory struct {
	loops map[string]Loop
}

func NewLoopFactory() *LoopFactory {
	return &LoopFactory{
		loops: make(map[string]Loop),
	}
}

func (f *LoopFactory) NewLoop(id string) Loop {
	if l, ok := f.loops[id]; ok {
		return l
	}
	l := Loop{}
	f.loops[id] = l
	return l
}

// Loop orchestrates the goal-driven cycle.
// It does not own decision making — that belongs to Agent.
type Loop struct {
	Agent    Agent
	Runtime  Runtime
	World    World
	Verifier Verifier
	MaxSteps int
	History  []Result
	OnEvent  func(Event)
}

func (l *Loop) emit(typ, msg string) {
	if l.OnEvent != nil {
		l.OnEvent(Event{Type: typ, Message: msg})
	}
}

// func (l *Loop) BuildDecisionContext(task Task) DecisionContext {
// 	return DecisionContext{Task: task, Agent: l.Agent, World: l.World}
// }

// func (l *Loop) Execute(decision Decision) Result {
// 	out, err := l.Runtime.Execute(decision.Action)
// 	return Result{Decision: decision, Output: out, Err: err}
// }

func (l *Loop) Record(result Result) {
	l.History = append(l.History, result)
	if result.Err != nil {
		l.emit("action.failed", result.Err.Error())
		return
	}
	// l.emit("action.done", fmt.Sprintf("%s -> %v", result.Decision.Action, result.Output))
}

func (l *Loop) UpdateWorld(result Result) {
	if result.Err != nil || len(result.WorldState) == 0 {
		return
	}
	// w, ok := l.World.(StateWriter)
	// if !ok {
	// 	return
	// }
	// for id, state := range result.WorldState {
	// 	w.Set(id, state)
	// }
}

func (l *Loop) ShouldTerminate(result Result) bool {
	return result.Err != nil
}

func (l *Loop) TaskFailure(task Task, result Result) error {
	if result.Err != nil {
		return fmt.Errorf("task %s failed: %w", task.ID, result.Err)
	}
	return fmt.Errorf("task %s failed", task.ID)
}

// // Run drives one task until the contract is met, execution fails, or MaxSteps is exhausted.
// func (l *Loop) Run(task Task) error {
// 	if l.MaxSteps <= 0 {
// 		l.MaxSteps = 8
// 	}
// 	if l.Agent.Decide == nil {
// 		return fmt.Errorf("agent %s has no decision making", l.Agent.ID)
// 	}

// 	l.Agent.CurrentTask = &task
// 	l.Agent.State = "running"
// 	defer func() {
// 		l.Agent.State = "idle"
// 		l.Agent.CurrentTask = nil
// 	}()

// 	for i := 0; i < l.MaxSteps; i++ {
// 		ctx := l.BuildDecisionContext(task)

// 		decision, err := l.Agent.Decide.Decide(ctx)
// 		if err != nil {
// 			return err
// 		}
// 		l.emit("decision.made", decision.Action.Capability)

// 		result := l.Execute(decision)
// 		l.Record(result)
// 		l.UpdateWorld(result)

// 		if l.Verifier.Verify(task, l.World) {
// 			l.emit("task.done", task.ID)
// 			return nil
// 		}

// 		if l.ShouldTerminate(result) {
// 			return l.TaskFailure(task, result)
// 		}

// 		l.emit("task.continue", fmt.Sprintf("asset %s is %s", task.Target, l.World.Get(task.Target)))
// 	}

// 	return fmt.Errorf("task %s exceeded decision cycle limit", task.ID)
// }
