package autonomy

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// briefingStore opens a store (and points the process singleton at it, because the
// pinned-contract read goes through the active store) plus a task and its agent, which
// is what a task's record hangs off.
func briefingStore(t *testing.T) (rawStore, *Task, *Agent) {
	t.Helper()
	store, err := openStore(t, filepath.Join(t.TempDir(), "briefing.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	prev := _store
	t.Cleanup(func() { _store = prev })
	_store = store

	task := &Task{ID: "task-brief", Description: "ship the dashboard", Status: TaskStatusRunning, AgentID: 10001}
	if err := store.UpsertTask(task); err != nil {
		t.Fatal(err)
	}
	agent := &Agent{ID: 10001, Name: "agent-10001", State: "idle", CurrentTask: task, Lifecycle: AgentLifecyclePersistent}
	if err := store.UpsertAgent(agent); err != nil {
		t.Fatal(err)
	}
	return store, task, agent
}

// recordDashboardRound writes the round the deployed runtime would have written for the
// monitor-dashboard task: implement (code_edit) then deploy (service.deploy), the deploy
// failing — the record a restart has to hand to the next run.
func recordDashboardRound(t *testing.T, store rawStore) (int64, []ExecutionStepPlan) {
	t.Helper()
	planID, planned, err := saveExecutionPlan(ExecutionPlan{
		TaskID: "task-brief", AgentID: 10001, Cycle: 1, DecisionType: "plan",
		Reason:    "deliver the dashboard as a pull request, then deploy it",
		StepCount: 2, CreatedAt: time.Now(),
	}, []ExecutionStepPlan{
		{Idx: 1, Name: "implement", Capability: "code_edit", Input: `{"instruction":"agent status dashboard"}`,
			ExpectedEffect: "a pull request exists"},
		{Idx: 2, Name: "deploy", Capability: "service.deploy",
			Input:          `{"branch":{"source":"step:implement.output.head_branch"}}`,
			ExpectedEffect: "the dashboard is deployed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendExecutionStep(ExecutionStep{
		PlanID: planID, PlanStepID: planned[0].ID, TaskID: "task-brief", AgentID: 10001, Cycle: 1,
		Idx: 1, Name: "implement", Capability: "code_edit", Status: "ok",
		Input:  `{"instruction":"agent status dashboard"}`,
		Output: `{"pr_url":"https://github.com/kaulie/autonomy/pull/144","summary":"dashboard delivered"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendExecutionStep(ExecutionStep{
		PlanID: planID, PlanStepID: planned[1].ID, TaskID: "task-brief", AgentID: 10001, Cycle: 1,
		Idx: 2, Name: "deploy", Capability: "service.deploy", Status: "failed",
		Input: `{"branch":"feature/agent-monitor-dashboard"}`, Output: `{"state":"queued"}`,
		Error: "control plane refused the pipeline",
	}); err != nil {
		t.Fatal(err)
	}
	return planID, planned
}

// The record a run starts after a restart has to carry what the task already did: the
// plan's own reason, each step's input and output (the pr_url is the point), and how the
// round ended.
func TestTaskBriefingRebuildsWhatTheTaskAlreadyDid(t *testing.T) {
	store, _, _ := briefingStore(t)
	planID, _ := recordDashboardRound(t, store)

	rt := &Autonomy{Store: store}
	brief := rt.taskBriefing("task-brief")
	if brief == nil {
		t.Fatal("no briefing for a task with a record")
	}
	if strings.TrimSpace(brief.Note) == "" {
		t.Fatal("the briefing travels without its own words")
	}
	if len(brief.EarlierRounds) != 1 {
		t.Fatalf("rounds=%+v, want the one recorded round", brief.EarlierRounds)
	}
	round := brief.EarlierRounds[0]
	if round.PlanID != planID || round.Cycle != 1 || round.Decision != "plan" {
		t.Fatalf("round=%+v", round)
	}
	if !strings.Contains(round.Reason, "deliver the dashboard") {
		t.Fatalf("round.Reason=%q, want the planner's own words", round.Reason)
	}
	if round.Status != "failed" || !strings.Contains(round.Error, "refused") {
		t.Fatalf("round=%+v, want the last step's failure", round)
	}
	if len(round.Steps) != 2 {
		t.Fatalf("steps=%+v", round.Steps)
	}
	if !strings.Contains(string(round.Steps[0].Output), "pull/144") {
		t.Fatalf("step 1 output=%s, want the pr_url it produced", round.Steps[0].Output)
	}
	if round.Steps[1].Error != "control plane refused the pipeline" {
		t.Fatalf("step 2=%+v, want the failure it carries", round.Steps[1])
	}
	if brief.State == nil || brief.State.LastRound == nil || brief.State.LastRound.PlanID != planID {
		t.Fatalf("state=%+v, want the last round as the state's own", brief.State)
	}
}

// A step that was planned and never ran is part of the record too — that is what
// "interrupted in the middle of this round" looks like — and its input travels verbatim,
// bindings and all (resolving them is not this reader's business).
func TestTaskBriefingKeepsAPlannedStepThatNeverRan(t *testing.T) {
	store, _, _ := briefingStore(t)
	planID, planned, err := saveExecutionPlan(ExecutionPlan{
		TaskID: "task-brief", AgentID: 10001, Cycle: 1, DecisionType: "plan",
		Reason: "deploy what the last run implemented", StepCount: 2, CreatedAt: time.Now(),
	}, []ExecutionStepPlan{
		{Idx: 1, Name: "review", Capability: "pull_request.review", Input: `{"pr":"https://example/1"}`},
		{Idx: 2, Name: "deploy", Capability: "service.deploy",
			Input:          `{"branch":{"source":"step:review.output.head_branch"}}`,
			ExpectedEffect: "the service is deployed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendExecutionStep(ExecutionStep{
		PlanID: planID, PlanStepID: planned[0].ID, TaskID: "task-brief", AgentID: 10001, Cycle: 1,
		Idx: 1, Name: "review", Capability: "pull_request.review", Status: "ok",
		Input: `{"pr":"https://example/1"}`, Output: `{"head_branch":"feature/x"}`,
	}); err != nil {
		t.Fatal(err)
	}

	round := (&Autonomy{Store: store}).taskBriefing("task-brief").EarlierRounds[0]
	if round.Status != "ok" {
		t.Fatalf("round=%+v, want the one step that ran", round)
	}
	if len(round.Steps) != 2 {
		t.Fatalf("steps=%+v, want both planned steps", round.Steps)
	}
	second := round.Steps[1]
	if second.Status != "pending" || len(second.Output) != 0 {
		t.Fatalf("step 2=%+v, want planned and never run", second)
	}
	if !strings.Contains(string(second.Input), "step:review.output.head_branch") {
		t.Fatalf("step 2 input=%s, want the plan's own binding", second.Input)
	}
}

// "Where does this task stand?" is the pinned contract minus what has passed: the run
// that continues the task has to be able to answer "what is still missing" without
// re-reading the whole conversation.
func TestTaskBriefingReportsTheCriteriaStillOpen(t *testing.T) {
	store, _, _ := briefingStore(t)
	planID, _ := recordDashboardRound(t, store)
	for _, criterion := range []ContractCriterion{
		{TaskID: "task-brief", Idx: 1, PlanID: planID, Name: "implementation_delivered", Criterion: `"the dashboard is delivered"`},
		{TaskID: "task-brief", Idx: 2, PlanID: planID, Name: "dashboard_running", Criterion: `"the dashboard is running"`},
	} {
		if err := store.AppendCompletionContract(criterion); err != nil {
			t.Fatal(err)
		}
	}
	for _, verdict := range []Verification{
		{TaskID: "task-brief", PlanID: planID, Cycle: 1, Criterion: "implementation_delivered", Result: "pass"},
		{TaskID: "task-brief", PlanID: planID, Cycle: 1, Criterion: "dashboard_running", Result: "inconclusive"},
	} {
		if _, err := store.AppendVerification(verdict); err != nil {
			t.Fatal(err)
		}
	}

	state := (&Autonomy{Store: store}).taskBriefing("task-brief").State
	if len(state.Verdicts) != 2 {
		t.Fatalf("verdicts=%+v", state.Verdicts)
	}
	if len(state.OpenCriteria) != 1 || state.OpenCriteria[0] != "dashboard_running" {
		t.Fatalf("open_criteria=%v, want the criterion no verdict passed", state.OpenCriteria)
	}
}

// The briefing is rendered where a run reads it, and not into a delegated worker's
// prompt: the worker is handed one job by the delegating cycle, and every delegation
// would pay for the task's whole record.
func TestBriefingReachesThePromptAndNotAWorkers(t *testing.T) {
	brief := &TaskBriefing{
		Note:  taskBriefingNote,
		State: &TaskState{OpenCriteria: []string{"dashboard_running"}},
		EarlierRounds: []TaskRound{{
			PlanID: 7, Cycle: 1, Decision: "plan", Reason: "deliver it", Status: "ok",
			Steps: []TaskRoundStep{{Idx: 1, Name: "implement", Capability: "code_edit", Status: "ok",
				Output: json.RawMessage(`{"pr_url":"https://github.com/kaulie/autonomy/pull/144"}`)}},
		}},
	}
	task := &Task{ID: "task-brief", Description: "ship the dashboard"}
	ctx := DecisionContext{Task: task, Agent: &Agent{Name: "agent-10001"}, Cycle: 2, Briefing: brief}

	raw := string(formatRuntimeContextJSON(ctx, ReasoningInput{}))
	for _, want := range []string{`"briefing"`, `"open_criteria"`, "dashboard_running", "pull/144", "before this run"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("runtime_context=%s, want %s", raw, want)
		}
	}

	worker := string(workerRuntimeContextJSON(ctx))
	if strings.Contains(worker, "briefing") {
		t.Fatalf("a worker's runtime_context carries the briefing: %s", worker)
	}

	delta, err := buildReasoningDelta(ctx, ReasoningInput{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(delta, "before this run") || !strings.Contains(delta, `"briefing"`) {
		t.Fatalf("delta=%s, want the continuation sentence and the briefing", delta)
	}
	// And a first instruction pays for none of it.
	plain, err := buildReasoningDelta(DecisionContext{Task: task, Cycle: 1}, ReasoningInput{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "briefing") || strings.Contains(plain, "before this run") {
		t.Fatalf("delta without a record carries a briefing: %s", plain)
	}
}

// recordingReasoner answers a concluding decision and remembers the context it was
// asked about: how a test sees what a run actually handed its cycles.
type recordingReasoner struct {
	seen []DecisionContext
}

func (r *recordingReasoner) Reason(ctx DecisionContext, _ ReasoningInput) (ReasoningResult, error) {
	r.seen = append(r.seen, ctx)
	// Ctx travels on the decision: Execute reads the task it belongs to from there.
	return ReasoningResult{Decision: Decision{
		Ctx:    ctx,
		Type:   "blocked",
		Reason: "waiting on the record",
		Need:   Need{Type: "input", Description: "the test's own answer"},
	}}, nil
}

// The point of the whole file: a run that starts after a restart is a continuation. Its
// cycles see what the task already did — handed in by the run, not remembered by a
// session the restart took away.
func TestARunAfterARestartContinuesTheTask(t *testing.T) {
	store, task, agent := briefingStore(t)
	recordDashboardRound(t, store)

	reasoner := &recordingReasoner{}
	agent.DecideMaker = &DecisionMaker{reasoner: reasoner}
	rt := &Autonomy{
		Store:        store,
		AgentFactory: NewAgentFactory(),
		Runtime:      NewRuntime(NewAgentFactory()),
		MaxSteps:     1,
	}
	if err := rt.runLoop(context.Background(), agent, task, "继续吧"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(reasoner.seen) == 0 {
		t.Fatal("the run never decided")
	}
	brief := reasoner.seen[0].Briefing
	if brief == nil {
		t.Fatal("the run's cycles were handed no briefing")
	}
	if len(brief.EarlierRounds) != 1 || brief.EarlierRounds[0].Cycle != 1 {
		t.Fatalf("briefing=%+v, want the round the previous process ran", brief)
	}
	if !strings.Contains(string(brief.EarlierRounds[0].Steps[0].Output), "pull/144") {
		t.Fatalf("briefing=%+v, want the pr_url the earlier round produced", brief)
	}
	// This run's own cycles stay in previous_actions: the record is what came before it.
	if len(reasoner.seen[0].History) != 0 {
		t.Fatalf("history=%+v, want this run's own cycles only", reasoner.seen[0].History)
	}
}

// A task with no record gets no briefing: a first instruction is not a continuation.
func TestATaskWithNoRecordGetsNoBriefing(t *testing.T) {
	store, _, _ := briefingStore(t)
	if brief := (&Autonomy{Store: store}).taskBriefing("task-brief"); brief != nil {
		t.Fatalf("briefing=%+v, want none for a task that never ran", brief)
	}
	if brief := (&Autonomy{Store: store}).taskBriefing(""); brief != nil {
		t.Fatalf("briefing=%+v, want none without a task id", brief)
	}
}
