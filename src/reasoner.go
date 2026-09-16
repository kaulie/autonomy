package autonomy

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/cursorsdk"
	"github.com/kaulie/autonomy/src/llmrun"
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

// LLMReasoner uses the official Cursor SDK Bridge via the Go cursorsdk adapter.
// Requires CURSOR_API_KEY and cursor-sdk-bridge (CURSOR_SDK_BRIDGE_BIN or third_party/bin).
// Cursor agent lifetime follows Autonomy Agent.Lifecycle (create once per task; Delete on ephemeral finish).
type LLMReasoner struct {
	model string
	cwd   string
}

func NewLLMReasoner(model string) Reasoner {
	return &LLMReasoner{model: model}
}

func (r *LLMReasoner) Reason(ctx DecisionContext, input ReasoningInput) (ReasoningResult, error) {
	if ctx.Agent == nil {
		return ReasoningResult{}, fmt.Errorf("llm reasoner requires DecisionContext.Agent")
	}

	cwd := r.cwd
	if cwd == "" && ctx.Agent != nil && ctx.Agent.Workspace != "" {
		cwd = ctx.Agent.Workspace
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	model := r.model
	if model == "" {
		model = os.Getenv("AUTONOMY_LLM_MODEL")
	}
	if model == "" {
		model = "composer-2"
	}

	parent := context.Background()
	if ctx.Context != nil {
		parent = ctx.Context
	}
	// A run is never capped by wall clock: the watchdog only aborts it after
	// AUTONOMY_LLM_TIMEOUT (default 3m) with no provider activity.
	idle := llmrun.IdleTimeout()
	wd := llmrun.NewIdleWatchdog(parent, idle)
	defer wd.Stop()
	goCtx := llmrun.WithIdleWatchdog(wd.Context(), wd)

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

	stage("start", "model=%s idle=%s cwd=%s lifecycle=%s", model, idle, cwd, ctx.Agent.Lifecycle)

	stage("ensureSession", "begin")
	tEnsure := time.Now()
	session, err := ctx.Agent.ensureLLMSession(goCtx, model, cwd, ReasonModePlan)
	if err != nil {
		return ReasoningResult{}, err
	}
	stage("ensureSession", "ok session=%s elapsed=%s", session, time.Since(tEnsure).Round(time.Millisecond))

	stage("prompt", "building")
	tPrompt := time.Now()
	prompt, sentFrame, err := reasoningPrompt(ctx, input)
	if err != nil {
		return ReasoningResult{}, fmt.Errorf("build prompt: %w", err)
	}
	stage("prompt", "ready bytes=%d frame=%v elapsed=%s", len(prompt), sentFrame, time.Since(tPrompt).Round(time.Millisecond))
	if cursorsdk.TraceVerbose() {
		stage("prompt", "body:\n%s", prompt)
	}

	stage("prompt_run", "begin session=%s", session)
	tSend := time.Now()
	trace := BeginLLMTrace(ctx.Agent, reasonTaskID(ctx), ctx.Cycle, ReasonModePlan, prompt)
	text, runRes, err := ctx.Agent.PromptLLMStream(goCtx, prompt, ReasonModePlan, trace.Emit)
	if err != nil {
		trace.Finish(runRes)
		return ReasoningResult{}, err
	}
	// The frame counts as delivered only once the session actually answered: a
	// failed first cycle must resend it instead of leaving the session without
	// its instructions.
	ctx.Agent.markLLMFrameSent()
	stage("prompt_run", "done text_bytes=%d elapsed=%s total=%s",
		len(text), time.Since(tSend).Round(time.Millisecond), time.Since(t0).Round(time.Millisecond))

	stage("parse", "begin")
	decision, parseErr := parseDecision(text)
	runRes.RawOutput = text
	trace.Finish(runRes)
	if parseErr != nil {
		return ReasoningResult{}, fmt.Errorf("cursor decision: %w\nraw=%s", parseErr, text)
	}
	decision.Ctx = ctx
	stage("parse", "ok type=%s reason=%q actions=%d total=%s",
		decision.Type, decision.Reason, len(decision.Actions), time.Since(t0).Round(time.Millisecond))
	// The plan the runtime writes points back at this reply (and the input it
	// answered) by message id, not by cycle number.
	return ReasoningResult{Decision: decision, Origin: trace.Origin()}, nil
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

// reasonTaskID returns the task id carried by a decision context, if any.
func reasonTaskID(ctx DecisionContext) string {
	if ctx.Task == nil {
		return ""
	}
	return ctx.Task.ID
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
