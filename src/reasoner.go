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

	goCtx := context.Background()
	if ctx.Context != nil {
		goCtx = ctx.Context
	}

	client := cursorsdk.NewClient(
		cursorsdk.WithAPIKey(os.Getenv("CURSOR_API_KEY")),
		cursorsdk.WithWorkspace(cwd),
		cursorsdk.WithBridgeBin(os.Getenv("CURSOR_SDK_BRIDGE_BIN")),
	)
	defer client.Close()

	if err := client.Ping(goCtx); err != nil {
		return ReasoningResult{}, fmt.Errorf("cursor bridge ping: %w", err)
	}

	agent, err := client.Agents().Create(goCtx, cursorsdk.CreateOptions{
		Model: model,
		CWD:   cwd,
	})
	if err != nil {
		return ReasoningResult{}, fmt.Errorf("create cursor agent: %w", err)
	}
	defer agent.Close(goCtx)

	prompt := buildReasoningPrompt(ctx, input)
	run, err := agent.Send(goCtx, prompt)
	if err != nil {
		return ReasoningResult{}, fmt.Errorf("cursor send: %w", err)
	}
	result, err := run.Wait(goCtx)
	if err != nil {
		return ReasoningResult{}, fmt.Errorf("cursor wait: %w", err)
	}
	text := strings.TrimSpace(result.Text)
	if text == "" {
		return ReasoningResult{}, fmt.Errorf("cursor run returned empty text (status=%s msg=%s)", result.Status, result.ErrorMessage)
	}

	return ReasoningResult{
		Decision: Decision{
			Reason: text,
			Action: NothingAction{},
			Ctx:    ctx,
		},
	}, nil
}

func buildReasoningPrompt(ctx DecisionContext, input ReasoningInput) string {
	var b strings.Builder
	b.WriteString("You are the Decision Making component of an Autonomy agent.\n")
	b.WriteString("Given the task, propose the next action briefly.\n")
	b.WriteString("Reply in plain text with: (1) reason (2) suggested next step.\n\n")
	if ctx.Task != nil {
		fmt.Fprintf(&b, "Task ID: %s\n", ctx.Task.ID)
		fmt.Fprintf(&b, "Goal: %s\n", ctx.Task.Goal)
		fmt.Fprintf(&b, "Description: %s\n", ctx.Task.Description)
		fmt.Fprintf(&b, "Target: %s\n", ctx.Task.Target)
		fmt.Fprintf(&b, "Expected state: %s\n", ctx.Task.Contract.ExpectedState)
	}
	if strings.TrimSpace(input.Text) != "" {
		fmt.Fprintf(&b, "\nAdditional input:\n%s\n", input.Text)
	}
	return b.String()
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
