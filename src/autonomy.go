package autonomy

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/kaulie/autonomy/src/capability"
)

type Autonomy struct {
	AgentFactory *AgentFactory
	// used to manage context containers registration
	ContextContainerManager *ContextContainerManager
	// used to manage context entities registration
	ContextEntityManager *ContextEntityManager
	// used to manage domain entities registration
	DomainEntityManager *DomainEntityManager
	// used to manage context references for a task
	TaskCtxManager    *TaskCtxManager
	CapabilityFactory *capability.Factory
	Runtime           *Runtime
	World             *World
	Store             Store
	MaxSteps          int
}

var bootstrapFlag bool
var _autonomy *Autonomy

const DefaultMaxSteps = 1

func GetAutonomy() *Autonomy {
	if _autonomy == nil {
		panic("Autonomy not initialized")
	}
	return _autonomy
}

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
		Store:             store,
		MaxSteps:          DefaultMaxSteps,
	}
	_autonomy.SetWorld(world)

	contextContainerManager := NewContextContainerManager()
	_autonomy.ContextContainerManager = contextContainerManager

	contextEntityManager := NewContextEntityManager()
	_autonomy.ContextEntityManager = contextEntityManager

	domainEntityManager := NewDomainEntityManager()
	_autonomy.DomainEntityManager = domainEntityManager

	TaskCtxManager := NewTaskCtxManager()
	_autonomy.TaskCtxManager = TaskCtxManager

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

	cycles := 0
	var err error
	var decision Decision
	var result Result
	var history []Result
	for r.ShouldContinue(agent) {
		cycles++
		if cycles > r.maxSteps() {
			break
		}
		decision, err = agent.DecideAtCycle(cycles, history)
		if err != nil {
			// Nothing was planned, so nothing else can explain it: the reason it
			// could not decide is the whole failure.
			err = fmt.Errorf("decide: %w", err)
			failTask(task, err)
			return err
		}
		// A failed action stops this cycle, not the task: the failure is observed
		// and handed to the next decision (see executeDecision).
		result = r.executeDecision(agent, decision)
		err = result.Err
		history = append(history, result)
	}

	// ret, err := agent.Result()
	// fmt.Printf("Agent result: %v, error: %v\n", ret, err)
	if task.Status == "running" || task.Status == "pending" {
		if err != nil {
			failTask(task, err)
		} else {
			task.Status = "completed" //completed not means success, it means the task is completed
			persistTask(task)
		}
	}
	return err
}

// failTask ends the task in error, with the reason the runtime gave up on it —
// the decide that failed, or the last cycle's failing action. Both used to be
// printed and thrown away: a task row said "error" and nothing said why, so the
// only account of the failure was the terminal it happened in.
//
// The row describes the attempt that recorded an outcome: it is written here and
// where a task completes, not when a run starts, so an unrecorded run cannot
// leave a row claiming an outcome it never had.
func failTask(task *Task, err error) {
	if task == nil {
		return
	}
	task.Status = "error"
	if err != nil {
		task.Error = err.Error()
	}
	persistTask(task)
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

// executeDecision runs one decision's plan and tells the agent what happened.
// A failed action (see Runtime.Execute: the plan stops at its first failure)
// ends the cycle without failing the task: the result carries the error, the
// next decision sees it in previous_actions, and the task ends as error only if
// the last cycle failed.
func (r *Autonomy) executeDecision(agent *Agent, decision Decision) Result {
	result, err := r.Runtime.Execute(decision)
	if err != nil {
		result.Err = err
	}
	if agent != nil {
		agent.Observe(result)
	}
	return result
}

// maxSteps is how many decision cycles a task may run: Autonomy.MaxSteps, or
// AUTONOMY_MAX_STEPS when set. It is the loop's budget, not its goal (see
// docs/execution-loop.md); the default keeps one cycle per task.
func (r *Autonomy) maxSteps() int {
	if env := strings.TrimSpace(os.Getenv("AUTONOMY_MAX_STEPS")); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n > 0 {
			return n
		}
	}
	if r.MaxSteps > 0 {
		return r.MaxSteps
	}
	return DefaultMaxSteps
}

func (r *Autonomy) ShouldContinue(agent *Agent) bool {
	return true
}
