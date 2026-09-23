package autonomy

import (
	"context"
	"fmt"
	"github.com/kaulie/autonomy/src/llmbackend"
	"os"
	"strings"
	"time"
)

type ReasoningInput struct {
	Text string
}

type ReasoningResult struct {
	Decision Decision
	// Origin is where this decision came from in the LLM record (the run and the
	// messages), empty when the reasoner recorded nothing.
	Origin DecisionOrigin
}

type Reasoner interface {
	Reason(ctx DecisionContext, input ReasoningInput) (ReasoningResult, error)
}

type Reason struct {
	ID          string
	Description string
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// LLMReasoner asks the agent's own LLM session for one turn and reads the answer as a
// decision.
//
// Requires the agent's session (Agent.Session): which backend, model and workspace a
// turn runs on, and where it is recorded, belong to the session rather than to the
// reasoner — a turn is the same thing whether a task's planner or a delegated worker
// takes it (see LLMSession).
type LLMReasoner struct{}

func NewLLMReasoner() Reasoner {
	return &LLMReasoner{}
}

func (r *LLMReasoner) Reason(ctx DecisionContext, input ReasoningInput) (ReasoningResult, error) {
	if ctx.Agent == nil {
		return ReasoningResult{}, fmt.Errorf("llm reasoner requires DecisionContext.Agent")
	}
	sess := ctx.Agent.Session
	if sess == nil {
		return ReasoningResult{}, fmt.Errorf("llm reasoner requires the agent's session (NewLLMSession; a task's agent gets one in Autonomy.Run)")
	}

	parent := context.Background()
	if ctx.Context != nil {
		parent = ctx.Context
	}

	t0 := time.Now()
	stage := func(name string, format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		fmt.Fprintf(os.Stderr, "[LLMReasoner %s +%s] %s — %s\n",
			time.Now().Format("15:04:05.000"),
			time.Since(t0).Round(time.Millisecond),
			name,
			msg,
		)
	}

	stage("start", "agent=%s model=%s cycle=%d", ctx.Agent.Name, ctx.Agent.Model, ctx.Cycle)

	stage("prompt", "building")
	tPrompt := time.Now()
	prompt, sentFrame, err := reasoningPrompt(ctx, input)
	if err != nil {
		return ReasoningResult{}, fmt.Errorf("build prompt: %w", err)
	}
	stage("prompt", "ready bytes=%d frame=%v elapsed=%s", len(prompt), sentFrame, time.Since(tPrompt).Round(time.Millisecond))
	if llmbackend.TraceVerbose() {
		stage("prompt", "body:\n%s", prompt)
	}

	stage("prompt_run", "begin cycle=%d", ctx.Cycle)
	tSend := time.Now()
	// One turn on this agent's own session, recorded as the decision cycle it is:
	// the plan it produces points back at the reply it came from (Origin), not at a
	// cycle number.
	turn, err := sess.Say(parent, prompt, ctx.Cycle)
	if err != nil {
		return ReasoningResult{}, err
	}
	stage("prompt_run", "done text_bytes=%d elapsed=%s total=%s",
		len(turn.Text), time.Since(tSend).Round(time.Millisecond), time.Since(t0).Round(time.Millisecond))

	stage("parse", "begin")
	decision, parseErr := parseDecision(turn.Text)
	if parseErr != nil {
		return ReasoningResult{}, fmt.Errorf("cursor decision: %w\nraw=%s", parseErr, turn.Text)
	}
	decision.Ctx = ctx
	stage("parse", "ok type=%s reason=%q actions=%d total=%s",
		decision.Type, decision.Reason, len(decision.Actions), time.Since(t0).Round(time.Millisecond))
	// The plan the runtime writes points back at this reply (and the input it
	// answered) by message id, not by cycle number.
	return ReasoningResult{Decision: decision, Origin: turn.Origin}, nil
}

// reasoningPrompt builds the message for one decision cycle. The AGENT_V2 frame
// (the instructions that do not change per cycle) travels only on a session's
// first cycle; later cycles send just the delta, because the session already
// holds the frame. See Agent.needsLLMFrame.
func reasoningPrompt(ctx DecisionContext, input ReasoningInput) (string, bool, error) {
	if ctx.Agent.needsLLMFrame() {
		prompt, err := buildReasoningPrompt(ctx, input)
		return prompt, true, err
	}
	prompt, err := buildReasoningDelta(ctx, input)
	return prompt, false, err
}

func recordReasonIO(ctx DecisionContext, input, output string) {
	var agent *Agent
	var taskID string
	if ctx.Agent != nil {
		agent = ctx.Agent
	}
	if ctx.Task != nil {
		taskID = ctx.Task.ID
	}
	// One-shot (non-streaming) recording: top-level decisions run in plan mode,
	// while runtime capability prompts (recordAgentPrompt) run in agent mode.
	// Streamed provider runs use BeginLLMTrace instead.
	recordReasonTurn(agent, taskID, ctx.Cycle, ReasonModePlan, input, output)
}

type LocalReasoner struct {
	model string
}

func NewLocalReasoner(model string) Reasoner {
	return &LocalReasoner{model: model}
}

func (r *LocalReasoner) Reason(ctx DecisionContext, input ReasoningInput) (ReasoningResult, error) {
	in := strings.TrimSpace(input.Text)
	if in == "" {
		in = "local-reasoner: no additional input"
		if ctx.Task != nil {
			in = fmt.Sprintf("local-reasoner task_id=%s", ctx.Task.ID)
		}
	}
	out := `{"type":"plan","reason":"local reason","plan":[{"capability":"asset.change","input":{},"expected_effect":"the asset's state changes"}],"need":{}}`
	recordReasonIO(ctx, in, out)
	return ReasoningResult{
		Decision: Decision{
			Type:   "plan",
			Reason: "local reason",
			Actions: []Action{CapabilityAction{
				Name:           "asset.change",
				Inputs:         map[string]StepInput{},
				ExpectedEffect: "the asset's state changes",
			}},
			Ctx: ctx,
		},
	}, nil
}
