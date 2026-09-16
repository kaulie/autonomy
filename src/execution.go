package autonomy

import (
	"encoding/json"
	"time"
)

// The runtime's own record of what it planned and what it did, next to the LLM
// interaction log (reason_turns / llm_events / llm_messages) rather than inside it:
// those three tables describe a conversation with an LLM, these describe a decision
// and its execution — with an agent as one kind of provider of one execution step.
//
// A plan is written in full before its first step runs and is never rewritten. Its
// outcome is not stored: it is the status of its last execution step, and a planned
// step with no execution row simply never ran. See docs/execution-step.md.

// ExecutionPlan is one decision's plan (AGENT_V2's answer), as one immutable row.
//
// Its identity is ID — not the task's cycle, not the step names: a re-plan is a new
// plan even when it plans exactly what the previous one did, and ReplyMessageID
// ties the row to the exact planner reply it came from (one reply can produce one
// plan). Cycle is an ordering label (this agent's own round) and nothing keys on it.
type ExecutionPlan struct {
	ID           int64
	TaskID       string
	AgentID      int64
	Cycle        int
	DecisionType string // plan | done | blocked | need_input
	Reason       string
	Evidence     string // JSON array, verbatim
	Need         string // JSON object, verbatim (blocked / need_input)
	StepCount    int
	PlanHash     string // fingerprint of the planned steps
	// Where this plan is traceable to, all llm_messages / reason_turns ids:
	ReplyMessageID     int64 // the planner reply the plan is (llm_messages)
	InputMessageID     int64 // the input that reply answered (llm_messages)
	TaskInputMessageID int64 // the task's own first user input (llm_messages)
	ReasonTurnID       int64 // the run that produced the reply (reason_turns)
	CreatedAt          time.Time
}

// ExecutionStepPlan is one step of a plan, as the plan asked for it. It is written
// before execution and never updated: whether the step ran is a question for
// ExecutionStep.
type ExecutionStepPlan struct {
	ID             int64
	PlanID         int64
	Idx            int    // position in the plan, 1-based
	Name           string // what the plan called this step: a later step's binding addresses it by name
	Capability     string
	Input          string // JSON object: the input the plan asked for, verbatim (literals and {"source": …} bindings)
	ExpectedEffect string
	EvidenceRefs   string // JSON array
	CreatedAt      time.Time
}

// ExecutionStep is one step that actually ran, linked to the plan step it carries
// out. The first failing step ends the cycle, so the steps after it have no row.
type ExecutionStep struct {
	ID         int64
	PlanID     int64
	PlanStepID int64
	TaskID     string
	AgentID    int64 // the agent that ran the step (the planner of that cycle)
	Cycle      int
	Idx        int    // execution order within the plan
	Name       string // what the plan called this step (a binding addresses it as step:<name>.output.<key>)
	Capability string
	Provider   string // github / agent-control-plane / cursor / cline / autonomy
	Status     string // ok | failed
	Input      string // JSON object: what the capability was actually called with
	Output     string // JSON object
	Error      string
	StartedAt  time.Time
	EndedAt    time.Time
	DurationMS int64
	CreatedAt  time.Time
}

// ExecutionStep interaction kinds: how a step talked to its provider.
const (
	// InteractionLLM is an agent-backed step: ReasonTurnID points at the worker's
	// own run, and its stream lives in llm_events / llm_messages.
	InteractionLLM = "llm"
	// InteractionHTTP is a capability talking to an API (GitHub, the deployment
	// control plane): the interaction is recorded, its request/response detail is
	// not (yet) — see docs/execution-step.md.
	InteractionHTTP = "http"
	// InteractionLocal is a capability that changed the world in process (the asset
	// store) and had no provider interaction at all.
	InteractionLocal = "local"
)

// ExecutionStepInteraction is one provider interaction of one step. A step may have
// several (an agent-backed capability prompts a worker once; a capability that also
// watches something over HTTP has two), which is why this is its own table and each
// row is one provider.
type ExecutionStepInteraction struct {
	ID           int64
	StepID       int64 // ExecutionStep.id
	Seq          int   // which interaction of that step, 1-based
	Kind         string
	Provider     string
	ReasonTurnID int64 // reason_turns.id for an LLM interaction
	CreatedAt    time.Time
}

// ExecutionPlanOutcome is a plan's result, derived from its steps rather than
// stored: the status of the last step that ran, and how much of the plan that was.
// A plan whose steps never ran has no outcome (found = false), and its planned steps
// are still all there — that is what "planned but never executed" looks like.
type ExecutionPlanOutcome struct {
	PlanID   int64
	StepID   int64  // the last execution step, 0 when nothing ran
	Status   string // that step's status: ok | failed
	Error    string
	Planned  int // planned steps
	Executed int // steps that ran
}

// jsonObject renders a capability input/output map as the JSON text the execution
// tables store, and reads it back.
func jsonObject(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func parseJSONObject(text string) map[string]string {
	out := map[string]string{}
	if text == "" {
		return out
	}
	_ = json.Unmarshal([]byte(text), &out)
	return out
}
