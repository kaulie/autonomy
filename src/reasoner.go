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
type LLMReasoner struct {
	model string
	cwd   string
}

func NewLLMReasoner(model string) Reasoner {
	return &LLMReasoner{model: model}
}

func (r *LLMReasoner) Reason(ctx DecisionContext, input ReasoningInput) (ReasoningResult, error) {
	cwd := r.cwd
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

	stage("start", "model=%s timeout=%s cwd=%s", model, timeout, cwd)
	client := cursorsdk.NewClient(
		cursorsdk.WithAPIKey(os.Getenv("CURSOR_API_KEY")),
		cursorsdk.WithWorkspace(cwd),
		cursorsdk.WithBridgeBin(os.Getenv("CURSOR_SDK_BRIDGE_BIN")),
	)
	defer func() {
		stage("close", "shutting down client/bridge")
		_ = client.Close()
	}()

	stage("ping", "begin")
	tPing := time.Now()
	if err := client.Ping(goCtx); err != nil {
		return ReasoningResult{}, fmt.Errorf("cursor bridge ping: %w", err)
	}
	stage("ping", "ok elapsed=%s", time.Since(tPing).Round(time.Millisecond))

	stage("CreateAgent", "begin model=%s", model)
	tCreate := time.Now()
	agent, err := client.Agents().Create(goCtx, cursorsdk.CreateOptions{
		Model: model,
		CWD:   cwd,
	})
	if err != nil {
		return ReasoningResult{}, fmt.Errorf("create cursor agent: %w", err)
	}
	defer func() {
		stage("CloseAgent", "begin id=%s", agent.ID)
		_ = agent.Close(goCtx)
	}()
	stage("CreateAgent", "ok id=%s elapsed=%s", agent.ID, time.Since(tCreate).Round(time.Millisecond))

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
		return ReasoningResult{}, fmt.Errorf("cursor decision: %w\nraw=%s", err, text)
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
	return ReasoningResult{
		Decision: Decision{
			Reason: "local reason",
			Action: SimpleAction{},
			Ctx:    ctx,
		},
	}, nil
}
