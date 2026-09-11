package autonomy

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/cursorsdk"
)

type ReasoningInput struct {
	Text string
}

type ReasoningResult struct {
	Decision Decision
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
	timeout := llmTimeout()
	goCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

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

	stage("start", "model=%s timeout=%s cwd=%s lifecycle=%s", model, timeout, cwd, ctx.Agent.Lifecycle)

	stage("ensureCursor", "begin")
	tEnsure := time.Now()
	cAgent, err := ctx.Agent.ensureCursorSession(goCtx, model, cwd)
	if err != nil {
		return ReasoningResult{}, err
	}
	stage("ensureCursor", "ok id=%s elapsed=%s", cAgent.ID, time.Since(tEnsure).Round(time.Millisecond))

	stage("prompt", "building")
	tPrompt := time.Now()
	prompt, err := buildReasoningPrompt(ctx, input)
	if err != nil {
		return ReasoningResult{}, fmt.Errorf("build prompt: %w", err)
	}
	stage("prompt", "ready bytes=%d elapsed=%s", len(prompt), time.Since(tPrompt).Round(time.Millisecond))
	if cursorsdk.TraceVerbose() {
		stage("prompt", "body:\n%s", prompt)
	}

	stage("PromptCursor", "begin agent_id=%s", cAgent.ID)
	tSend := time.Now()
	trace := BeginLLMTrace(ctx.Agent, reasonTaskID(ctx), ctx.Step, ReasonModePlan, prompt)
	text, runRes, err := ctx.Agent.PromptCursorStream(goCtx, prompt, trace.Emit)
	if err != nil {
		trace.Finish(runRes)
		return ReasoningResult{}, err
	}
	stage("PromptCursor", "done text_bytes=%d elapsed=%s total=%s",
		len(text), time.Since(tSend).Round(time.Millisecond), time.Since(t0).Round(time.Millisecond))

	stage("parse", "begin")
	reason, action, parseErr := parseDecision(text)
	runRes.RawOutput = text
	trace.Finish(runRes)
	if parseErr != nil {
		return ReasoningResult{}, fmt.Errorf("cursor decision: %w\nraw=%s", parseErr, text)
	}
	stage("parse", "ok reason=%q total=%s", reason, time.Since(t0).Round(time.Millisecond))
	return ReasoningResult{
		Decision: Decision{
			Reason: reason,
			Action: action,
			Ctx:    ctx,
		},
	}, nil
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
	recordReasonTurn(agent, taskID, ctx.Step, ReasonModePlan, input, output)
}

func llmTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("AUTONOMY_LLM_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 3 * time.Minute
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
	out := `{"type":"plan","reason":"local reason","plan":[{"capability":"asset.change","input":{}}],"need":{}}`
	recordReasonIO(ctx, in, out)
	return ReasoningResult{
		Decision: Decision{
			Reason: "local reason",
			Action: CapabilityAction{Name: "asset.change", Input: map[string]string{}},
			Ctx:    ctx,
		},
	}, nil
}
