package autonomy

import "github.com/kaulie/autonomy/src/llmbackend"

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The task ↔ agent pairing across process lifetimes: an instruction finds the
// agent the task is paired with, in this process or in the one that just started,
// and runs on it (src/agent_resume.go).

// resumeTestStore opens a store and points the package's active store at it, so the
// runtime's own persistence calls (persistTask / persistAgent) land here.
func resumeTestStore(t *testing.T) rawStore {
	t.Helper()
	store, err := openStore(filepath.Join(t.TempDir(), "autonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	prev := _store
	t.Cleanup(func() {
		_store = prev
		_ = store.Close()
	})
	_store = store
	return store
}

// pairedTask is what a process that ran a task leaves behind: the task row with
// its agent_id, and the agent row — id, name, provider session — that row names.
func pairedTask(t *testing.T, store rawStore, taskID string) (*Task, *Agent) {
	t.Helper()
	task := &Task{ID: taskID, Description: "ship it", Status: TaskStatusRunning}
	agent := &Agent{
		State:       "idle",
		Lifecycle:   AgentLifecyclePersistent,
		LLMProvider: llmbackend.ProviderCline,
		Model:       "deepseek-v4-pro",
		LLMAgentID:  "cls-before-the-restart",
		CurrentTask: task,
	}
	if err := store.UpsertAgent(agent); err != nil {
		t.Fatal(err)
	}
	task.AgentID = agent.ID
	if err := store.UpsertTask(task); err != nil {
		t.Fatal(err)
	}
	return task, agent
}

// TestASecondInstructionForATaskReusesItsAgent: the instruction after the one that
// created the agent is the same conversation, not a second agent on one task.
func TestASecondInstructionForATaskReusesItsAgent(t *testing.T) {
	store := resumeTestStore(t)
	auto := &Autonomy{AgentFactory: NewAgentFactory(), Store: store}

	first, err := auto.resumeAgentForTask(&Task{ID: "t-reused", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == 0 || first.Role != AgentRolePlanner {
		t.Fatalf("first instruction got %+v, want a planner agent", first)
	}
	// The second instruction arrives as its own Task value, the way AcceptTask
	// builds one from a request that names the same task id.
	second, err := auto.resumeAgentForTask(&Task{ID: "t-reused", AgentID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("second instruction got agent %d, want the same handle as the first (%d)", second.ID, first.ID)
	}
}

// TestAfterARestartATaskResumesTheAgentItsRowNames is the restart flow: a process
// that holds nothing takes an instruction, finds the agent by the task's own row,
// rebuilds the handle, and registers it in the factory — without opening a provider
// session, which is the first turn's job (see the test below).
func TestAfterARestartATaskResumesTheAgentItsRowNames(t *testing.T) {
	store := resumeTestStore(t)
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")

	task, agent := pairedTask(t, store, "t-restarted")

	// A fresh process: nothing in memory, only what the store kept.
	f := NewAgentFactory()
	restarted := &Autonomy{AgentFactory: f, Runtime: NewRuntime(f), Store: store}
	got, err := restarted.resumeAgentForTask(&Task{ID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != agent.ID || got.Name != agent.Name {
		t.Fatalf("resumed agent %d/%s, want the task's own %d/%s", got.ID, got.Name, agent.ID, agent.Name)
	}
	if got.Role != AgentRolePlanner {
		t.Fatalf("role=%q, want %q", got.Role, AgentRolePlanner)
	}
	if f.Get(got.Name) != got {
		t.Fatal("the resumed handle is not the one the factory maintains")
	}
	// The handle is the row's record and nothing more: no provider session was
	// opened, and the session id on the row is untouched — which is what keeps an
	// accept (and a broadcast's many accepts) from waiting on a bridge.
	if got.LLMAgentID != agent.LLMAgentID {
		t.Fatalf("LLMAgentID=%q, want the recorded session %q: accepting an instruction opens no session",
			got.LLMAgentID, agent.LLMAgentID)
	}
	if got.cursorAgent != nil || got.clineAgent != nil || got.Session != nil {
		t.Fatal("the accept path attached a provider session: it belongs to the turn")
	}
	stored, err := store.GetAgent(agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LLMAgentID != agent.LLMAgentID {
		t.Fatalf("agents.llm_agent_id=%q, want the recorded session %q: the row is rewritten when a session is opened, not before",
			stored.LLMAgentID, agent.LLMAgentID)
	}
}

// TestTheFirstTurnOpensTheSessionAnAcceptLeftAlone: what the accept path gave up is
// the turn's to do — the first cycle attaches the agent's backend session, asking it to
// continue the recorded one (a Cline session lives inside the bridge process, so a
// restart takes it and the conversation is continued by seeding the new session with the
// recorded session's transcript), and records the session this process is now on.
func TestTheFirstTurnOpensTheSessionAnAcceptLeftAlone(t *testing.T) {
	store := resumeTestStore(t)
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")

	task, agent := pairedTask(t, store, "t-restarted-turn")

	f := NewAgentFactory()
	restarted := &Autonomy{AgentFactory: f, Runtime: NewRuntime(f), Store: store}
	got, err := restarted.resumeAgentForTask(&Task{ID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := got.ensureLLMSession(context.Background(), "", got.Workspace, ReasonModePlan); err != nil {
		t.Fatalf("the first turn could not open the session the accept left alone: %v", err)
	}
	if got.clineAgent == nil {
		t.Fatal("the turn ran without attaching the agent's session")
	}
	// Opening the session asks the bridge to continue the recorded one, and records
	// nothing yet: the live session id exists only once a run started.
	if got.clineAgent.ResumeSessionID != agent.LLMAgentID {
		t.Fatalf("resume=%q, want the recorded session %q", got.clineAgent.ResumeSessionID, agent.LLMAgentID)
	}
	if got.LLMAgentID != agent.LLMAgentID {
		t.Fatalf("LLMAgentID=%q, want the recorded session %q until a run starts",
			got.LLMAgentID, agent.LLMAgentID)
	}
	if !got.needsLLMFrame() {
		t.Fatal("a session this process started has no frame yet: the next cycle must send it")
	}

	// The run is what makes the live session known, and that is what is recorded for
	// the next restart.
	if _, _, err := got.PromptLLMStream(context.Background(), "plan something", ReasonModePlan, nil); err != nil {
		t.Fatalf("the turn's prompt: %v", err)
	}
	if got.LLMAgentID == agent.LLMAgentID || got.LLMAgentID != got.clineAgent.SessionID {
		t.Fatalf("LLMAgentID=%q, want this process's live session %q, not the recorded one (%q)",
			got.LLMAgentID, got.clineAgent.SessionID, agent.LLMAgentID)
	}
	stored, err := store.GetAgent(agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LLMAgentID != got.LLMAgentID {
		t.Fatalf("agents.llm_agent_id=%q, want the live session %q", stored.LLMAgentID, got.LLMAgentID)
	}
}

// TestARestartedRuntimeRunsTheInstructionOnTheTasksAgent: the same flow through
// Autonomy.Run — the cycles of a task nobody in this process created are still
// that task's own agent's cycles.
func TestARestartedRuntimeRunsTheInstructionOnTheTasksAgent(t *testing.T) {
	store := resumeTestStore(t)
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	t.Setenv("AUTONOMY_REASONER", "local")
	t.Setenv("AUTONOMY_MAX_STEPS", "1")

	task, agent := pairedTask(t, store, "t-restarted-run")

	f := NewAgentFactory()
	restarted := &Autonomy{AgentFactory: f, Runtime: NewRuntime(f), Store: store}
	_, _ = restarted.Run(AcceptTaskRequest{ID: task.ID})

	var agentID int64
	if err := store.RawDB().QueryRow(`SELECT agent_id FROM reason_turns WHERE task_id = ? LIMIT 1`, task.ID).Scan(&agentID); err != nil {
		t.Fatalf("the instruction left no run for the task: %v", err)
	}
	if agentID != agent.ID {
		t.Fatalf("reason_turns.agent_id=%d, want the task's own agent %d", agentID, agent.ID)
	}
	var agents int
	if err := store.RawDB().QueryRow(`SELECT count(*) FROM agents`).Scan(&agents); err != nil {
		t.Fatal(err)
	}
	if agents != 1 {
		t.Fatalf("agents=%d, want the one the task was already paired with", agents)
	}
}

// TestATaskWhoseAgentWasLetGoGetsANewOne: a deleted agent is not resumed — the
// task gets a new agent instead of a handle back to a row that was let go.
// waitForTaskToFinish waits until the agent is done with what it was processing, so
// a test can read the record a run left behind.
func waitForTaskToFinish(t *testing.T, taskID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, running := inFlightTasks.Load(taskID); !running {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("task %s never left the in-flight set", taskID)
}

// TestASecondInstructionIsAcceptedOnTheSameAgent is the flow as a user sees it: an
// instruction arrives, the runtime pairs the task with an agent, and the next
// instruction for that task is that agent's next conversation — not a second agent
// on one task.
func TestASecondInstructionIsAcceptedOnTheSameAgent(t *testing.T) {
	store := resumeTestStore(t)
	t.Setenv("AUTONOMY_REASONER", "local")
	t.Setenv("AUTONOMY_MAX_STEPS", "1")

	f := NewAgentFactory()
	auto := &Autonomy{AgentFactory: f, Runtime: NewRuntime(f), Store: store}

	first, err := auto.AcceptTask(AcceptTaskRequest{ID: "t-http", Description: "ship it"})
	if err != nil {
		t.Fatal(err)
	}
	if first.AgentID == 0 || first.MessageID == 0 {
		t.Fatalf("the accepted instruction has no agent or no message: %+v", first)
	}

	// The second instruction arrives while the agent may still be busy, and that is
	// the point: it is accepted and queued, not refused.
	second, err := auto.AcceptTask(AcceptTaskRequest{ID: "t-http", Description: "and again"})
	if err != nil {
		t.Fatal(err)
	}
	if second.AgentID != first.AgentID {
		t.Fatalf("second instruction got agent %d, want the task's own agent %d", second.AgentID, first.AgentID)
	}
	if second.MessageID == first.MessageID {
		t.Fatalf("both instructions are message %d, want one message each", first.MessageID)
	}
	var agents int
	if err := store.RawDB().QueryRow(`SELECT count(*) FROM agents`).Scan(&agents); err != nil {
		t.Fatal(err)
	}
	if agents != 1 {
		t.Fatalf("agents=%d, want the one this task's instructions share", agents)
	}
	// Both instructions are the agent's messages, in the order they were accepted.
	waitForTaskToFinish(t, "t-http")
	messages, err := store.ListAgentMessages(first.AgentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("inbox=%d messages, want the two instructions", len(messages))
	}
	for i, want := range []string{"ship it", "and again"} {
		if messages[i].Content != want || messages[i].Sender != MessageSenderUser || messages[i].Kind != MessageKindInstruction {
			t.Errorf("message %d=%+v, want the %q instruction from the user", i, messages[i], want)
		}
	}
}

func TestATaskWhoseAgentWasLetGoGetsANewOne(t *testing.T) {
	store := resumeTestStore(t)
	task, agent := pairedTask(t, store, "t-let-go")
	if err := store.SoftDeleteAgent(agent.ID); err != nil {
		t.Fatal(err)
	}

	auto := &Autonomy{AgentFactory: NewAgentFactory(), Store: store}
	got, err := auto.resumeAgentForTask(&Task{ID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == agent.ID {
		t.Fatalf("resumed the agent %d that was let go", agent.ID)
	}
	if got.CurrentTask == nil || got.CurrentTask.ID != task.ID {
		t.Fatalf("the new agent is not bound to the task: %+v", got)
	}
}

// waitForInboxDry waits until the agent has no queued or running message left, so a
// test can read everything the instructions produced.
func waitForInboxDry(t *testing.T, store rawStore, agentID int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		messages, err := store.ListAgentMessages(agentID, 0)
		if err != nil {
			t.Fatal(err)
		}
		busy := 0
		for _, msg := range messages {
			if msg.Status == MessageStatusQueued || msg.Status == MessageStatusRunning {
				busy++
			}
		}
		if busy == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the agent never finished its inbox")
}

// TestAQueuedInstructionReachesTheCycleThatAnswersIt: a second instruction for a
// task is a message behind the first, and the cycle that answers it carries it —
// the agent reads what it was asked, not only the description its task began with.
func TestAQueuedInstructionReachesTheCycleThatAnswersIt(t *testing.T) {
	store := resumeTestStore(t)
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	t.Setenv("AUTONOMY_REASONER", "llm")
	t.Setenv("AUTONOMY_MAX_STEPS", "1")
	// The LLM prompt is built from the policy files, so this test needs a root that
	// has them (a deployment renders the same prompt).
	t.Setenv("PROJECT_ROOT", preparePolicyRoot(t))

	task, agent := pairedTask(t, store, "t-instruction")
	f := NewAgentFactory()
	auto := &Autonomy{AgentFactory: f, Runtime: NewRuntime(f), Store: store}

	// Two instructions for the same task: both reach the same agent, and the second
	// waits for the first (the canned answer both prompts draw is the fake planner's).
	if _, err := auto.AcceptTask(AcceptTaskRequest{ID: task.ID, Description: "answer done: deploy the service"}); err != nil {
		t.Fatal(err)
	}
	if _, err := auto.AcceptTask(AcceptTaskRequest{ID: task.ID, Description: "answer done: and then tell me"}); err != nil {
		t.Fatal(err)
	}
	waitForInboxDry(t, store, agent.ID)

	rows, err := store.RawDB().Query(`SELECT input FROM reason_turns WHERE task_id = ? ORDER BY id`, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var prompts []string
	for rows.Next() {
		var input string
		if err := rows.Scan(&input); err != nil {
			t.Fatal(err)
		}
		prompts = append(prompts, input)
	}
	if len(prompts) < 2 {
		t.Fatalf("the two instructions produced %d runs, want one each", len(prompts))
	}
	for i, want := range []string{"deploy the service", "and then tell me"} {
		if !strings.Contains(prompts[i], want) {
			t.Errorf("run %d does not carry its instruction %q", i, want)
		}
		if !strings.Contains(prompts[i], "additional_input") {
			t.Errorf("run %d does not render the message as additional_input", i)
		}
	}
}

// TestARunLeavesTheAgentResident: a run ending does not stop the agent. It is only
// marked idle, its session stays attached, and the next instruction continues on that
// same session — one agent, one conversation, however many instructions arrive.
func TestARunLeavesTheAgentResident(t *testing.T) {
	store := resumeTestStore(t)
	installFakeClineClient(t)
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	t.Setenv("AUTONOMY_REASONER", "llm")
	t.Setenv("AUTONOMY_MAX_STEPS", "1")
	t.Setenv("PROJECT_ROOT", preparePolicyRoot(t))

	task, _ := pairedTask(t, store, "t-resident")
	f := NewAgentFactory()
	auto := &Autonomy{AgentFactory: f, Runtime: NewRuntime(f), Store: store}

	var sessions []string
	for i := 1; i <= 2; i++ {
		// Whether the instruction concludes anything is not what this test is about:
		// what matters is what an agent looks like after the queue runs dry.
		_, _ = auto.Run(AcceptTaskRequest{ID: task.ID, Description: fmt.Sprintf("answer done: instruction %d", i)})
		agent := f.ForTask(task.ID)
		if agent == nil {
			t.Fatalf("instruction %d left no agent for the task", i)
		}
		if agent.State != "idle" {
			t.Fatalf("after instruction %d state=%q, want idle (a finished run does not leave it running)", i, agent.State)
		}
		if agent.clineAgent == nil || agent.LLMAgentID == "" {
			t.Fatalf("after instruction %d the agent holds no session (%q): a resident agent keeps it", i, agent.LLMAgentID)
		}
		if agent.needsLLMFrame() {
			t.Fatalf("after instruction %d the session is treated as new; a resident agent's session already has the frame", i)
		}
		sessions = append(sessions, agent.LLMAgentID)
	}
	if sessions[0] != sessions[1] {
		t.Fatalf("the second instruction opened a new session (%q → %q); a resident agent continues the one it has", sessions[0], sessions[1])
	}

	// Still one agent, with its row and its two messages: a finished run is not a farewell.
	resident := f.ForTask(task.ID)
	stored, err := store.GetAgent(resident.ID)
	if err != nil || stored == nil || !stored.DeletedAt.IsZero() {
		t.Fatalf("the agent row is gone or let go: %+v err=%v", stored, err)
	}
	if stored.LLMAgentID != sessions[1] {
		t.Fatalf("agents.llm_agent_id=%q, want the resident session %q", stored.LLMAgentID, sessions[1])
	}
	messages, err := store.ListAgentMessages(stored.ID, 0)
	if err != nil || len(messages) != 2 {
		t.Fatalf("inbox=%d messages err=%v, want both instructions", len(messages), err)
	}
}
