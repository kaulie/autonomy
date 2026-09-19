package autonomy

import (
	"context"
	"fmt"
	"os"

	"github.com/kaulie/autonomy/src/capability/broker"
	"github.com/kaulie/autonomy/src/llmrun"
)

// A session is one agent's conversation with its LLM, and it is the same thing
// whoever the agent is: a task's own agent (its planner) takes a turn every decision
// cycle, and a worker a capability acquired takes a turn for every prompt the
// capability sends it. What an agent *is* — planner or worker — is its identity
// (Agent.Role), not a different kind of session.
//
// One turn is therefore one thing, and this is where it is written down: open the
// agent's backend session if it is not open yet, wrap the run in the idle watchdog,
// open the run header (reason_turns), let the provider stream into it, finalize it,
// and — when the provider cut the turn off at its output-token limit — ask once more
// on the same session. Nothing here is about the caller: whoever takes a turn
// supplies the prompt and (when it numbers its own rounds) the round, and everything
// else is derived from the identity the agent was given.
type LLMSession struct {
	rt     *Runtime
	agent  *Agent
	taskID string
	// model is the model to open the backend session with; empty means the agent's
	// own choice, else the host default for the backend (see Agent.AttachCursor).
	model string
	// round counts the turns this session has taken. It is the cycle a turn is
	// recorded as (reason_turns.cycle) where the caller lets the session number its
	// own turns: an agent's rounds are its own, from 1.
	round int
	// turns are the runs this session recorded, in order: what a step that used
	// this session talked to (see recordStep).
	turns []int64
	// providerName is the backend this session runs on, named by whoever opened it
	// (Runtime.AcquireAgent knows the backend it attached). It is kept here because
	// Release detaches the agent and the step is recorded after that.
	providerName string
	// delegatedBy is the agent that handed this session its work, which is the sender
	// of every message a capability's prompt becomes (see SessionOpts.DelegatedBy).
	delegatedBy string
	// inbox is the agent's message queue, when the host has one: a session with an
	// inbox does not prompt the agent directly — it sends it a message and the
	// agent's own consumer processes it (see SessionOpts.Inbox, src/inbox.go).
	inbox *Inbox
}

// SessionOpts is what a session needs to know beyond the agent it runs as: the task
// every turn belongs to, and the model and backend to open its provider session with.
// The agent's identity — planner or worker, and the purpose a worker was acquired for
// — is the agent's own (Agent.Role / Agent.Purpose), and so is where it runs (its
// AGENT_WORKSPACE); none of that is repeated here.
type SessionOpts struct {
	// TaskID is the task every turn of this session is attributed to: it is what
	// reason_turns.task_id records, and what the worker's agents.current_task_id is.
	TaskID string
	// Model overrides the model the backend session is opened with.
	Model string
	// Provider is the backend this session runs on ("cursor", "cline", "local").
	// Empty means the host default (Agent.effectiveBackend).
	Provider string
	// DelegatedBy is the agent that handed this session its job (the planner whose
	// step acquired the worker), which is who every message it receives is from.
	DelegatedBy string
	// Inbox is the agent's message queue (src/inbox.go): a session that has one
	// delivers its prompts as messages, so a worker's turns are processed by its own
	// consumer, in order, like everything else addressed to it.
	Inbox *Inbox
}

// TurnResult is what one turn produced: the text the agent answered with, and where
// that answer is in the LLM record, so a decision can point at the reply it came from
// rather than at a cycle number (DecisionOrigin).
type TurnResult struct {
	Text   string
	Origin DecisionOrigin
}

// RoundAuto is the round for a caller that lets the session number its own turns: a
// delegated worker's turns are its own rounds, 1..n. A caller that numbers its own
// rounds — the planner's decision cycle, which the runtime loop counts — passes that
// number instead, so reason_turns.cycle and the cycle the plan row records stay the
// same number.
const RoundAuto = 0

// NewLLMSession opens the session for one agent. It is how every agent is obtained:
// Runtime.AcquireAgent is this call with a worker identity on the agent, and the agent
// a task's cycles belong to is this call with a planner one. rt may be nil (an agent
// with no host: no World, nothing to delegate from), and its turns still run and are
// recorded.
func NewLLMSession(rt *Runtime, agent *Agent, opts SessionOpts) *LLMSession {
	return &LLMSession{
		rt:           rt,
		agent:        agent,
		taskID:       opts.TaskID,
		model:        opts.Model,
		providerName: opts.Provider,
		delegatedBy:  opts.DelegatedBy,
		inbox:        opts.Inbox,
	}
}

// ID is the agent's name, which is what the session is called in a log.
func (s *LLMSession) ID() string {
	if s == nil || s.agent == nil {
		return ""
	}
	return s.agent.Name
}

// Workspace is where the agent runs (its own AGENT_WORKSPACE), so a capability can
// tell the agent and its caller which workspace the work happens in.
func (s *LLMSession) Workspace() string {
	if s == nil || s.agent == nil {
		return ""
	}
	return s.agent.Workspace
}

// provider is the LLM backend behind this session, which is who the interaction was
// with.
func (s *LLMSession) provider() string {
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

// Turns are the runs this session recorded, in order.
func (s *LLMSession) Turns() []int64 {
	if s == nil {
		return nil
	}
	return s.turns
}

// WorkerPlaceholders lets a capability ask this session's host for the runtime
// context of the delegation it was acquired for (see broker.WorkerPromptContext):
// the session speaks for the worker agent it runs.
func (s *LLMSession) WorkerPlaceholders() map[string]string {
	if s == nil || s.rt == nil {
		return nil
	}
	return s.rt.WorkerPlaceholders(s.agent)
}

// turnShape is what one turn of this session is, derived from the identity the agent
// carries: the mode the agent works in, and who authored the input it answers. A
// worker was acquired by a capability, so its input comes from the agent that
// delegated (the user only authors the top-level task); a task's own agent answers
// the runtime on behalf of the task, so its input is the user's.
func (s *LLMSession) turnShape() (ReasonMode, LLMMessageRole) {
	if s != nil && s.agent != nil && s.agent.Role == AgentRoleWorker {
		return ReasonModeAgent, LLMMessageRoleAgent
	}
	return ReasonModePlan, LLMMessageRoleUser
}

// beginTurn opens the run header for one turn of this session: the task it belongs to,
// the round it is, and the shape its identity gives it. round is RoundAuto for a
// session that numbers its own turns.
func (s *LLMSession) beginTurn(prompt string, round int) *LLMTrace {
	mode, inputRole := s.turnShape()
	if round == RoundAuto {
		s.round++
		round = s.round
	} else {
		s.round = round
	}
	trace := BeginLLMTraceFrom(s.agent, inputRole, s.taskID, round, mode, prompt)
	if trace != nil && trace.handle.TurnID != 0 {
		s.turns = append(s.turns, trace.handle.TurnID)
	}
	return trace
}

// promptable reports whether this session runs on an LLM at all. A session opened as
// local is the agent's own bookkeeping with no backend behind it (AgentBackendLocal):
// there is nothing to prompt, and nothing to attach.
func (s *LLMSession) promptable() bool {
	return AgentBackend(s.provider()) != AgentBackendLocal
}

// workspaceOrCwd is where this session's provider run happens: the agent's own
// workspace, else the process's directory.
func (s *LLMSession) workspaceOrCwd() string {
	if s != nil && s.agent != nil && s.agent.Workspace != "" {
		return s.agent.Workspace
	}
	cwd, _ := os.Getwd()
	return cwd
}

// Say takes one turn: it asks the agent this session runs, records the run as the turn
// it is, and hands back the answer with where it was recorded. round is the round to
// record it as (RoundAuto: the session's next one).
//
// A turn the model's output limit cut off is the one failed run worth another turn:
// the session is intact, nothing of the truncated turn ran, and the agent can be told
// to finish the rest in smaller steps. Every other failure (a dead session, a provider
// error, an exhausted account) is reported as it is.
func (s *LLMSession) Say(ctx context.Context, prompt string, round int) (TurnResult, error) {
	if s == nil || s.agent == nil {
		return TurnResult{}, fmt.Errorf("nil agent session")
	}
	if !s.promptable() {
		return TurnResult{}, fmt.Errorf("prompt unsupported for backend %q", s.provider())
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// A run is never capped by wall clock: the watchdog only aborts it after
	// AUTONOMY_LLM_TIMEOUT (default 3m) with no provider activity.
	wd := llmrun.NewIdleWatchdog(ctx, llmrun.IdleTimeout())
	defer wd.Stop()
	goCtx := llmrun.WithIdleWatchdog(wd.Context(), wd)

	// The backend session is opened here too, not only where the agent was acquired:
	// a task's own agent is created local and gets its backend on its first turn,
	// while an acquired worker is attached when it is acquired. Either way the turn
	// has a session when it runs, because opening it is idempotent.
	mode, _ := s.turnShape()
	if _, err := s.agent.ensureLLMSession(goCtx, s.model, s.workspaceOrCwd(), mode); err != nil {
		return TurnResult{}, err
	}

	retries := turnRetryBudget()
	for attempt := 0; ; attempt++ {
		trace := s.beginTurn(prompt, round)
		text, runRes, err := s.agent.PromptLLMStream(goCtx, prompt, mode, trace.Emit)
		if err == nil {
			runRes.RawOutput = text
			trace.Finish(runRes)
			// The frame counts as delivered only once the session actually answered:
			// a failed first turn resends it instead of leaving the session without
			// its instructions.
			s.agent.markLLMFrameSent()
			return TurnResult{Text: text, Origin: trace.Origin()}, nil
		}
		trace.Finish(runRes)
		if attempt >= retries || !isTruncatedTurn(err) {
			return TurnResult{}, err
		}
		reminder, rerr := turnTruncatedPrompt(broker.WorkerFrame(s))
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] truncated turn on %s, no retry: %v\n", s.agent.Name, rerr)
			return TurnResult{}, err
		}
		fmt.Fprintf(os.Stderr, "[autonomy] truncated turn on %s (retry %d/%d): %v — asking for the rest in smaller steps\n",
			s.agent.Name, attempt+1, retries, err)
		prompt = reminder
	}
}

// Prompt is the session as a capability sees it (broker.AgentSession): one prompt, one
// answer, on this agent's own conversation. A capability does not number rounds — its
// prompts are the worker's next turns.
//
// The prompt is delivered as a message from the delegating agent, so it is
// processed by the worker's own consumer, in order with whatever else reached it
// (src/inbox.go). A host with no inbox (a session built by hand) prompts directly.
func (s *LLMSession) Prompt(ctx context.Context, prompt string) (string, error) {
	if s != nil && s.inbox != nil && s.inbox.store != nil && s.agent != nil {
		res, err := s.inbox.Send(ctx, s.agent, AgentMessage{
			TaskID:   s.taskID,
			Sender:   MessageSenderAgent,
			SenderID: s.delegatedBy,
			Kind:     MessageKindDelegation,
			Content:  prompt,
		})
		if err != nil {
			return "", err
		}
		return res.Text, nil
	}
	res, err := s.Say(ctx, prompt, RoundAuto)
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

// Release hands the agent back (broker.AgentSession) — which is Close: a capability
// that was lent an agent is done with it, and that is when its life ends.
func (s *LLMSession) Release(ctx context.Context) error {
	return s.Close(ctx)
}

// Close ends the session and the agent's life with it (see closeAgent): stop, tear
// down the provider sessions, and let it go — an ephemeral agent is deleted, a
// persistent one is kept so its durable provider-side state can be resumed later.
func (s *LLMSession) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	var factory *AgentFactory
	if s.rt != nil {
		factory = s.rt.agents
	}
	closeAgent(s.agent, factory, ctx)
	s.agent = nil
	return nil
}

// The session is what a capability is handed when it acquires an agent, and what a
// host is asked for the runtime context of that delegation.
var (
	_ broker.AgentSession        = (*LLMSession)(nil)
	_ broker.WorkerPromptContext = (*LLMSession)(nil)
)
