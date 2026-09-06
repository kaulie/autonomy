package autonomy

import (
	"fmt"
)

type Autonomy struct {
	AgentFactory *AgentFactory
	Runtime      *Runtime
	Varifier     *Verifier
	World        *World
	MaxSteps     int
}

var bootstrapFlag bool
var _autonomy *Autonomy

const DefaultMaxSteps = 10

func BootstrapAutonomy() *Autonomy {
	if bootstrapFlag {
		return _autonomy
	}
	capabilityFactory := NewCapabilityFactory()
	_autonomy = &Autonomy{
		AgentFactory: NewAgentFactory(),
		Runtime:      NewRuntime(capabilityFactory.GetAll()...),
		Varifier:     NewVerifier(),
		MaxSteps:     DefaultMaxSteps,
	}
	world := buildWorld()
	_autonomy.SetWorld(world)
	bootstrapFlag = true
	return _autonomy
}

func (r *Autonomy) SetWorld(world *World) {
	r.World = world
}

func (r *Autonomy) Run(task *Task) error {
	agent := r.AgentFactory.Create(task)

	agent.Start()

	steps := 0

	for r.ShouldContinue(agent) {
		steps++
		if steps > r.MaxSteps {
			break
		}
		decision := agent.Decide()
		result, err := r.Runtime.Execute(decision)
		// fmt.Printf("result: %v, error: %v\n", result, err)
		if err != nil {
			return fmt.Errorf("execute decision: %w", err)
		}
		agent.Observe(result)

		if r.Varifier.Verify(task, r.World) {
			fmt.Printf("Task verified\n")
			break
		}
	}

	agent.Stop()

	ret, err := agent.Result()
	fmt.Printf("Agent result: %v, error: %v\n", ret, err)

	return err
}

func (r *Autonomy) ShouldContinue(agent *Agent) bool {
	return true
}
