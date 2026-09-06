package autonomy

import (
	"time"
)

type ReasoningInput struct {
	input string
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

type DefaultReasoner struct {
	model string
}

func NewLLMReasoner(s string) Reasoner {
	return &LLMReasoner{model: s}
}

type LLMReasoner struct {
	model string
}

func (r *LLMReasoner) Reason(ctx DecisionContext, input ReasoningInput) (ReasoningResult, error) {
	return ReasoningResult{
		Decision: Decision{
			Reason: "llm reason",
			Action: NothingAction{},
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
