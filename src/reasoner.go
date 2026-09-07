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
	fmt.Printf("Creating cursor client for model: %s\n", model)
	client := cursorsdk.NewClient(
		cursorsdk.WithAPIKey(os.Getenv("CURSOR_API_KEY")),
		cursorsdk.WithWorkspace(cwd),
		cursorsdk.WithBridgeBin(os.Getenv("CURSOR_SDK_BRIDGE_BIN")),
	)
	defer client.Close()

	if err := client.Ping(goCtx); err != nil {
		return ReasoningResult{}, fmt.Errorf("cursor bridge ping: %w", err)
	}
	fmt.Printf("Creating cursor agent for model: %s\n", model)
	agent, err := client.Agents().Create(goCtx, cursorsdk.CreateOptions{
		Model: model,
		CWD:   cwd,
	})
	if err != nil {
		return ReasoningResult{}, fmt.Errorf("create cursor agent: %w", err)
	}
	defer agent.Close(goCtx)

	prompt := buildReasoningPrompt(ctx, input)
	fmt.Printf("Sending prompt to cursor agent: %s\n", prompt)
	run, err := agent.Send(goCtx, prompt)
	if err != nil {
		return ReasoningResult{}, fmt.Errorf("cursor send: %w", err)
	}
	result, err := run.Wait(goCtx)
	fmt.Printf("Cursor agent result: %s\n", result.Text)
	if err != nil {
		return ReasoningResult{}, fmt.Errorf("cursor wait: %w", err)
	}
	text := strings.TrimSpace(result.Text)
	if text == "" {
		return ReasoningResult{}, fmt.Errorf("cursor run returned empty text (status=%s msg=%s)", result.Status, result.ErrorMessage)
	}

	reason, action, err := parseDecision(text)
	if err != nil {
		return ReasoningResult{}, fmt.Errorf("cursor decision: %w", err)
	}
	return ReasoningResult{
		Decision: Decision{
			Reason: reason,
			Action: action,
			Ctx:    ctx,
		},
	}, nil
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
