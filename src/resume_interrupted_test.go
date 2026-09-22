package autonomy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// waitFor polls until cond holds, or fails the test.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// reasonTurns is how many runs a task has recorded.
func reasonTurns(t *testing.T, store rawStore, taskID string) int {
	t.Helper()
	var turns int
	if err := store.RawDB().QueryRow(`SELECT count(*) FROM reason_turns WHERE task_id = ?`, taskID).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	return turns
}

// interruptedStore leaves what a restart leaves behind: a task whose run was claimed by a
// process that is gone (the message is still `running`), and a task row that says who
// stopped it.
func interruptedStore(t *testing.T, taskID string, reason StopReason) (rawStore, *Task, *Agent) {
	t.Helper()
	store := resumeTestStore(t)
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	t.Setenv("AUTONOMY_REASONER", "local")
	t.Setenv("AUTONOMY_MAX_STEPS", "1")

	task, agent := pairedTask(t, store, taskID)
	if _, err := store.EnqueueMessage(AgentMessage{
		AgentID: agent.ID, TaskID: task.ID, Sender: MessageSenderUser, SenderID: "user_001",
		Kind: MessageKindInstruction, Content: "ship it",
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.ClaimNextMessage(agent.ID); err != nil || !found {
		t.Fatalf("claim: found=%v err=%v", found, err)
	}
	task.Status = TaskStatusStopped
	task.Error = reason.errorText()
	if err := store.UpsertTask(task); err != nil {
		t.Fatal(err)
	}
	return store, task, agent
}

// TestBootResumesTheRunTheLastProcessDiedIn: the runtime heals itself on boot. A run the
// previous process was in the middle of is started again — no new instruction, and the
// queue hands that work back — because each agent looks at its own state (its message is
// still `running`) instead of waiting to be told.
func TestBootResumesTheRunTheLastProcessDiedIn(t *testing.T) {
	store, task, agent := interruptedStore(t, "t-cut-run", StopReason{
		By: stoppedByRuntime, RequestID: "pipeline-boot", Deployment: "deployment-x", Version: "abc1234",
	})
	before := reasonTurns(t, store, task.ID)

	f := NewAgentFactory()
	auto := &Autonomy{AgentFactory: f, Runtime: NewRuntime(f), Store: store}
	auto.resumeInterruptedRuns()

	waitFor(t, "the resumed run to be recorded", func() bool {
		return reasonTurns(t, store, task.ID) > before
	})
	waitFor(t, "the run to leave in-flight", func() bool {
		_, running := inFlightTasks.Load(task.ID)
		return !running
	})
	// The message the dead process left running is not running any more: the resumed run
	// put it down.
	messages, err := store.ListAgentMessages(agent.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range messages {
		if msg.Status == MessageStatusRunning {
			t.Fatalf("message %d is still running after the boot: %+v", msg.ID, msg)
		}
	}
}

// TestBootLeavesAPersonsStopAlone: a task a person stopped is a decision, not an
// interruption — the boot does not re-enter it.
func TestBootLeavesAPersonsStopAlone(t *testing.T) {
	store, task, agent := interruptedStore(t, "t-user-stopped", StopReason{By: stoppedByUser})
	before := reasonTurns(t, store, task.ID)

	f := NewAgentFactory()
	auto := &Autonomy{AgentFactory: f, Runtime: NewRuntime(f), Store: store}
	auto.resumeInterruptedRuns()
	time.Sleep(300 * time.Millisecond)

	if got := reasonTurns(t, store, task.ID); got != before {
		t.Fatalf("reason turns=%d, want %d: a person's stop was re-entered", got, before)
	}
	messages, err := store.ListAgentMessages(agent.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	running := 0
	for _, msg := range messages {
		if msg.Status == MessageStatusRunning {
			running++
		}
	}
	if running != 1 {
		t.Fatalf("running messages=%d, want the one nobody touched", running)
	}
}

// TestBootSelfHealCanBeTurnedOff: AUTONOMY_RESUME_INTERRUPTED=0 leaves the cut run where
// it is — the switch an operator reaches for.
func TestBootSelfHealCanBeTurnedOff(t *testing.T) {
	store, task, _ := interruptedStore(t, "t-cut-off", StopReason{By: stoppedByRuntime, RequestID: "pipeline-off"})
	t.Setenv("AUTONOMY_RESUME_INTERRUPTED", "0")
	before := reasonTurns(t, store, task.ID)

	f := NewAgentFactory()
	auto := &Autonomy{AgentFactory: f, Runtime: NewRuntime(f), Store: store}
	auto.resumeInterruptedRuns()
	time.Sleep(300 * time.Millisecond)

	if got := reasonTurns(t, store, task.ID); got != before {
		t.Fatalf("reason turns=%d, want %d with the self-heal off", got, before)
	}
}

// TestBootSkipsAnInterruptionOlderThanTheBound: a cut from days ago is not the runtime's
// to re-enter on boot.
func TestBootSkipsAnInterruptionOlderThanTheBound(t *testing.T) {
	store, task, _ := interruptedStore(t, "t-cut-old", StopReason{By: stoppedByRuntime, RequestID: "pipeline-old"})
	t.Setenv("AUTONOMY_RESUME_MAX_AGE", "1ns")
	before := reasonTurns(t, store, task.ID)

	f := NewAgentFactory()
	auto := &Autonomy{AgentFactory: f, Runtime: NewRuntime(f), Store: store}
	auto.resumeInterruptedRuns()
	time.Sleep(300 * time.Millisecond)

	if got := reasonTurns(t, store, task.ID); got != before {
		t.Fatalf("reason turns=%d, want %d: an interruption past the age bound was re-entered", got, before)
	}
}

// TestResumePolicyKnobs: the age bound is a duration whose two special answers are the
// default (empty) and "no bound" (0 or less), and a value that is not a duration is the
// default instead of a silent zero; the self-heal itself is on unless it is switched off.
func TestResumePolicyKnobs(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", 24 * time.Hour},
		{"30m", 30 * time.Minute},
		{"0", 0},
		{"-5m", 0},
		{"half an hour", 24 * time.Hour},
	}
	for _, c := range cases {
		t.Setenv("AUTONOMY_RESUME_MAX_AGE", c.raw)
		if got := resumeMaxAge(); got != c.want {
			t.Fatalf("resumeMaxAge(%q)=%s, want %s", c.raw, got, c.want)
		}
	}
	for _, raw := range []string{"", "1", "true", "yes"} {
		t.Setenv("AUTONOMY_RESUME_INTERRUPTED", raw)
		if !resumeInterruptedEnabled() {
			t.Fatalf("resumeInterruptedEnabled()=false for %q, want on by default", raw)
		}
	}
	for _, raw := range []string{"0", "false", "no", "off", "OFF"} {
		t.Setenv("AUTONOMY_RESUME_INTERRUPTED", raw)
		if resumeInterruptedEnabled() {
			t.Fatalf("resumeInterruptedEnabled()=true for %q, want off", raw)
		}
	}
}

// TestTheBriefingSaysTheRoundWasCut: the run that continues a cut round is told so — the
// reason the runtime wrote, and the step to pick up from — instead of reading a
// half-finished plan as the whole story.
func TestTheBriefingSaysTheRoundWasCut(t *testing.T) {
	store, task, _ := briefingStore(t)
	planID, planned, err := saveExecutionPlan(ExecutionPlan{
		TaskID: "task-brief", AgentID: 10001, Cycle: 1, DecisionType: "plan",
		Reason: "a plan whose second step never ran", StepCount: 1, CreatedAt: time.Now(),
	}, []ExecutionStepPlan{
		{Idx: 1, Name: "review", Capability: "pull_request.review", Input: `{"pr":"https://example/1"}`},
		{Idx: 2, Name: "deploy", Capability: "service.deploy", Input: `{"branch":"main"}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The first step ran; the second never got the chance — that is the cut.
	if _, err := store.AppendExecutionStep(ExecutionStep{
		PlanID: planID, PlanStepID: planned[0].ID, TaskID: "task-brief", AgentID: 10001, Cycle: 1,
		Idx: 1, Name: "review", Capability: "pull_request.review", Status: "ok",
		Input: `{"pr":"https://example/1"}`, Output: `{"head_branch":"feature/x"}`,
	}); err != nil {
		t.Fatal(err)
	}
	reason := StopReason{By: stoppedByRuntime, RequestID: "pipeline-archived"}

	// Not interrupted yet: a task that is still running has nothing to declare.
	if cut := (&Autonomy{Store: store}).taskBriefing(task.ID).State.Interrupted; cut != nil {
		t.Fatalf("interrupted=%+v, want none while the task is running", cut)
	}

	task.Status = TaskStatusStopped
	task.Error = reason.errorText()
	if err := store.UpsertTask(task); err != nil {
		t.Fatal(err)
	}
	brief := (&Autonomy{Store: store}).taskBriefing(task.ID)
	if brief == nil || brief.State == nil || brief.State.Interrupted == nil {
		t.Fatalf("briefing=%+v, want the interruption it was cut by", brief)
	}
	cut := brief.State.Interrupted
	if !strings.Contains(cut.Reason, "pipeline-archived") {
		t.Fatalf("reason=%q, want the restart it belonged to", cut.Reason)
	}
	if cut.StoppedAtStep != 2 || cut.NextStep != "deploy" {
		t.Fatalf("cut=%+v, want the step that never ran", cut)
	}
	if !strings.Contains(brief.Note, "cut by a restart") {
		t.Fatalf("note=%q, want the briefing to say the round was cut", brief.Note)
	}
}

// TestTheInterruptedFlagIsNotGuessed: the reader's negative side. A plan with a step that
// never ran looks like an interruption, but only the runtime's own stop record can say so
// — a task that ended on its own, or was stopped by a person, has none.
func TestTheInterruptedFlagIsNotGuessed(t *testing.T) {
	store, task, _ := briefingStore(t)
	if _, _, err := saveExecutionPlan(ExecutionPlan{
		TaskID: "task-brief", AgentID: 10001, Cycle: 1, DecisionType: "plan",
		Reason: "a plan with a step that never ran", StepCount: 1, CreatedAt: time.Now(),
	}, []ExecutionStepPlan{{Idx: 1, Name: "deploy", Capability: "service.deploy"}}); err != nil {
		t.Fatal(err)
	}

	for _, status := range []string{TaskStatusRunning, TaskStatusPending, "", TaskStatusCompleted} {
		task.Status = status
		task.Error = ""
		if err := store.UpsertTask(task); err != nil {
			t.Fatal(err)
		}
		if cut := (&Autonomy{Store: store}).taskBriefing(task.ID).State.Interrupted; cut != nil {
			t.Fatalf("status=%q interrupted=%+v, want none without the runtime's own stop record", status, cut)
		}
	}
	task.Status = TaskStatusStopped
	task.Error = "stopped"
	if err := store.UpsertTask(task); err != nil {
		t.Fatal(err)
	}
	if cut := (&Autonomy{Store: store}).taskBriefing(task.ID).State.Interrupted; cut != nil {
		t.Fatalf("interrupted=%+v, want none for a person's stop", cut)
	}
}

// TestTaskDetailCarriesTheState: the panel reads the same state a continuing run is
// handed — the newest round, what the contract still misses, and that the runtime cut the
// run (and where) — instead of reconstructing it from the plans.
func TestTaskDetailCarriesTheState(t *testing.T) {
	store, task, _ := briefingStore(t)
	planID, planned, err := saveExecutionPlan(ExecutionPlan{
		TaskID: "task-brief", AgentID: 10001, Cycle: 1, DecisionType: "plan",
		Reason: "deliver the dashboard, then deploy it", StepCount: 1, CreatedAt: time.Now(),
	}, []ExecutionStepPlan{
		{Idx: 1, Name: "implement", Capability: "code_edit", Input: `{"instruction":"dashboard"}`},
		{Idx: 2, Name: "deploy", Capability: "service.deploy", Input: `{"branch":"main"}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendExecutionStep(ExecutionStep{
		PlanID: planID, PlanStepID: planned[0].ID, TaskID: "task-brief", AgentID: 10001, Cycle: 1,
		Idx: 1, Name: "implement", Capability: "code_edit", Status: "ok",
		Input: `{"instruction":"dashboard"}`, Output: `{"pr_url":"https://github.com/kaulie/autonomy/pull/144"}`,
	}); err != nil {
		t.Fatal(err)
	}
	task.Status = TaskStatusStopped
	task.Error = StopReason{By: stoppedByRuntime, RequestID: "pipeline-detail"}.errorText()
	if err := store.UpsertTask(task); err != nil {
		t.Fatal(err)
	}

	srv := NewHTTPServer(&Autonomy{Store: store})
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/task-brief", nil)
	req.SetPathValue("taskID", "task-brief")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var detail struct {
		State *TaskState `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.State == nil || detail.State.LastRound == nil {
		t.Fatalf("state=%+v, want the newest round", detail.State)
	}
	if detail.State.Interrupted == nil || !strings.Contains(detail.State.Interrupted.Reason, "pipeline-detail") {
		t.Fatalf("state.interrupted=%+v, want the restart that cut it", detail.State.Interrupted)
	}
	if detail.State.Interrupted.NextStep != "deploy" || detail.State.Interrupted.StoppedAtStep != 2 {
		t.Fatalf("state.interrupted=%+v, want the step that never ran", detail.State.Interrupted)
	}
}
