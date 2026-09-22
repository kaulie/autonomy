package autonomy

import (
	"strings"
	"testing"
)

func TestParseUserMessageKind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    AgentMessageKind
		wantErr bool
	}{
		{"", MessageKindInstruction, false},
		{"command", MessageKindInstruction, false},
		{"COMMAND", MessageKindInstruction, false},
		{"chat", MessageKindChat, false},
		{" Chat ", MessageKindChat, false},
		{"plan", "", true},
		{"agent", "", true},
	}
	for _, tc := range cases {
		got, err := parseUserMessageKind(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("mode %q: want error", tc.in)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("mode %q: kind=%q err=%v, want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestChatModeDoesNotWriteOrReplaceAPlan(t *testing.T) {
	t.Setenv("AUTONOMY_REASONER", "local")
	t.Setenv("AUTONOMY_MAX_STEPS", "1")
	store := executionTestStore(t)
	rt := &Autonomy{
		AgentFactory: NewAgentFactory(),
		Runtime:      NewRuntime(NewAgentFactory()),
		Store:        store,
	}

	const taskID = "task-chat-mode"
	// Seed a plan the way a finished command would have left it.
	existingID, err := store.CreateExecutionPlan(ExecutionPlan{
		TaskID: taskID, Cycle: 1, DecisionType: "plan", Reason: "do the work", StepCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTask(&Task{
		ID: taskID, Description: "do the work", Status: TaskStatusCompleted,
	}); err != nil {
		t.Fatal(err)
	}

	accepted, err := rt.Run(AcceptTaskRequest{
		ID:          taskID,
		Description: "what is the current plan?",
		Mode:        UserMessageModeChat,
	})
	if err != nil {
		t.Fatalf("chat Run: %v", err)
	}

	messages, err := store.ListAgentMessages(accepted.AgentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages=%d, want exactly the chat", len(messages))
	}
	chat := messages[0]
	if chat.Kind != MessageKindChat {
		t.Fatalf("chat kind=%q, want %q", chat.Kind, MessageKindChat)
	}
	if strings.TrimSpace(chat.Content) != "what is the current plan?" {
		t.Fatalf("chat content=%q, want the user's words (not the planner wrapper)", chat.Content)
	}

	plansAfter, err := store.ListExecutionPlans(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plansAfter) != 1 || plansAfter[0].ID != existingID || plansAfter[0].DecisionType != "plan" {
		t.Fatalf("existing plan changed: %+v (id was %d)", plansAfter, existingID)
	}

	taskAfter, err := store.GetTask(taskID)
	if err != nil || taskAfter == nil {
		t.Fatalf("task after chat: %v %v", taskAfter, err)
	}
	if taskAfter.Status != TaskStatusCompleted {
		t.Fatalf("chat moved the task status to %q, want the seeded completed", taskAfter.Status)
	}
	if taskAfter.Description != "do the work" {
		t.Fatalf("chat overwrote the task description to %q", taskAfter.Description)
	}
}

func TestAcceptTaskRejectsUnknownMode(t *testing.T) {
	store := resumeTestStore(t)
	rt := &Autonomy{AgentFactory: NewAgentFactory(), Runtime: NewRuntime(NewAgentFactory()), Store: store}
	_, err := rt.AcceptTask(AcceptTaskRequest{ID: "task-bad-mode", Description: "hi", Mode: "plan"})
	if err == nil || !strings.Contains(err.Error(), "chat") {
		t.Fatalf("err=%v, want mode must be chat or command", err)
	}
}

func TestOmittedModeStaysAnInstruction(t *testing.T) {
	store := resumeTestStore(t)
	probe := &inboxProbe{started: make(chan string, 1), release: make(chan struct{})}
	rt := &Autonomy{AgentFactory: NewAgentFactory(), Runtime: NewRuntime(NewAgentFactory()), Store: store}
	rt.Inbox = NewInbox(store, probe.handle, nil)
	accepted, err := rt.AcceptTask(AcceptTaskRequest{ID: "task-default-mode", Description: "go"})
	if err != nil {
		t.Fatalf("AcceptTask: %v", err)
	}
	messages, err := store.ListAgentMessages(accepted.AgentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Kind != MessageKindInstruction {
		t.Fatalf("messages=%+v, want one instruction (omitted mode = command)", messages)
	}
	close(probe.release)
	waitForInboxDry(t, store, accepted.AgentID)
}
