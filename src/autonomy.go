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

const DefaultMaxSteps = 4

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

// cycleDone is the event the runtime worker sends when a dispatched decision has
// finished. The planner event loop observes it and decides whether to plan again.
type cycleDone struct {
	decision Decision
	result   Result
}

func (r *Autonomy) Run(task *Task) error {
	return r.run(context.Background(), task)
}

func (r *Autonomy) run(ctx context.Context, task *Task) error {
	if task == nil {
		return fmt.Errorf("nil task")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if task.Status == "" {
		task.Status = TaskStatusRunning
	}
	persistTask(task)

	agent := r.AgentFactory.Create(task)
	defer r.finishAgent(agent)

	// The task's own agent takes a turn every cycle, on the same session a delegated
	// worker gets: what makes this agent the planner is its identity, not a different
	// kind of session (see LLMSession). Every turn is attributed to this task.
	agent.Session = NewLLMSession(r.Runtime, agent, SessionOpts{TaskID: task.ID})

	agent.Start()
	persistAgent(agent)

	// Event-driven loop (docs/principles.md §Event Driven; docs/runtime.md): Decide
	// runs on this goroutine; Runtime.Execute runs on a worker. After a decision is
	// dispatched the planner parks on events — it is not locked inside Execute.
	// Per task, decide → execute → observe stays ordered: the next Decide waits for
	// this cycle's Result so previous_actions stay coherent.
	events := make(chan cycleDone, 1)
	cycles := 0
	var err error
	var decision Decision
	var result Result
	var history []Result
	needDecide := true
	for {
		if err := ctx.Err(); err != nil {
			markStopped(task)
			return fmt.Errorf("stopped: %w", err)
		}
		if needDecide {
			if !r.ShouldContinue(agent) {
				break
			}
			cycles++
			if cycles > r.maxSteps() {
				break
			}
			decision, err = agent.decide(ctx, cycles, history)
			if err != nil {
				if ctx.Err() != nil {
					markStopped(task)
					return fmt.Errorf("stopped: %w", ctx.Err())
				}
				// Nothing was planned, so nothing else can explain it: the reason it
				// could not decide is the whole failure.
				err = fmt.Errorf("decide: %w", err)
				failTask(task, err)
				return err
			}
			// Whether this decision concludes the task is the decision's own answer to
			// give — its type — and nothing the cycle does can change it.
			//
			// The cycle still runs: Runtime.Execute is where the decision's contract is
			// checked (a done that proves nothing does not get to complete anything)
			// and where its plan row is written. A concluding decision carries no steps,
			// so running it *is* that check and that row — still on the worker, so the
			// planner loop is free while it happens.
			r.dispatchExecute(events, decision)
			needDecide = false
			continue
		}

		var ev cycleDone
		select {
		case ev = <-events:
		case <-ctx.Done():
			markStopped(task)
			return fmt.Errorf("stopped: %w", ctx.Err())
		}
		decision = ev.decision
		result = ev.result
		err = result.Err
		if agent != nil {
			agent.Observe(result)
		}
		history = append(history, result)
		// A decision that concludes the task ends the run here: done / blocked /
		// need_input are answers, not plans, so another cycle would only ask the same
		// question again (task-26 asked three times after its plan had succeeded).
		//
		// An answer the runtime *refused* concluded nothing. A done with no evidence
		// and a blocked that does not say what is missing break their type's
		// requirements (src/decision_rules.go), so that cycle failed like any other
		// failed cycle and the planner re-plans from the reason it carries — the
		// promise docs/execution-loop.md makes. Only an answer that held up ends the
		// run. Either way the loop stays bounded by maxSteps.
		if decision.Concludes() && result.Err == nil {
			break
		}
		needDecide = true
	}

	if ctx.Err() != nil {
		markStopped(task)
		return fmt.Errorf("stopped: %w", ctx.Err())
	}
	if task.Status == TaskStatusRunning || task.Status == TaskStatusPending {
		switch {
		case err != nil && isDone(decision):
			// The last answer was `done` and it did not hold up — verification refused
			// it, or the type's own requirements did. The run ends without the goal
			// ever being verified, which is its own outcome: not `completed` (that is
			// for a `done` verification passed) and not `error` (this is not a failed
			// action or a failed decide). Why it did not hold up is on the task, and in
			// the `verification` rows of its plans (docs/verification.md).
			task.Status = TaskStatusUnverified
			task.Error = err.Error()
			persistTask(task)
		case err != nil:
			failTask(task, err)
		case decision.Concludes():
			// The decision that concluded the run is what happened to the task: `done`
			// under the vocabulary's own word for it, and `blocked` / `need_input` as
			// themselves, since a task waiting on something is not a finished one. Why it
			// is blocked is that decision's `need`, on its plan row and in its reply;
			// Error stays what docs/store.md says it is: why a run failed. (A concluding
			// decision reaches this branch only if it held up: a refused answer leaves the
			// loop the way any failed cycle does, so `err` above takes it instead.)
			task.Status = TaskStatusFor(decision)
			persistTask(task)
		default:
			// No failure and no conclusion: the budget ran out after a plan.
			task.Status = TaskStatusCompleted //completed not means success, it means the task is completed
			persistTask(task)
		}
	}
	return err
}

// dispatchExecute runs Runtime.Execute on a worker goroutine and delivers the
// Result as a cycleDone event. Observe stays on the planner event loop so the
// next Decide sees previous_actions in order.
func (r *Autonomy) dispatchExecute(events chan<- cycleDone, decision Decision) {
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				events <- cycleDone{
					decision: decision,
					result: Result{
						Err:     fmt.Errorf("execute panic: %v", rec),
						Message: fmt.Sprintf("execute panic: %v", rec),
					},
				}
			}
		}()
		result, execErr := r.Runtime.Execute(decision)
		if execErr != nil {
			result.Err = execErr
		}
		events <- cycleDone{decision: decision, result: result}
	}()
}

// markStopped records that a caller cancelled the run (POST /api/tasks/{id}/stop).
// A run that already left running/pending keeps that outcome.
func markStopped(task *Task) {
	if task == nil {
		return
	}
	if task.Status != TaskStatusRunning && task.Status != TaskStatusPending && task.Status != "" {
		return
	}
	task.Status = TaskStatusStopped
	task.Error = "stopped"
	persistTask(task)
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
	task.Status = TaskStatusError
	if err != nil {
		task.Error = err.Error()
	}
	persistTask(task)
}

// finishAgent ends the agent the run was for. Ending an agent's life — stop, tear
// down its provider sessions, delete it or keep it — is the same wherever the agent
// came from, so it is one function (closeAgent) that both this and a released worker
// session go through.
func (r *Autonomy) finishAgent(agent *Agent) {
	closeAgent(agent, r.AgentFactory, context.Background())
}

// executeDecision runs one decision's plan and tells the agent what happened.
// Tests call this synchronously; Autonomy.Run dispatches Execute on a worker and
// Observes on the planner event loop instead (see dispatchExecute).
//
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
// docs/execution-loop.md): a decision that concludes the task (done / blocked /
// need_input) ends the run before the budget is spent, and the budget is what bounds
// re-planning after a failed cycle.
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
