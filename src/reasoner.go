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
	agent, err := ctx.Agent.ensureCursorSession(goCtx, model, cwd)
	if err != nil {
		return ReasoningResult{}, err
	}
	stage("ensureCursor", "ok id=%s elapsed=%s", agent.ID, time.Since(tEnsure).Round(time.Millisecond))

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

	stage("Send", "begin agent_id=%s (opening stream; may block until bridge accepts)", agent.ID)
	tSend := time.Now()
	run, err := agent.Send(goCtx, prompt)
	if err != nil {
		return ReasoningResult{}, fmt.Errorf("cursor send: %w", err)
	}
	stage("Send", "stream opened elapsed=%s", time.Since(tSend).Round(time.Millisecond))

	stage("Wait", "begin draining run stream (model inference usually lives here)")
	tWait := time.Now()
	result, err := run.Wait(goCtx)
	if err != nil {
		return ReasoningResult{}, fmt.Errorf("cursor wait: %w", err)
	}
	stage("Wait", "done status=%s text_bytes=%d elapsed=%s total=%s",
		result.Status, len(result.Text), time.Since(tWait).Round(time.Millisecond), time.Since(t0).Round(time.Millisecond))

	text := strings.TrimSpace(result.Text)
	if text == "" {
		return ReasoningResult{}, fmt.Errorf("cursor run returned empty text (status=%s msg=%s)", result.Status, result.ErrorMessage)
	}

	stage("parse", "begin")
	reason, action, err := parseDecision(text)
	if err != nil {
		recordReasonIO(ctx, prompt, text)
		return ReasoningResult{}, fmt.Errorf("cursor decision: %w\nraw=%s", err, text)
	}
	stage("parse", "ok reason=%q total=%s", reason, time.Since(t0).Round(time.Millisecond))
	recordReasonIO(ctx, prompt, text)
	return ReasoningResult{
		Decision: Decision{
			Reason: reason,
			Action: action,
			Ctx:    ctx,
		},
	}, nil
}

func recordReasonIO(ctx DecisionContext, input, output string) {
	turn := ReasonTurn{
		Step:   ctx.Step,
		Input:  input,
		Output: output,
	}
	if ctx.Task != nil {
		turn.TaskID = ctx.Task.ID
	}
	if ctx.Agent != nil {
		turn.AgentID = ctx.Agent.ID
	}
	persistReasonTurn(turn)
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
			in = fmt.Sprintf("local-reasoner task_id=%s goal=%s target=%s", ctx.Task.ID, ctx.Task.Goal, ctx.Task.Target)
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
