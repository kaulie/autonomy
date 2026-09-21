package autonomy

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/kaulie/autonomy/src/capability"
	"github.com/kaulie/autonomy/src/context_builder"
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
	TaskCtxManager *TaskCtxManager
	// ContextBuilder resolves a task's context_ref into the world its cycles reason
	// about (src/context_resolver.go, src/context_builder): this process's own
	// registered context, plus the platform's registries — the project and the
	// organization it belongs to. It is nil when the builder is switched off
	// (AUTONOMY_CONTEXT_BUILDER=0).
	ContextBuilder    *context_builder.Builder
	CapabilityFactory *capability.Factory
	Runtime           *Runtime
	World             *World
	Store             Store
	// Inbox is every agent's message queue: the messages addressed to it are
	// processed by that agent, one at a time, in the order they arrived
	// (src/inbox.go). It is also how an instruction reaches an agent — including
	// one that arrives while the agent is busy, which is what makes instructions
	// continuously acceptable.
	Inbox *Inbox
	// Drain is the graceful restart this runtime is in the middle of, when it is:
	// an announced restart stops the inbox from starting new runs, and the two
	// endpoints the deployment platform talks to (POST /api/ops/restart-notify,
	// GET /api/ops/restart-status) are this field's story (src/graceful.go). It is
	// built on demand — wiring the inbox asks for it, so a runtime that never
	// queues anything never has one.
	Drain    *RestartDrain
	MaxSteps int
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

	// The inbox is wired here: it needs the store (where the messages wait) and the
	// runtime (the sessions a delegated message is processed on), and the runtime
	// needs it back to send one.
	_autonomy.agentInbox()

	contextEntityManager := NewContextEntityManager()
	_autonomy.ContextEntityManager = contextEntityManager

	domainEntityManager := NewDomainEntityManager()
	_autonomy.DomainEntityManager = domainEntityManager

	TaskCtxManager := NewTaskCtxManager()
	_autonomy.TaskCtxManager = TaskCtxManager

	// The context builder is wired here, after the managers it reads: every decision
	// cycle resolves its task's context_ref through it (fillContextSections).
	_autonomy.ContextBuilder = newContextBuilder(_autonomy)

	// Last, once every manager a decision cycle reads is wired: the instructions a
	// previous process left queued (a restart in the middle of a drain holds them
	// back) are started here, so accepting an instruction across a restart is not a
	// way to lose it (src/graceful.go).
	_autonomy.resumeAcceptedInstructions()

	bootstrapFlag = true
	return _autonomy, nil
}

// agentInbox is this runtime's message queue, built on demand: bootstrap wires one,
// and a runtime assembled by hand (a test, an embedder) gets one the first time a
// message goes into an agent's queue. Both doors must lead to the same queue — the
// sessions a capability is handed send their messages through it too.
func (r *Autonomy) agentInbox() *Inbox {
	if r == nil {
		return nil
	}
	if r.Inbox == nil {
		store := r.Store
		if store == nil {
			store = activeStore()
		}
		r.Inbox = NewInbox(store, r.processMessage, r.drainedAgent)
		// The drain, when there is one, is what holds new runs back: an announced
		// restart stops the inbox from starting an agent on an instruction that
		// arrived after it (src/graceful.go). Nothing is lost: the message is a row,
		// and the next process starts it.
		r.Inbox.holdWhile(r.restartDrain().paused)
		if r.Runtime != nil {
			r.Runtime.SetInbox(r.Inbox)
		}
	}
	return r.Inbox
}

func (r *Autonomy) SetWorld(world *World) {
	r.World = world
}

// Close releases process-wide resources at runtime teardown (e.g. deferred in main):
// the provider sessions the resident agents are holding, the shared bridge clients,
// and the Store. Rows are kept — an agent is not deleted by a shutdown, and the
// session id on its row is what the next process resumes.
func (r *Autonomy) Close() error {
	var first error
	// A drain that is still armed must not come back to life while teardown is
	// closing the store under it: there is nothing left to resume (src/graceful.go).
	if r != nil && r.Drain != nil {
		r.Drain.stop()
	}
	// Agents are resident (see drainedAgent), so teardown is where their provider
	// sessions are torn down: the rows and handles stay, and the session ids they
	// recorded are what a later process resumes.
	if r != nil && r.AgentFactory != nil {
		for _, agent := range r.AgentFactory.snapshot() {
			closeAgent(agent, r.AgentFactory, context.Background())
		}
	}
	if err := closeSharedCursorClient(); err != nil && first == nil {
		first = err
	}
	if err := closeSharedClineClient(); err != nil && first == nil {
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

// instruction resolves the task's agent and builds the message one instruction
// becomes: the user speaking, about that task, saying this. Both doors build their
// message here — HTTP's AcceptTask and Autonomy.Run — so an instruction is the same
// message wherever it came from. Accepting an instruction queues it, so the task is
// written as pending — the agent's consumer is what marks it running.
//
// Nothing here opens the agent's provider session, so nothing here can wait on a
// bridge: that is the turn's job (LLMSession.Say), which is what keeps both doors —
// and a broadcast, which is one accept per agent — answering promptly.
func (r *Autonomy) instruction(task *Task, content string) (*Agent, AgentMessage, error) {
	if task == nil {
		return nil, AgentMessage{}, fmt.Errorf("nil task")
	}
	text := strings.TrimSpace(content)
	if text == "" {
		// A request may carry just the task's id: what that task is asked to do is
		// then its own row — the description an earlier instruction gave it. The row
		// is written again below (persistTask), so it is read from the writer
		// (docs/store.md「读写分离」).
		if store := writerReads(r.taskStore()); store != nil {
			if stored, err := store.GetTask(task.ID); err == nil && stored != nil {
				text = strings.TrimSpace(stored.Description)
			}
		}
	}
	if text == "" {
		return nil, AgentMessage{}, fmt.Errorf("description is required")
	}
	if task.Status == "" {
		task.Status = TaskStatusPending
	}
	// The agent before the task row: which agent a task is paired with is read off
	// that row (src/agent_resume.go), so the instruction finds it before this write
	// — which carries a Task value that need not know the agent — touches it.
	agent, err := r.resumeAgentForTask(task)
	if err != nil {
		// The instruction could not be picked up at all, so the task says so
		// instead of being left claiming to run.
		failTask(task, err)
		return nil, AgentMessage{}, err
	}
	persistTask(task)
	return agent, AgentMessage{
		TaskID:   task.ID,
		Sender:   MessageSenderUser,
		SenderID: string(MessageSenderUser),
		Kind:     MessageKindInstruction,
		Content:  text,
	}, nil
}

// processMessage is what the inbox hands one message to: the agent's own work.
// What the work is, is what the message is — an instruction is a decision run, a
// delegation is one turn of a worker's session, and a stop has already happened by
// the time it is read (cancelling the message is what stopped it; this is the
// record of it). The agent processing its messages one at a time, in the order
// they arrived, is the same for all three.
func (r *Autonomy) processMessage(ctx context.Context, cancel context.CancelFunc, agent *Agent, msg AgentMessage) (TurnResult, error) {
	switch msg.Kind {
	case MessageKindStop:
		return TurnResult{}, nil
	case MessageKindDelegation:
		if agent == nil || agent.Session == nil {
			return TurnResult{}, fmt.Errorf("delegated message for an agent with no session")
		}
		return agent.Session.Say(ctx, msg.Content, RoundAuto)
	default:
		return r.processInstruction(ctx, cancel, agent, msg)
	}
}

// processInstruction is one instruction's run: the task it is about is running
// again, this message is what the run answers (every cycle carries it as
// additional_input), and the loop is the same loop every instruction has gone
// through.
func (r *Autonomy) processInstruction(ctx context.Context, cancel context.CancelFunc, agent *Agent, msg AgentMessage) (TurnResult, error) {
	task := r.taskForMessage(msg)
	if task == nil {
		return TurnResult{}, fmt.Errorf("instruction for an unknown task: %q", msg.TaskID)
	}
	task.Status = TaskStatusRunning
	task.AgentID = agent.ID
	persistTask(task)

	// The task's own agent takes a turn every cycle, on the same session a delegated
	// worker gets: what makes this agent the planner is its identity, not a different
	// kind of session (see LLMSession). Every turn is attributed to this task.
	//
	// The agent is resident, so its session is kept across messages: a new message
	// continues the conversation it already has. A session is only opened when there is
	// none left to continue (the first message, or one that was closed).
	if agent.Session == nil || agent.Session.agent == nil || agent.Session.taskID != task.ID {
		agent.Session = NewLLMSession(r.Runtime, agent, SessionOpts{TaskID: task.ID})
	}
	// Processing a message is running a task: that is what a stop cancels
	// (POST /api/tasks/{id}/stop) and what says a task is running at all.
	if cancel != nil {
		inFlightTasks.Store(task.ID, cancel)
		defer inFlightTasks.Delete(task.ID)
	}
	agent.Start()
	persistAgent(agent)

	return TurnResult{}, r.runLoop(ctx, agent, task, msg.Content)
}

// taskForMessage is the task an instruction is about: its row, or a bare task with
// that id when the store has none. The row is marked running as soon as the message
// is processed (processInstruction), so it is read from the writer — this message is
// one this runtime queued, and the row it belongs to may be younger than the
// replication lag (docs/store.md「读写分离」).
func (r *Autonomy) taskForMessage(msg AgentMessage) *Task {
	if strings.TrimSpace(msg.TaskID) == "" {
		return nil
	}
	if store := writerReads(r.taskStore()); store != nil {
		if task, err := store.GetTask(msg.TaskID); err == nil && task != nil {
			return task
		}
	}
	return &Task{ID: msg.TaskID}
}

// drainedAgent is the inbox telling the runtime that an agent has nothing left to
// process. The agent stays resident: it is only marked idle — not stopped, its
// provider session stays open — so the next message continues the same conversation
// on the same session, with no re-attach and no frame to send again. An agent ends
// when it is explicitly ended: a worker when the capability that acquired it
// releases the session, an agent that never attached at all. Nothing ends it just
// because a run finished.
func (r *Autonomy) drainedAgent(agent *Agent) {
	if agent == nil {
		return
	}
	agent.Stop()
}

// runLoop is one instruction's decision loop: decide → execute → observe until the
// decision concludes the task or the budget runs out. input is the message this
// run answers, which every cycle of it carries.
func (r *Autonomy) runLoop(ctx context.Context, agent *Agent, task *Task, input string) error {
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
			decision, err = agent.decide(ctx, cycles, history, input)
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
