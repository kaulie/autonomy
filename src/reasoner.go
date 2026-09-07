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

	reason, action := parseDecision(text)
	return ReasoningResult{
		Decision: Decision{
			Reason: reason,
			Action: action,
			Ctx:    ctx,
		},
	}, nil
}

func buildReasoningPrompt(ctx DecisionContext, input ReasoningInput) string {
	var b strings.Builder
	b.WriteString("You are the Decision Making component of an Autonomy agent.\n")
	b.WriteString("Choose exactly one next action for the task target asset.\n")
	b.WriteString("Available actions:\n")
	b.WriteString("- change: mutate the target asset state toward the contract expected state\n")
	b.WriteString("- noop: do nothing this cycle\n")
	b.WriteString("Reply in exactly this format (no markdown):\n")
	b.WriteString("ACTION: <change|noop>\n")
	b.WriteString("REASON: <one short sentence>\n\n")
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

// parseDecision maps model text into a Decision reason + Action.
// Unknown or missing ACTION defaults to change so a coherent reply still advances the demo loop.
func parseDecision(text string) (string, Action) {
	actionName := "change"
	reason := text
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "ACTION:"):
			actionName = strings.ToLower(strings.TrimSpace(line[len("ACTION:"):]))
		case strings.HasPrefix(upper, "REASON:"):
			reason = strings.TrimSpace(line[len("REASON:"):])
		}
	}
	switch actionName {
	case "noop", "nothing", "none":
		return reason, NothingAction{}
	default:
		return reason, SimpleAction{}
	}
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
