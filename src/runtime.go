package autonomy

import "github.com/kaulie/autonomy/src/llmbackend"

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/capability"
	"github.com/kaulie/autonomy/src/capability/broker"
)

// Runtime reliably executes actions and exposes agent acquisition to capabilities.
type Runtime struct {
	caps   map[string]capability.Capability
	world  *World
	agents *AgentFactory
	// cycle is the decision cycle the in-flight Execute is running, so a capability
	// can ask what cycle it is delegating from (see WorkerPlaceholders). Execute may
	// run on a worker goroutine (Autonomy.dispatchExecute); one Execute at a time
	// still owns this field for the duration of that call, and nil outside one.
	cycle *DecisionContext
	// stepSessions collects the agents acquired while the current step runs, so the
	// step can record which providers it talked to (see recordStep). Reset per step.
	stepSessions []*LLMSession
	// inbox is every agent's message queue (src/inbox.go). The runtime does not own
	// it — the process does — but a session a capability is handed needs it: a
	// capability's prompt is a message from the delegating agent, so it goes
	// through the worker's inbox instead of straight onto its session.
	inbox *Inbox
}

// SetInbox hands the runtime the process's inbox (bootstrap) — see Inbox.
func (r *Runtime) SetInbox(inbox *Inbox) {
	if r == nil {
		return
	}
	r.inbox = inbox
}

func NewRuntime(agents *AgentFactory, caps ...capability.Capability) *Runtime {
	m := make(map[string]capability.Capability, len(caps))
	for _, c := range caps {
		m[c.Name()] = c
	}
	return &Runtime{caps: m, agents: agents}
}

// SetCapabilities replaces the capability lookup table (used after RegisterDefaults).
func (r *Runtime) SetCapabilities(caps ...capability.Capability) {
	m := make(map[string]capability.Capability, len(caps))
	for _, c := range caps {
		m[c.Name()] = c
	}
	r.caps = m
}

// Execute runs a decision: every action of its plan, in order. A plan is one
// cycle's worth of work, so all of its steps run here; the first failing step
// ends the cycle (the next decision sees the world it left behind). done /
// blocked / need_input carry no actions and execute nothing.
//
// The plan is written *before* the first step runs (execution_plan plus one
// execution_step_plan row per step), because that is what makes "planned but never
// executed" answerable: a step with no execution_step row never ran. Writing the
// plan is authoritative — if it cannot be written, nothing runs — while the records
// of what happened are best effort, since the step happened either way.
//
// Each action's record — its raw input and output — is collected into the Result,
// per action and in order, so the next decision sees what actually happened
// (previous_actions) and can decide for itself which entry matters.
func (r *Runtime) Execute(decision Decision) (Result, error) {
	return r.executePlan(decision, 0, nil)
}

// ExecuteApproved runs a decision whose plan was already written while the run
// waited for the user to confirm it (RecordPlanOnly / Agent.RequirePlanApproval): the
// plan row and its steps exist, so this runs the steps and records what each did,
// without writing the plan again. planID and planned are what RecordPlanOnly returned.
func (r *Runtime) ExecuteApproved(decision Decision, planID int64, planned []ExecutionStepPlan) (Result, error) {
	return r.executePlan(decision, planID, planned)
}

// RecordPlanOnly writes a decision's plan and its steps without running any of them,
// and returns the plan id with the steps as stored. A run that must wait for the user
// to confirm a plan (Agent.RequirePlanApproval) pauses through here: the plan is on
// record — what the user reviews — and no execution_step belongs to it, which is
// exactly what "planned but never executed" means (src/execution-loop.md).
func (r *Runtime) RecordPlanOnly(decision Decision) (int64, []ExecutionStepPlan, error) {
	r.cycle = &decision.Ctx
	defer func() { r.cycle = nil }()

	if err := validateDecision(decision); err != nil {
		return 0, nil, err
	}
	if err := validatePlanLineage(decision.Actions); err != nil {
		return 0, nil, err
	}
	if decision.Ctx.Cycle == 1 {
		if err := validateCompletionContract(decision.Contract, decision.Actions); err != nil {
			return 0, nil, err
		}
	}
	planID, planned, err := r.recordPlan(decision)
	if err != nil {
		return planID, nil, err
	}
	// Pinned where the first plan is written, whether or not it runs yet: the contract
	// belongs to the task and the row it came in with is what makes it traceable
	// (src/completion_contract.go). ExecuteApproved does not pin it again.
	if decision.Ctx.Cycle == 1 {
		pinCompletionContract(decision, planID)
	}
	return planID, planned, nil
}

// executePlan is Execute and ExecuteApproved: with planID 0 the decision's plan is
// validated and written first (the normal path, below); with a planID already
// recorded, the plan exists and only its steps run.
func (r *Runtime) executePlan(decision Decision, planID int64, planned []ExecutionStepPlan) (Result, error) {
	// The cycle's context is what a capability delegating out of this cycle hands
	// to its worker (see WorkerPlaceholders). It lives only for this call.
	r.cycle = &decision.Ctx
	defer func() { r.cycle = nil }()

	if planID == 0 {
		// The decision's own contract first (AGENT_V2.md §Type-specific Requirements),
		// then the plan's data dependencies: a decision that breaks either does not run
		// at all, so nothing is executed against a plan that was never going to work and
		// the planner gets the reason to re-plan from (src/decision_rules.go,
		// src/plan_lineage.go).
		if err := validateDecision(decision); err != nil {
			return Result{Err: err, Message: err.Error()}, err
		}
		if err := validatePlanLineage(decision.Actions); err != nil {
			return Result{Err: err, Message: err.Error()}, err
		}
		// The contract this run will be judged by arrives with its first answer, and is
		// checked before it is pinned: a contract the runtime could never evaluate must not
		// become the standard the task ends up being held to (src/verification.go).
		if decision.Ctx.Cycle == 1 {
			if err := validateCompletionContract(decision.Contract, decision.Actions); err != nil {
				return Result{Err: err, Message: err.Error()}, err
			}
		}

		var err error
		planID, planned, err = r.recordPlan(decision)
		if err != nil {
			return Result{Err: err}, err
		}
		// Pinned after the plan row is written — the contract belongs to the task, and the
		// row it came in with is what makes it traceable. Only the first answer pins one:
		// a later cycle restating its contract changes nothing (src/completion_contract.go).
		if decision.Ctx.Cycle == 1 {
			pinCompletionContract(decision, planID)
		}
	}
	if len(decision.Actions) == 0 {
		// A `done` is the one decision that claims the task is over, so it is the one
		// decision the runtime verifies: the contract's criteria, judged against the
		// world by the authoritative sources (src/verification.go). A claim that did not
		// hold up is this cycle's failure — the planner re-plans from the verdict.
		if isDone(decision) {
			if err := r.verifyDone(decision.Ctx, planID); err != nil {
				return Result{Err: err, Message: err.Error()}, err
			}
		}
		return Result{Message: fmt.Sprintf("decision %s: nothing to execute", decision.Type)}, nil
	}
	result := Result{}
	ran := make([]string, 0, len(decision.Actions))
	// Each step runs with the steps before it in hand: that is what its input
	// bindings resolve against (src/plan_lineage.go). The plan's own order is the
	// lineage's horizon — a binding can never read a later step, nor another cycle's.
	ctx := decision.Ctx
	for i, action := range decision.Actions {
		if action == nil {
			continue
		}
		r.stepSessions = nil
		started := time.Now()
		ctx.StepOutputs = result.Actions
		record, err := action.Execute(ctx)
		if err != nil {
			record.Error = err.Error()
		}
		record.PlanStepID = planStepID(planned, i)
		record.ExecutionStepID = r.recordStep(decision, planID, planned, i, record, started, err)
		result.Actions = append(result.Actions, record)
		ran = append(ran, record.Capability)
		if err != nil {
			result.Err = err
			result.Message = fmt.Sprintf("action %d/%d (%s) failed: %v", i+1, len(decision.Actions), record.Capability, err)
			return result, err
		}
	}
	result.Message = fmt.Sprintf("executed %d action(s): %s", len(ran), strings.Join(ran, ", "))
	return result, nil
}

// AcquireAgent registers an agent via AgentFactory and attaches the requested backend.

// recordPlan writes the decision's plan and its steps, and returns the plan's id
// with the planned steps in order. A decision with no actions still gets its row:
// done / blocked / need_input are answers too, and they are worth finding later by
// the reply they came from.
func (r *Runtime) recordPlan(decision Decision) (int64, []ExecutionStepPlan, error) {
	ctx := decision.Ctx
	plan := ExecutionPlan{
		DecisionType: strings.TrimSpace(decision.Type),
		Reason:       decision.Reason,
		Evidence:     jsonArrayOf(decision.Evidence),
		Need:         jsonNeed(decision.Need),
		StepCount:    len(decision.Actions),
		PlanHash:     planHash(decision.Actions),
		ReasonTurnID: decision.Origin.ReasonTurnID,
	}
	if ctx.Task != nil {
		plan.TaskID = ctx.Task.ID
	}
	if ctx.Agent != nil {
		plan.AgentID = ctx.Agent.ID
	}
	plan.Cycle = ctx.Cycle
	plan.InputMessageID = decision.Origin.InputMessageID
	plan.ReplyMessageID = decision.Origin.ReplyMessageID
	// The task's own first input: whatever an earlier plan of this task recorded,
	// else this plan's input — which, for the first plan, *is* the task's input.
	if plan.TaskID != "" {
		if id, found := taskInputMessageID(plan.TaskID); found {
			plan.TaskInputMessageID = id
		} else {
			plan.TaskInputMessageID = plan.InputMessageID
		}
	}
	steps := make([]ExecutionStepPlan, 0, len(decision.Actions))
	for i, action := range decision.Actions {
		if action == nil {
			continue
		}
		steps = append(steps, ExecutionStepPlan{
			Idx:            i + 1,
			Name:           plannedName(action),
			Capability:     plannedCapability(action),
			Input:          plannedInput(action),
			ExpectedEffect: plannedExpectedEffect(action),
			EvidenceRefs:   plannedEvidenceRefs(action),
		})
	}
	planID, saved, err := saveExecutionPlan(plan, steps)
	if err != nil {
		return planID, nil, fmt.Errorf("record plan: %w", err)
	}
	return planID, saved, nil
}

// recordStep writes what one action did, linked to the plan step it carries out,
// and records the providers it talked to. Both are best effort: the step happened,
// and the plan written above is the authority on what it was supposed to do.
func (r *Runtime) recordStep(decision Decision, planID int64, planned []ExecutionStepPlan, i int, record ActionResult, started time.Time, actionErr error) int64 {
	if planID == 0 {
		return 0
	}
	status := "ok"
	if actionErr != nil {
		status = "failed"
	}
	ctx := decision.Ctx
	step := ExecutionStep{
		PlanID:     planID,
		PlanStepID: planStepID(planned, i),
		TaskID:     taskIDOf(ctx),
		AgentID:    agentIDOf(ctx),
		Cycle:      ctx.Cycle,
		Idx:        i + 1,
		Name:       strings.TrimSpace(record.StepName),
		Capability: record.Capability,
		Provider:   r.providerOf(record.Capability),
		Status:     status,
		Input:      jsonObject(record.Input),
		Output:     jsonObject(record.Output),
		Error:      record.Error,
		StartedAt:  started,
		EndedAt:    time.Now(),
		DurationMS: time.Since(started).Milliseconds(),
	}
	stepID := saveExecutionStep(step)
	if stepID == 0 {
		return 0
	}
	// What this step talked to: the agents it acquired, one interaction each — a
	// step may have several, which is why they have their own rows. A step that
	// acquired none is a capability talking to an API or to the world in process,
	// and that interaction detail is not recorded yet (docs/execution-step.md).
	seq := 0
	for _, sess := range r.stepSessions {
		for _, turnID := range sess.Turns() {
			seq++
			saveExecutionStepInteraction(ExecutionStepInteraction{
				StepID:       stepID,
				Seq:          seq,
				Kind:         InteractionLLM,
				Provider:     sess.provider(),
				ReasonTurnID: turnID,
			})
		}
	}
	r.stepSessions = nil
	return stepID
}

func taskIDOf(ctx DecisionContext) string {
	if ctx.Task == nil {
		return ""
	}
	return ctx.Task.ID
}

func agentIDOf(ctx DecisionContext) int64 {
	if ctx.Agent == nil {
		return 0
	}
	return ctx.Agent.ID
}

// providerOf is who a step's capability really talks to: the capability's own
// provider (github, agent-control-plane, autonomy, …), so a step row says where its
// effect happened even when it acquired no agent.
func (r *Runtime) providerOf(capability string) string {
	if r == nil {
		return ""
	}
	if cap, ok := r.caps[strings.ToLower(strings.TrimSpace(capability))]; ok {
		return cap.Provider()
	}
	return ""
}

// planStepID is the planned step row an action belongs to (0 when the plan has no
// such step): the row, not the position, so a re-plan's identical step is a
// different row and can never be confused with this one.
func planStepID(planned []ExecutionStepPlan, i int) int64 {
	for _, step := range planned {
		if step.Idx == i+1 {
			return step.ID
		}
	}
	return 0
}

// plannedCapability / plannedInput / … read a plan step out of the action the
// planner gave, so the plan rows say what the planner asked for while the execution
// rows say what was actually called.
func plannedCapability(action Action) string {
	if cap, ok := action.(CapabilityAction); ok {
		return strings.ToLower(strings.TrimSpace(cap.Name))
	}
	return ""
}

// plannedName is what the plan called a step, which is what another step's binding
// addresses ("step:<name>.output.<key>"). It is recorded so the lineage in a stored
// plan's input can be resolved against the rows it names.
func plannedName(action Action) string {
	if cap, ok := action.(CapabilityAction); ok {
		return strings.TrimSpace(cap.StepName)
	}
	return ""
}

// plannedInput renders the plan step's own input as JSON: the literals the planner
// wrote, and its bindings as {"source":"…"} — the planner's raw input, which is what
// the plan row keeps. What the capability was actually called with is the step's
// record (execution_step.input), where the bindings are resolved
// (docs/execution-step.md).
func plannedInput(action Action) string {
	step, ok := capabilityStep(action)
	if !ok || len(step.Inputs) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(step.Inputs)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func plannedExpectedEffect(action Action) string {
	if cap, ok := action.(CapabilityAction); ok {
		return cap.ExpectedEffect
	}
	return ""
}

func plannedEvidenceRefs(action Action) string {
	if cap, ok := action.(CapabilityAction); ok {
		return jsonArrayOf(cap.EvidenceRefs)
	}
	return "[]"
}

// jsonArrayOf renders a plan's evidence / a step's evidence refs as the JSON array
// the execution tables store.
func jsonArrayOf[T any](v []T) string {
	if len(v) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

// jsonNeed renders what a blocked / need_input decision asked for, verbatim.
func jsonNeed(need Need) string {
	if strings.TrimSpace(need.Type) == "" && strings.TrimSpace(need.Description) == "" {
		return "{}"
	}
	raw, err := json.Marshal(need)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// planHash fingerprints a plan's steps (capability and input, in order). A re-plan
// is always a new plan; the hash only says it plans the same thing, which is the
// machine-readable sign of a loop.
func planHash(actions []Action) string {
	h := sha256.New()
	for i, action := range actions {
		if action == nil {
			continue
		}
		fmt.Fprintf(h, "%d|%s|%s|", i+1, plannedCapability(action), plannedInput(action))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Capabilities call this instead of creating Cursor clients themselves.
func (r *Runtime) AcquireAgent(ctx context.Context, opts broker.AcquireAgentOpts) (broker.AgentSession, error) {
	if r == nil || r.agents == nil {
		return nil, fmt.Errorf("runtime agent factory not ready")
	}
	agent := r.agents.NewAgent()
	// Acquired for one delegated job: the capability named what it is for, and
	// that purpose is what the agent's own prompt says it is (## Agent).
	agent.Role = AgentRoleWorker
	agent.Purpose = strings.TrimSpace(opts.Purpose)
	// A worker is kept like any other agent unless the capability asked for a
	// throwaway one: its row records what it was acquired for, and its provider
	// session stays resumable (see NewAgent, docs/agent.md).
	agent.Lifecycle = AgentLifecyclePersistent
	if opts.Ephemeral {
		agent.Lifecycle = AgentLifecycleEphemeral
	}
	// A capability-acquired agent works on the same task as the agent that
	// delegated to it, so its row carries that task id too (current_task_id used
	// to stay empty for delegated workers even though their runs already recorded
	// the task). Only the id is known at this point; nothing reads CurrentTask for
	// a delegated agent.
	if taskID := strings.TrimSpace(opts.TaskID); taskID != "" {
		agent.CurrentTask = &Task{ID: taskID}
	}
	if ws := strings.TrimSpace(opts.Workspace); ws != "" {
		agent.Workspace = ws
	}
	backend := strings.ToLower(strings.TrimSpace(opts.Backend))
	if backend == "" || backend == string(llmbackend.Cursor) {
		// Capabilities name a purpose ("an autonomous coding agent"), not a
		// vendor: the legacy "cursor" label means "the host default backend",
		// which AUTONOMY_LLM_BACKEND selects at runtime.
		backend = string(llmbackend.DefaultBackend())
	}
	switch llmbackend.Backend(backend) {
	case llmbackend.Cursor:
		if err := agent.AttachCursor(ctx, opts.Model); err != nil {
			r.releaseRegistered(agent)
			return nil, err
		}
	case llmbackend.Cline:
		if err := agent.AttachCline(ctx); err != nil {
			r.releaseRegistered(agent)
			return nil, err
		}
	case llmbackend.Local:
		// Local-only session: no LLM attach; Prompt is unsupported.
	default:
		r.releaseRegistered(agent)
		return nil, fmt.Errorf("unknown agent backend %q", opts.Backend)
	}
	agent.Start()
	// Who is handing this worker its work: the agent that delegated, named on every
	// message it gets (the capability is the hand, the planner is the agent that
	// asked for the job — see docs/delegation.md).
	delegatedBy := ""
	if r.cycle != nil && r.cycle.Agent != nil {
		delegatedBy = r.cycle.Agent.Name
	}
	// The worker's session: the same session a task's own agent runs on, with a
	// worker identity on the agent. What makes its turns delegated turns is that
	// identity (see LLMSession.turnShape) — not a different session type — and it is
	// why the worker's runs are attributed to the task the capability delegated from.
	sess := NewLLMSession(r, agent, SessionOpts{
		TaskID:      strings.TrimSpace(opts.TaskID),
		Model:       opts.Model,
		Provider:    backend,
		DelegatedBy: delegatedBy,
		Inbox:       r.inbox,
	})
	agent.Session = sess
	// Inside a cycle, this acquisition belongs to the step that made it; the step
	// records it as one of its interactions (see recordStep).
	if r.cycle != nil {
		r.stepSessions = append(r.stepSessions, sess)
	}
	return sess, nil
}

// releaseRegistered lets go of an agent that could not be attached. It never
// became an agent this runtime could use, so it is not one that ended: the handle
// goes back and its row goes with it (closeAgent is where the agents that did run
// are kept — see docs/agent.md).
func (r *Runtime) releaseRegistered(agent *Agent) {
	if agent == nil {
		return
	}
	agent.disposeLLMSession(context.Background())
	softDeleteAgent(agent.ID)
	r.agents.Delete(agent.Name)
}

// recordAgentPrompt persists a complete agent-mode interaction in one shot. It
// is retained for callers that already hold the final output; live provider
// runs record through BeginLLMTrace so the stream is captured as it happens.
//
// cycle is 1: cycle counts interaction rounds with an LLM, and this is the first
// (and, for a one-shot recording, only) round of that agent's conversation.
func recordAgentPrompt(agent *Agent, taskID, input, output string) {
	if agent == nil {
		return
	}
	recordReasonTurn(agent, taskID, 1, ReasonModeAgent, input, output)
}

var _ broker.AgentBroker = (*Runtime)(nil)
