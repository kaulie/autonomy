package autonomy

import (
	"context"
	"os"
	"strings"
)

// DecisionContext is the input bag for one decision cycle.
type DecisionContext struct {
	context.Context

	Task  *Task
	Agent *Agent
	World World
	Cycle int // 1-based decision cycle when set by Autonomy.Run
	// Input is the message this cycle answers — the inbox message the agent is
	// processing (src/message.go): an instruction from the user, a delegation from
	// another agent, or the runtime's own notice. The prompt renders it as
	// additional_input; it is empty for a cycle nobody addressed to the agent.
	Input string
	// History is what the previous cycles of this task did, oldest first: the
	// planner sees it as previous_actions and can base its evidence on it (see
	// AGENT_V2 evidence source previous_action).
	History []Result
	// Briefing is what this run was handed about its own task before it started: what
	// the task did in earlier runs (its plans, the steps they ran and what those steps
	// produced) and where it stands now — read back from the runtime's own record
	// (src/task_record.go), because the conversation that used to carry it dies with
	// the process. It is nil for a first instruction, and for a worker's turns: the
	// briefing belongs to the task's own agent.
	Briefing *TaskBriefing
	// StepOutputs are the steps of the plan being executed that have already run,
	// oldest first — what this step's input bindings resolve against
	// ("step:<name>.output.<key>", src/plan_lineage.go). It is empty outside an
	// execution: a plan's bindings only ever read the same plan.
	StepOutputs []ActionResult
	// ContextSections is the task's context_ref, resolved before this cycle's prompt
	// was built (src/context_resolver.go, src/context_builder): the ref type -> the
	// fields known about the container it names, its own id and type included. The
	// prompt renders it as context_entity; it is nil when nothing resolved it (a task
	// that names no world, the builder switched off, a context assembled by hand).
	ContextSections map[string]map[string]any
}

type DecisionMaker struct {
	reasoner Reasoner
}

func NewDecideMaker() *DecisionMaker {
	// Default stays local for offline tests. Set AUTONOMY_REASONER=llm to use Cursor SDK Bridge.
	reasoner := Reasoner(NewLocalReasoner("local-reasoner"))
	if os.Getenv("AUTONOMY_REASONER") == "llm" {
		reasoner = NewLLMReasoner()
	}
	return &DecisionMaker{reasoner: reasoner}
}

func (d *DecisionMaker) Decide(ctx DecisionContext) (Decision, error) {
	reasonningResult, err := d.reasoner.Reason(ctx, ReasoningInput{Text: ctx.Input})
	if err != nil {
		return Decision{}, err
	}
	decision := reasonningResult.Decision
	// Where the decision came from is the reasoner's to report: a reasoner that
	// recorded nothing leaves it empty, and the plan is then written without
	// traceability rather than with an invented id.
	if decision.Origin.empty() {
		decision.Origin = reasonningResult.Origin
	}
	return decision, nil
}

// DecisionOrigin is where a decision came from in the LLM record: the run that
// produced it, the reply it consists of, and the input that reply answered. It is
// what makes a plan traceable to one recorded reply instead of to "cycle 3" — a
// cycle number is an ordering label, a message id is that message.
//
// The ids are 0 when the run was not recorded (no store, a local reasoner).
type DecisionOrigin struct {
	ReasonTurnID   int64 // reason_turns.id: the run
	InputMessageID int64 // llm_messages.id: the input the reply answered
	ReplyMessageID int64 // llm_messages.id: the reply itself
}

// empty reports whether nothing about this decision was recorded.
func (o DecisionOrigin) empty() bool {
	return o.ReasonTurnID == 0 && o.InputMessageID == 0 && o.ReplyMessageID == 0
}

// Decision is the outcome of one decision cycle: the AGENT_V2 answer the planner
// returns, as a Go value (see src/agent_policy/AGENT_V2.md §Output Schema — the raw
// JSON it comes from is in reason_turns.raw_output).
//
// A plan carries *every* step it wants executed, in order: the runtime executes
// them all and the agent observes afterwards, so a multi-step plan is not
// reduced to its first action.
type Decision struct {
	// Type is plan | done | blocked | need_input.
	Type   string
	Reason string
	// Origin is which recorded LLM interaction this decision came from, so the plan
	// the runtime writes is traceable to the reply (and the input) it was.
	Origin DecisionOrigin
	// Evidence are the facts the decision rests on; plan steps reference them by
	// ID via the step's evidence_refs.
	Evidence []Evidence
	// Contract is the completion contract the planner declares with its first answer
	// of a run: the facts that must hold for the task to be done, each with the
	// evidence slot it is about (src/completion_contract.go). The runtime pins the
	// first contract it is given and verifies every `done` against it — what executing
	// the decision did never changes the standard it is judged by.
	Contract []Criterion
	// Actions are the plan's steps in execution order (empty for
	// done/blocked/need_input).
	Actions []Action
	// Need is what is missing when the decision is blocked or needs input.
	Need Need
	// Deliverables is what the task hands over and Presentation is how the result
	// should be expressed to its consumer (the Owner's final decision, see
	// docs/completion-contract.md).
	Deliverables []Deliverable
	Presentation []Presentation
	Ctx          DecisionContext
}

// Evidence is one fact supporting a decision (AGENT_V2 "evidence").
type Evidence struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Reference string `json:"reference"`
	Fact      string `json:"fact"`
}

// Need says what is missing when the decision is blocked or needs input.
//
// Options is the choice the owner is handed, when the answer really is one of a few
// concrete ones: each is a self-contained answer they can accept as written, and the UI
// lists them 1..N so they can pick instead of typing. It is optional on purpose — an open
// question ("which environment?") has no list and the owner answers in their own words,
// and padding it with invented choices is worse than leaving it empty (AGENT_V2.md §Need).
type Need struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Options     []string `json:"options,omitempty"`
}

// Deliverable is the concrete result a task hands over: a system-defined Asset
// or a task-produced Artifact.
type Deliverable struct {
	// Type is asset | artifact.
	Type string `json:"type"`
	// ConcreteType names the recognised asset/artifact (Repository, Service, File, …).
	ConcreteType string         `json:"concrete_type"`
	Detail       map[string]any `json:"detail"`
}

// Presentation is how the task result should be expressed to its consumer.
type Presentation struct {
	// Type is summary | report | status | decision | finding | artifact | question.
	Type    string `json:"type"`
	Content string `json:"content"`
}

// ActionResult is one action's record in a cycle: what ran, the input it was
// actually called with, what it produced, and how it ended. Input and output are
// kept verbatim per action — never merged or summarised — so the consumer (the
// next decision, a UI) decides which entry matters.
type ActionResult struct {
	// Capability is the capability the step named ("nothing" for a skipped step).
	Capability string
	// StepName is what the plan called this step, so a later step's input binding
	// can address it ("step:<name>.output.<key>"). Empty when the plan named none.
	StepName string
	// Input is the input the capability was called with, verbatim: literals as the
	// plan wrote them and bindings already resolved to their values.
	Input map[string]string
	// Output is what the capability returned, verbatim; a capability that failed
	// keeps whatever it produced before failing.
	Output map[string]string
	// Error is empty when the action succeeded (filled in by Runtime.Execute).
	Error string
	// ExpectedEffect and EvidenceRefs are the plan step's own metadata
	// (AGENT_V2: what the step was meant to change, and on which evidence).
	ExpectedEffect string
	EvidenceRefs   []string
	// PlanStepID and ExecutionStepID are the rows this action *is* in the execution
	// tables: which planned step it carries out, and the step that records it. They
	// are 0 when nothing was recorded (no store), and they are ids — never the plan's
	// position — so a re-plan that plans the same thing cannot be confused with this
	// one.
	PlanStepID      int64
	ExecutionStepID int64
}

// Concludes reports whether this decision ends the run instead of planning more work.
//
// AGENT_V2: a plan plans; the other three decide the task. `done` is the completion
// contract satisfied — and the runtime requires it to carry its evidence — while
// `blocked` and `need_input` are the task saying it cannot go on without something.
//
// The loop stops at one of those: asking the planner again spends a whole cycle to be
// told the same thing, which is what "a cycle concludes with observation and
// verification, not with the step count running out" means (docs/execution-loop.md).
//
// This is a property of the type alone: what executing the decision did — including
// the runtime refusing it because it broke its type's requirements
// (src/decision_rules.go) — never changes it. So the loop reads this *before* the
// cycle runs and breaks only if the answer also held up: a refused done / blocked /
// need_input is a failed cycle, and the planner gets to re-plan from the reason.
func (d Decision) Concludes() bool {
	switch strings.ToLower(strings.TrimSpace(d.Type)) {
	case decisionDone, decisionBlocked, decisionNeedInput:
		return true
	}
	return false
}

// TaskStatusFor is the outcome a concluding decision writes on the task row: the
// decision type itself — `done` under the vocabulary's own word for it, `completed` —
// so a task ended by `blocked` / `need_input` says so instead of looking finished. Why
// it is blocked is the decision's own `need` (execution_plan.need) and its reply;
// tasks.error stays what docs/store.md says it is: why a run *failed*.
func TaskStatusFor(decision Decision) string {
	switch strings.ToLower(strings.TrimSpace(decision.Type)) {
	case decisionBlocked:
		return TaskStatusBlocked
	case decisionNeedInput:
		return TaskStatusNeedInput
	}
	return TaskStatusCompleted
}

// Result is what happened after executing a decision.
type Result struct {
	Message string
	// Actions holds one record per executed action, in plan order, with each
	// action's raw input and output. Nothing is merged away: a consumer that only
	// wants "the" output picks the entry it cares about.
	Actions []ActionResult
	Err     error
	// WorldState optionally patches asset state (applied by UpdateWorld).
	WorldState map[string]string
}
