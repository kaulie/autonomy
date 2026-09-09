package autonomy

import (
	"context"
	"fmt"

	"github.com/kaulie/autonomy/src/capability"
)

type Autonomy struct {
	AgentFactory      *AgentFactory
	CapabilityFactory *capability.Factory
	Runtime           *Runtime
	Varifier          *Verifier
	World             *World
	Store             Store
	MaxSteps          int
}

var bootstrapFlag bool
var _autonomy *Autonomy

const DefaultMaxSteps = 2

func BootstrapAutonomy() (*Autonomy, error) {
	if bootstrapFlag {
		return _autonomy, nil
	}
	store, err := OpenDefaultStore()
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	_store = store

	// World must exist before registering capabilities that mutate assets.
	world := buildWorld()

	agentFactory := NewAgentFactory()
	rt := NewRuntime(agentFactory)

	capabilityFactory := capability.NewFactory()
	capability.RegisterDefaults(capabilityFactory, capability.Deps{
		Assets: worldAssetMutator(),
		Agents: rt, // capabilities acquire Cursor-backed agents via Runtime
	})
	rt.SetCapabilities(capabilityFactory.GetAll()...)

	_autonomy = &Autonomy{
		AgentFactory:      agentFactory,
		CapabilityFactory: capabilityFactory,
		Runtime:           rt,
		Varifier:          NewVerifier(),
		Store:             store,
		MaxSteps:          DefaultMaxSteps,
	}
	_autonomy.SetWorld(world)
	bootstrapFlag = true
	return _autonomy, nil
}

func (r *Autonomy) SetWorld(world *World) {
	r.World = world
}

// Close releases process-wide resources: the shared Cursor bridge client and
// the Store. Call it at runtime teardown (e.g. deferred in main) once all
// agents have finished.
func (r *Autonomy) Close() error {
	var first error
	if err := closeSharedCursorClient(); err != nil && first == nil {
		first = err
	}
	if r != nil && r.Store != nil {
		if err := r.Store.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (r *Autonomy) Run(task *Task) error {
	if task == nil {
		return fmt.Errorf("nil task")
	}
	if task.Status == "" {
		task.Status = "running"
	}
	persistTask(task)

	agent := r.AgentFactory.Create(task)
	defer r.finishAgent(agent)

	agent.Start()
	persistAgent(agent)

	steps := 0

	for r.ShouldContinue(agent) {
		steps++
		if steps > r.MaxSteps {
			break
		}
		decision, err := agent.DecideAtStep(steps)
		if err != nil {
			task.Status = "error"
			persistTask(task)
			return fmt.Errorf("decide: %w", err)
		}
		result, err := r.Runtime.Execute(decision)
		if err != nil {
			task.Status = "error"
			persistTask(task)
			return fmt.Errorf("execute decision: %w", err)
		}
		agent.Observe(result)

		if r.Varifier.Verify(task, r.World) {
			fmt.Printf("Task verified\n")
			task.Status = "done"
			persistTask(task)
			break
		}
	}

	ret, err := agent.Result()
	fmt.Printf("Agent result: %v, error: %v\n", ret, err)
	if task.Status == "running" || task.Status == "pending" {
		if err != nil {
			task.Status = "error"
		} else {
			task.Status = "completed"
		}
		persistTask(task)
	}
	return err
}

// finishAgent stops the agent and tears down the Cursor SDK session.
// Ephemeral agents are permanently deleted via Cursor DeleteAgent; DB row is soft-deleted.
// Persistent agents are only Closed so durable Cursor state can be resumed later.
func (r *Autonomy) finishAgent(agent *Agent) {
	if agent == nil {
		return
	}
	agent.Stop()
	agent.disposeCursorSession(context.Background())
	if agent.IsEphemeral() {
		softDeleteAgent(agent.ID)
		r.AgentFactory.Delete(agent.Name)
	} else {
		persistAgent(agent)
	}
}

func (r *Autonomy) ShouldContinue(agent *Agent) bool {
	return true
}
