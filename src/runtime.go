package autonomy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/capability"
	"github.com/kaulie/autonomy/src/capability/broker"
	"github.com/kaulie/autonomy/src/llmrun"
)

// Runtime reliably executes actions and exposes agent acquisition to capabilities.
type Runtime struct {
	caps   map[string]capability.Capability
	world  *World
	agents *AgentFactory
	// cycle is the decision cycle Execute is running, so a capability can ask
	// what cycle it is delegating from (see WorkerPlaceholders). Execution is
	// synchronous — one cycle at a time on the calling goroutine — so this is
	// that cycle while its actions run, and nil outside one.
	cycle *DecisionContext
	// stepSessions collects the agents acquired while the current step runs, so the
	// step can record which providers it talked to (see recordStep). Reset per step.
	stepSessions []*runtimeAgentSession
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
	// The cycle's context is what a capability delegating out of this cycle hands
	// to its worker (see WorkerPlaceholders). It lives only for this call.
	r.cycle = &decision.Ctx
	defer func() { r.cycle = nil }()

	planID, planned, err := r.recordPlan(decision)
	if err != nil {
		return Result{Err: err}, err
	}
	if len(decision.Actions) == 0 {
		return Result{Message: fmt.Sprintf("decision %s: nothing to execute", decision.Type)}, nil
	}
	result := Result{}
	ran := make([]string, 0, len(decision.Actions))
	for i, action := range decision.Actions {
		if action == nil {
			continue
		}
		r.stepSessions = nil
		started := time.Now()
		record, err := action.Execute(decision.Ctx)
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
		for _, turnID := range sess.turns {
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

func plannedInput(action Action) string {
	if cap, ok := action.(CapabilityAction); ok {
		return jsonObject(cap.Input)
	}
	return "{}"
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
	agent.Lifecycle = AgentLifecycleEphemeral
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
	if backend == "" || backend == string(AgentBackendCursor) {
		// Capabilities name a purpose ("an autonomous coding agent"), not a
		// vendor: the legacy "cursor" label means "the host default backend",
		// which AUTONOMY_LLM_BACKEND selects at runtime.
		backend = string(defaultAgentBackend())
	}
	switch AgentBackend(backend) {
	case AgentBackendCursor:
		if err := agent.AttachCursor(ctx, opts.Model); err != nil {
			r.releaseRegistered(agent)
			return nil, err
		}
	case AgentBackendCline:
		if err := agent.AttachCline(ctx); err != nil {
			r.releaseRegistered(agent)
			return nil, err
		}
	case AgentBackendLocal:
		// Local-only session: no LLM attach; Prompt is unsupported.
	default:
		r.releaseRegistered(agent)
		return nil, fmt.Errorf("unknown agent backend %q", opts.Backend)
	}
	agent.Start()
	sess := &runtimeAgentSession{rt: r, agent: agent, taskID: opts.TaskID, providerName: string(backend)}
	// Inside a cycle, this acquisition belongs to the step that made it; the step
	// records it as one of its interactions (see recordStep).
	if r.cycle != nil {
		r.stepSessions = append(r.stepSessions, sess)
	}
	return sess, nil
}

func (r *Runtime) releaseRegistered(agent *Agent) {
	if agent == nil {
		return
	}
	agent.disposeCursorSession(context.Background())
	softDeleteAgent(agent.ID)
	r.agents.Delete(agent.Name)
}

type runtimeAgentSession struct {
	rt     *Runtime
	agent  *Agent
	taskID string
	// round counts the prompts this session has sent. cycle is always *this
	// agent's own* round number, starting at 1: an agent's cycles are its own,
	// whether it is planning a task or working through a delegated job (and a
	// delegated agent may well have decision cycles of its own). Nothing here
	// compares two agents' cycles. A session is prompted sequentially — a
	// capability asks and waits — so a plain counter is enough.
	round int
	// turns are the runs this session recorded, in order: what a step that used
	// this session talked to (see recordStep).
	turns []int64
	// providerName is the backend this session runs on, remembered at acquisition
	// because Release detaches the agent and the step is recorded after that.
	providerName string
}

// provider is the LLM backend behind this session, which is who the interaction
// was with.
func (s *runtimeAgentSession) provider() string {
	if s == nil {
		return ""
	}
	if s.providerName != "" {
		return s.providerName
	}
	if s.agent == nil {
		return ""
	}
	return string(s.agent.effectiveBackend())
}

func (s *runtimeAgentSession) ID() string {
	if s == nil || s.agent == nil {
		return ""
	}
	return s.agent.Name
}

// Workspace is the sandbox this agent works in. It is the agent's own
// AGENT_WORKSPACE unless AcquireAgent was asked to override it.
func (s *runtimeAgentSession) Workspace() string {
	if s == nil || s.agent == nil {
		return ""
	}
	return s.agent.Workspace
}

// WorkerPlaceholders lets a capability ask this session's host for the runtime
// context of the delegation it was acquired for (see
// broker.WorkerPromptContext): the session speaks for this worker agent.
func (s *runtimeAgentSession) WorkerPlaceholders() map[string]string {
	if s == nil || s.rt == nil {
		return nil
	}
	return s.rt.WorkerPlaceholders(s.agent)
}

// beginDelegatedTrace opens the run header for a prompt this agent received from
// another agent (a capability delegating a sub-task to it), so the recorded input
// row is attributed to the agent that delegated rather than to the user: the user
// only authors the top-level task.
//
// Its cycle is this agent's own round number, from 1 — not the delegating agent's
// cycle: every cycle is relative to the agent it belongs to, and the delegating
// agent's cycle appears as delegated_by.cycle in the prompt's Runtime Context.
func (s *runtimeAgentSession) beginDelegatedTrace(prompt string) *LLMTrace {
	s.round++
	trace := BeginLLMTraceFrom(s.agent, LLMMessageRoleAgent, s.taskID, s.round, ReasonModeAgent, prompt)
	if trace != nil && trace.handle.TurnID != 0 {
		s.turns = append(s.turns, trace.handle.TurnID)
	}
	return trace
}

func (s *runtimeAgentSession) Prompt(ctx context.Context, prompt string) (string, error) {
	if s == nil || s.agent == nil {
		return "", fmt.Errorf("nil agent session")
	}
	switch s.agent.effectiveBackend() {
	case AgentBackendCursor, AgentBackendCline:
	default:
		return "", fmt.Errorf("prompt unsupported for backend %q", s.agent.Backend)
	}
	idle := llmrun.IdleTimeout()
	wd := llmrun.NewIdleWatchdog(ctx, idle)
	defer wd.Stop()
	goCtx := llmrun.WithIdleWatchdog(wd.Context(), wd)

	retries := turnRetryBudget()
	for attempt := 0; ; attempt++ {
		trace := s.beginDelegatedTrace(prompt)
		text, runRes, err := s.agent.PromptLLMStream(goCtx, prompt, ReasonModeAgent, trace.Emit)
		if err == nil {
			runRes.RawOutput = text
			trace.Finish(runRes)
			return text, nil
		}
		trace.Finish(runRes)
		// A turn the model's output limit cut off is the one failed run worth
		// another turn: the session is intact, nothing of the truncated turn ran,
		// and the model can be told to finish the rest in smaller steps. Every
		// other failure (a dead session, a provider error, an exhausted account)
		// is reported as it always was.
		if attempt >= retries || !isTruncatedTurn(err) {
			return "", err
		}
		reminder, rerr := turnTruncatedPrompt(broker.WorkerFrame(s))
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] truncated turn on %s, no retry: %v\n", s.agent.Name, rerr)
			return "", err
		}
		fmt.Fprintf(os.Stderr, "[autonomy] truncated turn on %s (retry %d/%d): %v — asking for the rest in smaller steps\n",
			s.agent.Name, attempt+1, retries, err)
		prompt = reminder
	}
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

func (s *runtimeAgentSession) Release(ctx context.Context) error {
	if s == nil || s.agent == nil || s.rt == nil {
		return nil
	}
	s.agent.Stop()
	if ctx == nil {
		ctx = context.Background()
	}
	s.agent.disposeCursorSession(ctx)
	s.agent.disposeClineSession(ctx)
	if s.agent.IsEphemeral() {
		softDeleteAgent(s.agent.ID)
		s.rt.agents.Delete(s.agent.Name)
	} else {
		persistAgent(s.agent)
	}
	s.agent = nil
	return nil
}

var _ broker.AgentBroker = (*Runtime)(nil)
