package autonomy

import (
	"strings"
	"testing"
	"time"
)

// TestRunAcceptsTheSameRequestAsTheHTTPDoor: Run is not a second way to start a task,
// it is the same door with more patience — the request is the one POST /api/tasks
// takes (AcceptTaskRequest), it goes through the same accept path, and what is left
// behind is the same thing: a task row, and one user instruction for that task,
// saying what the request said.
func TestRunAcceptsTheSameRequestAsTheHTTPDoor(t *testing.T) {
	store, rt := plannerRun(t, "answer done")

	req := AcceptTaskRequest{
		ID:          "task-one-door",
		Description: "answer done",
		Domain:      string(TaskDomainServer),
		GoalType:    GoalType_FEATURE,
	}
	accepted, err := rt.Run(req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if accepted == nil || accepted.TaskID != req.ID || accepted.AgentID == 0 || accepted.MessageID == 0 {
		t.Fatalf("acceptance=%+v, want the task, its agent and the message that ran it", accepted)
	}

	task, err := store.GetTask(req.ID)
	if err != nil || task == nil {
		t.Fatalf("task=%v err=%v, want the row the request was accepted as", task, err)
	}
	if task.Description != req.Description || task.Domain != TaskDomainServer {
		t.Fatalf("task=%+v, want the request's own description and domain", task)
	}

	messages, err := store.ListAgentMessages(accepted.AgentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages=%+v, want exactly the one instruction this run was", messages)
	}
	msg := messages[0]
	if msg.Kind != MessageKindInstruction || msg.Sender != MessageSenderUser || strings.TrimSpace(msg.Content) != req.Description {
		t.Fatalf("message=%+v, want a user instruction saying what the request said", msg)
	}
	// Run waited: by the time it returns, the message it accepted has been processed.
	if msg.Status != MessageStatusDone {
		t.Fatalf("message status=%q, want the run to have been waited for", msg.Status)
	}
}

// TestRunWithOnlyTheTasksID: a request may carry nothing but the task's id — the same
// thing HTTP's door accepts — and the instruction is then what the task already says
// about itself, in the queue and on its row.
func TestRunWithOnlyTheTasksID(t *testing.T) {
	store, rt := plannerRun(t, "answer done")

	first, err := rt.Run(AcceptTaskRequest{ID: "task-again", Description: "answer done"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The same task, this time with no words of its own.
	if _, err := rt.Run(AcceptTaskRequest{ID: "task-again"}); err != nil {
		t.Fatalf("Run with only the task's id: %v", err)
	}

	messages, err := store.ListAgentMessages(first.AgentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages=%+v, want the second instruction to have been accepted too", messages)
	}
	if content := strings.TrimSpace(messages[1].Content); content != "answer done" {
		t.Fatalf("second instruction=%q, want the description the task already had", content)
	}
	if messages[1].TaskID != "task-again" {
		t.Fatalf("second message task_id=%q, want the task it was accepted for", messages[1].TaskID)
	}
	task, err := store.GetTask("task-again")
	if err != nil || task == nil {
		t.Fatalf("task=%v err=%v", task, err)
	}
	if task.Description != "answer done" {
		t.Fatalf("task description=%q, want an instruction with no words of its own to leave it alone", task.Description)
	}
}

// TestTheAcceptanceSaysHowManyMessagesAreInFront: `queued` is how many messages the
// agent still has in front of the one just accepted — the ones that arrived before it
// and have not finished, the message being processed right now included. 0 means the
// agent is on it, or is about to take it next; an instruction accepted while the agent
// is working on another says 1.
func TestTheAcceptanceSaysHowManyMessagesAreInFront(t *testing.T) {
	store := resumeTestStore(t)
	// The agent's work is held open, so the first instruction is still being
	// processed while the second is accepted behind it.
	probe := &inboxProbe{started: make(chan string, 2), release: make(chan struct{})}
	f := NewAgentFactory()
	auto := &Autonomy{AgentFactory: f, Runtime: NewRuntime(f), Store: store}
	auto.Inbox = NewInbox(store, probe.handle, nil)

	first, err := auto.AcceptTask(AcceptTaskRequest{ID: "t-ahead", Description: "one"})
	if err != nil {
		t.Fatalf("AcceptTask: %v", err)
	}
	if first.Queued != 0 {
		t.Fatalf("queued=%d for the first instruction, want 0: nothing is in front of it", first.Queued)
	}
	select {
	case <-probe.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the agent never picked up the first instruction")
	}

	second, err := auto.AcceptTask(AcceptTaskRequest{ID: "t-ahead", Description: "two"})
	if err != nil {
		t.Fatalf("AcceptTask: %v", err)
	}
	if second.Queued != 1 {
		t.Fatalf("queued=%d for the second instruction, want 1: the message being processed is in front of it", second.Queued)
	}

	close(probe.release)
	waitForMessageStatus(t, store, second.AgentID, second.MessageID, MessageStatusDone)
	waitForInboxDry(t, store, second.AgentID)
}

// path differ in patience, not in what they accept — the same request leaves the same
// record through HTTP as through Run.
func TestTheSameRequestIsAcceptedWhicheverDoorTakesIt(t *testing.T) {
	store, rt := plannerRun(t, "answer done")

	viaHTTP, err := rt.AcceptTask(AcceptTaskRequest{ID: "task-http-door", Description: "answer done", Domain: string(TaskDomainServer)})
	if err != nil {
		t.Fatalf("AcceptTask: %v", err)
	}
	viaRun, err := rt.Run(AcceptTaskRequest{ID: "task-run-door", Description: "answer done", Domain: string(TaskDomainServer)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	message := func(agentID int64) AgentMessage {
		t.Helper()
		messages, err := store.ListAgentMessages(agentID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(messages) != 1 {
			t.Fatalf("agent %d has %d messages, want the one instruction", agentID, len(messages))
		}
		return messages[0]
	}
	httpMsg, runMsg := message(viaHTTP.AgentID), message(viaRun.AgentID)
	if httpMsg.Kind != runMsg.Kind || httpMsg.Sender != runMsg.Sender || httpMsg.Content != runMsg.Content {
		t.Fatalf("http=%+v run=%+v, want the same instruction from either door", httpMsg, runMsg)
	}

	httpTask, err := store.GetTask(viaHTTP.TaskID)
	if err != nil || httpTask == nil {
		t.Fatalf("task=%v err=%v", httpTask, err)
	}
	runTask, err := store.GetTask(viaRun.TaskID)
	if err != nil || runTask == nil {
		t.Fatalf("task=%v err=%v", runTask, err)
	}
	if httpTask.Description != runTask.Description || httpTask.Domain != runTask.Domain || httpTask.GoalType != runTask.GoalType {
		t.Fatalf("http=%+v run=%+v, want the same task from either door", httpTask, runTask)
	}

	// The door that does not wait is still running behind us: let it settle before
	// the test closes the store under it.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		task, err := store.GetTask(viaHTTP.TaskID)
		if err == nil && task != nil && task.Status != TaskStatusPending && task.Status != TaskStatusRunning {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the instruction HTTP accepted for %s never finished", viaHTTP.TaskID)
}
