package autonomy

import (
	"fmt"
	"os"
	"strings"
	"time"
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

// LLMReasoner uses the official Cursor SDK Bridge to run one local agent turn.
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
	bridge, err := StartCursorBridge(cwd)
	if err != nil {
		return ReasoningResult{}, err
	}
	defer bridge.Close()

	agentID, err := bridge.CreateLocalAgent(r.model, cwd)
	if err != nil {
		return ReasoningResult{}, fmt.Errorf("create cursor agent: %w", err)
	}

	prompt := buildReasoningPrompt(ctx, input)
	text, err := bridge.Prompt(agentID, prompt)
	if err != nil {
		return ReasoningResult{}, fmt.Errorf("cursor prompt: %w", err)
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
