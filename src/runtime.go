package autonomy

import (
	"context"
	"fmt"
	"strings"

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
// Each action's record — its raw input and output — is collected into the Result,
// per action and in order, so the next decision sees what actually happened
// (previous_actions) and can decide for itself which entry matters.
func (r *Runtime) Execute(decision Decision) (Result, error) {
	// The cycle's context is what a capability delegating out of this cycle hands
	// to its worker (see WorkerPlaceholders). It lives only for this call.
	r.cycle = &decision.Ctx
	defer func() { r.cycle = nil }()

	if len(decision.Actions) == 0 {
		return Result{Message: fmt.Sprintf("decision %s: nothing to execute", decision.Type)}, nil
	}
	result := Result{}
	ran := make([]string, 0, len(decision.Actions))
	for i, action := range decision.Actions {
		if action == nil {
			continue
		}
		record, err := action.Execute(decision.Ctx)
		if err != nil {
			record.Error = err.Error()
		}
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
	return &runtimeAgentSession{rt: r, agent: agent, taskID: opts.TaskID}, nil
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
	return BeginLLMTraceFrom(s.agent, LLMMessageRoleAgent, s.taskID, s.round, ReasonModeAgent, prompt)
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
	trace := s.beginDelegatedTrace(prompt)
	text, runRes, err := s.agent.PromptLLMStream(goCtx, prompt, ReasonModeAgent, trace.Emit)
	if err != nil {
		trace.Finish(runRes)
		return "", err
	}
	runRes.RawOutput = text
	trace.Finish(runRes)
	return text, nil
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
