package autonomy

// A run is not the whole story of a task. The task's own record — the plans it made,
// the steps that ran and what those steps produced — is written to the store as it
// happens (execution_plan / execution_step_plan / execution_step, src/execution.go,
// docs/execution-step.md) and outlives everything the conversation lived in: the
// provider session dies with the process (a Cline bridge takes its sessions with it),
// a restart cuts the run that was in flight, and the next instruction for the task is
// a *continuation* of it.
//
// This file is that record read back into the prompt: `runtime_context.previous_actions`
// is this run's own cycles, and `runtime_context.briefing` is what the task did before
// this run plus where it stands right now (which criteria are still open).
//
// It is read at run start, once (src/autonomy.go:runLoop): a round this run runs is
// appended to the run's own history and never doubled from the record. Every read here
// is best-effort — a task with no record (a first instruction) gets no briefing, and a
// store that cannot be read gets a line on stderr, never a failed run.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// taskRecordRounds caps how many of a task's rounds the briefing carries: the
	// newest ones are what a continuation plans from, and a task that ran for a
	// hundred cycles must not flood every later cycle's prompt.
	taskRecordRounds = 8
	// taskRecordReasonChars caps one round's reason: the planner's own words for why
	// it did what it did are the point, but not at full essay length every cycle.
	taskRecordReasonChars = 1200
	// taskRecordVerdicts caps how many verdicts travel with the state.
	taskRecordVerdicts = 12
)

// taskBriefingNote is what the block is, in words. The planner has to know that these
// rounds are its own record from *before* this run — not this run's work — or a
// continuation reads them as a plan it never made and starts the task over.
const taskBriefingNote = "This is your own record on this task from before this run: the runtime kept " +
	"your plans, the steps they ran and what those steps produced, so a restart does not lose them. " +
	"It is the same conversation, not this cycle's work — continue from what these rounds already " +
	"delivered instead of doing it again, and check state.open_criteria for what is still missing."

// interruptedNote is added when the newest round was not finished but *cut*: the runtime
// itself took the run out (a restart), and this run is the continuation of it. The work
// the round already produced is done — the steps that never ran are the ones to do.
const interruptedNote = " Your newest round was cut by a restart of the runtime instead of ending by " +
	"itself: state.interrupted says where it stopped (and the restart it belonged to), and the steps of " +
	"that round which never ran are the ones to continue with. What it already produced is done, not " +
	"to be redone."

// TaskInterruption says the task's newest round was cut by the runtime itself. It comes
// from the task row's stop reason (src/stop_reason.go) — the runtime's own record of who
// stopped it — so a run that resumes the task knows it is continuing a cut round, and
// where the cut landed, instead of reading a half-finished plan as the whole story.
type TaskInterruption struct {
	// Reason is the runtime's own words for it (tasks.error), naming the restart
	// (requestId=…) when the platform announced one.
	Reason string `json:"reason,omitempty"`
	// StoppedAtStep is the step of the newest round that had not run yet when the run
	// was cut, and NextStep is that step's name: where a continuation picks up.
	StoppedAtStep int    `json:"stopped_at_step,omitempty"`
	NextStep      string `json:"next_step,omitempty"`
}

// TaskBriefing is the handover a run is given about its own task.
type TaskBriefing struct {
	// Note is the block's own words (taskBriefingNote), carried with the data so a
	// consumer that only reads the prompt still knows what it is looking at.
	Note string `json:"note"`
	// State is where the task stands now.
	State *TaskState `json:"state,omitempty"`
	// EarlierRounds is what the task already did, oldest first (execution_plan rows,
	// the newest taskRecordRounds of them).
	EarlierRounds []TaskRound `json:"earlier_rounds,omitempty"`
}

// TaskState is where the task stands, as the record tells it. The task *row's* status
// is deliberately not repeated here: it is in the prompt's `task` block, and it says
// `pending` from the moment a new instruction is accepted — which is not where the
// task stands. What it stands on is its last round and its open criteria.
type TaskState struct {
	LastRound *TaskRound `json:"last_round,omitempty"`
	// Verdicts are the verdicts against the pinned completion contract, oldest first.
	Verdicts []TaskVerdict `json:"verdicts,omitempty"`
	// OpenCriteria are the contract's criteria no verdict has passed yet: what the
	// task is still missing.
	OpenCriteria []string `json:"open_criteria,omitempty"`
	// Interrupted is set when the newest round was cut by the runtime (a restart) rather
	// than ending on its own — the case this run was resumed for.
	Interrupted *TaskInterruption `json:"interrupted,omitempty"`
}

// TaskRound is one round of a task as the runtime recorded it: the decision that was
// made (its type and the planner's own reason for it) and the steps of its plan.
type TaskRound struct {
	PlanID   int64  `json:"plan_id"`
	Cycle    int    `json:"cycle"`
	Decision string `json:"decision,omitempty"` // plan | done | blocked | need_input
	Reason   string `json:"reason,omitempty"`
	// Status is the plan's outcome: ok | failed | planned (a plan whose steps never
	// ran — planned, not executed).
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// Need is the decision's `need`, verbatim, for blocked / need_input.
	Need json.RawMessage `json:"need,omitempty"`
	At   time.Time       `json:"at,omitempty"`
	// Steps are the planned steps, each with what it ran as and produced; a step that
	// was planned and never ran has status "pending" and no output.
	Steps []TaskRoundStep `json:"steps,omitempty"`
}

// TaskRoundStep is one step of one round: what it ran as and produced, verbatim JSON
// (a step that was planned and never ran carries the plan's own input, bindings and
// all — resolving them is not this reader's business).
type TaskRoundStep struct {
	Idx        int             `json:"idx"`
	Name       string          `json:"name,omitempty"`
	Capability string          `json:"capability"`
	Status     string          `json:"status"` // ok | failed | pending
	Input      json.RawMessage `json:"input,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
	// ExpectedEffect is what the step was meant to change (the plan's own words).
	ExpectedEffect string `json:"expected_effect,omitempty"`
	DurationMS     int64  `json:"duration_ms,omitempty"`
}

// TaskVerdict is one verdict of one criterion of the pinned contract
// (src/verification.go, the `verification` table).
type TaskVerdict struct {
	Criterion string `json:"criterion"`
	Result    string `json:"result"` // pass | fail | inconclusive
	Method    string `json:"method,omitempty"`
	Expected  string `json:"expected,omitempty"`
	Observed  string `json:"observed,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Cycle     int    `json:"cycle,omitempty"`
}

// taskBriefing reads the task's record for the run that is starting. nil means "there
// is nothing to hand over": a task with no plans, no execution store, or a store that
// could not be read.
func (r *Autonomy) taskBriefing(taskID string) *TaskBriefing {
	if r == nil || strings.TrimSpace(taskID) == "" {
		return nil
	}
	store := r.executionStore()
	if store == nil {
		return nil
	}
	plans, err := store.ListExecutionPlans(taskID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] %s briefing: %v\n", taskID, err)
		return nil
	}
	if len(plans) == 0 {
		return nil
	}
	if len(plans) > taskRecordRounds {
		plans = plans[len(plans)-taskRecordRounds:]
	}
	rounds := make([]TaskRound, 0, len(plans))
	for _, plan := range plans {
		rounds = append(rounds, r.taskRound(store, plan))
	}
	last := rounds[len(rounds)-1]
	state := &TaskState{
		LastRound:    &last,
		Verdicts:     r.taskVerdicts(taskID),
		OpenCriteria: r.openCriteria(taskID),
	}
	note := taskBriefingNote
	if cut := r.taskInterruption(taskID, &last); cut != nil {
		state.Interrupted = cut
		note += interruptedNote
	}
	return &TaskBriefing{
		Note:          note,
		State:         state,
		EarlierRounds: rounds,
	}
}

// taskInterruption says whether the newest round was cut by the runtime, and where. It
// reads it from the task row's stop reason — what the runtime wrote when *it* was the one
// that stopped the run (src/stop_reason.go) — so the answer is the runtime's own record
// and not an inference from a half-finished plan. A task a person stopped, or one that
// ended on its own, has no interruption.
func (r *Autonomy) taskInterruption(taskID string, last *TaskRound) *TaskInterruption {
	store := r.taskStore()
	if store == nil {
		return nil
	}
	// The stop reason is written by the writer; read it there so a replica that has not
	// caught up cannot turn "cut by a restart" back into "ended on its own".
	task, err := writerReads(store).GetTask(taskID)
	if err != nil || !isRuntimeStopRecord(task) {
		return nil
	}
	reason := strings.TrimSpace(task.Error)
	cut := &TaskInterruption{Reason: reason}
	if last != nil {
		for _, step := range last.Steps {
			if strings.EqualFold(strings.TrimSpace(step.Status), "pending") {
				cut.StoppedAtStep = step.Idx
				cut.NextStep = step.Name
				break
			}
		}
	}
	return cut
}

// taskRound rebuilds one round: its plan row, its outcome (derived from the steps that
// ran) and the planned steps merged with what actually ran.
func (r *Autonomy) taskRound(store ExecutionStore, plan ExecutionPlan) TaskRound {
	round := TaskRound{
		PlanID:   plan.ID,
		Cycle:    plan.Cycle,
		Decision: plan.DecisionType,
		Reason:   clipText(plan.Reason, taskRecordReasonChars),
		Status:   "planned",
		At:       plan.CreatedAt,
	}
	if need := strings.TrimSpace(plan.Need); need != "" && need != "{}" && need != "null" {
		round.Need = json.RawMessage(need)
	}
	if outcome, ok, err := store.ExecutionPlanOutcome(plan.ID); err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] plan %d outcome: %v\n", plan.ID, err)
	} else if ok {
		round.Status = outcome.Status
		round.Error = outcome.Error
	}
	planned, err := store.ListExecutionStepPlan(plan.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] plan %d steps: %v\n", plan.ID, err)
	}
	executed, err := store.ListExecutionSteps(plan.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] plan %d execution: %v\n", plan.ID, err)
	}
	for _, step := range planStepProgress(planned, executed) {
		input := step.Input
		if len(input) == 0 {
			// A step that never ran: what the plan asked for is still worth showing.
			input = step.PlannedInput
		}
		round.Steps = append(round.Steps, TaskRoundStep{
			Idx:            step.Idx,
			Name:           step.Name,
			Capability:     step.Capability,
			Status:         step.Status,
			Input:          input,
			Output:         step.Output,
			Error:          step.Error,
			ExpectedEffect: step.ExpectedEffect,
			DurationMS:     step.DurationMS,
		})
	}
	return round
}

// taskVerdicts reads a task's verdicts (oldest first, newest taskRecordVerdicts kept).
func (r *Autonomy) taskVerdicts(taskID string) []TaskVerdict {
	store := r.verificationStore()
	if store == nil {
		return nil
	}
	verdicts, err := store.ListVerifications(taskID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] %s verdicts: %v\n", taskID, err)
		return nil
	}
	if len(verdicts) > taskRecordVerdicts {
		verdicts = verdicts[len(verdicts)-taskRecordVerdicts:]
	}
	out := make([]TaskVerdict, 0, len(verdicts))
	for _, v := range verdicts {
		out = append(out, TaskVerdict{
			Criterion: v.Criterion,
			Result:    v.Result,
			Method:    v.Method,
			Expected:  v.Expected,
			Observed:  v.Observed,
			Reason:    v.Reason,
			Cycle:     v.Cycle,
		})
	}
	return out
}

// openCriteria is the pinned contract's criteria that no verdict has passed yet — the
// question "what is still missing?" answered from the record instead of guessed.
func (r *Autonomy) openCriteria(taskID string) []string {
	contract := pinnedCompletionContract(taskID)
	if len(contract) == 0 {
		return nil
	}
	passed := map[string]bool{}
	for _, v := range r.taskVerdicts(taskID) {
		if v.Result == verificationPass {
			passed[v.Criterion] = true
		}
	}
	open := make([]string, 0, len(contract))
	for i, criterion := range contract {
		name := firstNonEmptyString(criterion.Name, fmt.Sprintf("C%d", i+1))
		if !passed[name] {
			open = append(open, name)
		}
	}
	return open
}

// clipText caps one text field of the briefing, at a rune boundary so a truncated
// reason is still valid UTF-8.
func clipText(s string, limit int) string {
	text := strings.TrimSpace(s)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	cut := text[:limit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimSpace(cut) + "…"
}
