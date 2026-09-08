package autonomy

import (
	"fmt"
)

type Autonomy struct {
	AgentFactory      *AgentFactory
	CapabilityFactory *CapabilityFactory
	Runtime           *Runtime
	Varifier          *Verifier
	World             *World
	MaxSteps          int
}

var bootstrapFlag bool
var _autonomy *Autonomy

const DefaultMaxSteps = 10

func BootstrapAutonomy() *Autonomy {
	if bootstrapFlag {
		return _autonomy
	}
	capabilityFactory := NewCapabilityFactory()
	capabilityFactory.Register(AssetChangeCapability{})

	_autonomy = &Autonomy{
		AgentFactory:      NewAgentFactory(),
		CapabilityFactory: capabilityFactory,
		Runtime:           NewRuntime(capabilityFactory.GetAll()...),
		Varifier:          NewVerifier(),
		MaxSteps:          DefaultMaxSteps,
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
	defer r.finishAgent(agent)

	agent.Start()

	steps := 0

	for r.ShouldContinue(agent) {
		steps++
		if steps > r.MaxSteps {
			break
		}
		decision, err := agent.Decide()
		if err != nil {
			return fmt.Errorf("decide: %w", err)
		}
		result, err := r.Runtime.Execute(decision)
		if err != nil {
			return fmt.Errorf("execute decision: %w", err)
		}
		agent.Observe(result)

		if r.Varifier.Verify(task, r.World) {
			fmt.Printf("Task verified\n")
			break
		}
	}

	ret, err := agent.Result()
	fmt.Printf("Agent result: %v, error: %v\n", ret, err)

	return err
}

// finishAgent stops the agent and deletes it when lifecycle is ephemeral (default).
func (r *Autonomy) finishAgent(agent *Agent) {
	if agent == nil {
		return
	}
	agent.Stop()
	if agent.IsEphemeral() {
		r.AgentFactory.Delete(agent.ID)
	}
}

func (r *Autonomy) ShouldContinue(agent *Agent) bool {
	return true
}
