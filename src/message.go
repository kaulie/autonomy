package autonomy

import "time"

// Every agent has an inbox: the messages addressed to it, in the order they
// arrived, and the agent processes them one at a time (src/inbox.go). The three
// things that can address an agent are the three senders:
//
//	user    an instruction for a task (POST /api/tasks, Autonomy.Run)
//	agent   another agent handing it a job (a capability's delegated prompt)
//	system  the runtime itself (today: a stop)
//
// A message is what was said and who said it; the turn it produces is recorded
// as any other turn (reason_turns / llm_messages), with the message as the input
// it answered. So the queue is the *order* — who spoke, when — and the LLM record
// is what came of it.
//
// It is a row (InboxStore), not memory: instructions that arrived before a
// restart are still in the agent's inbox afterwards.
type MessageSender string

const (
	MessageSenderUser   MessageSender = "user"
	MessageSenderAgent  MessageSender = "agent"
	MessageSenderSystem MessageSender = "system"
)

// AgentMessageKind is what a message asks the agent for.
type AgentMessageKind string

const (
	// MessageKindInstruction is a user instruction: the description a task was
	// accepted with, or a later command for the same task. The planner may
	// create or replace a plan and execute it.
	MessageKindInstruction AgentMessageKind = "instruction"
	// MessageKindChat is a user message for the planner only: it is answered as
	// a conversation turn and must not create, replace, or execute a plan
	// (AcceptTaskRequest.Mode = "chat").
	MessageKindChat AgentMessageKind = "chat"
	// MessageKindApproval is the user confirming a plan the agent produced and is
	// waiting on: the run releases the plan it paused on (status awaiting_approval)
	// and executes it, so implementation starts after the plan is approved
	// (AcceptTaskRequest.Approve, Agent.RequirePlanApproval).
	MessageKindApproval AgentMessageKind = "approval"
	// MessageKindDelegation is another agent handing this one a job — what a
	// capability prompts a worker it acquired with.
	MessageKindDelegation AgentMessageKind = "delegation"
	// MessageKindStop is the system telling the agent to stop what it is doing.
	// The stop is enforced by cancelling the message being processed; this is the
	// record of it, in its place in the queue.
	MessageKindStop AgentMessageKind = "stop"
)

// AgentMessageStatus is where a message is in the queue.
type AgentMessageStatus string

const (
	// MessageStatusQueued is waiting for the agent: nothing has started it.
	MessageStatusQueued AgentMessageStatus = "queued"
	// MessageStatusRunning is being processed right now. A message left running
	// by a process that died is put back in the queue by the next one
	// (InboxStore.RequeueRunningMessages).
	MessageStatusRunning AgentMessageStatus = "running"
	// MessageStatusDone was processed.
	MessageStatusDone AgentMessageStatus = "done"
	// MessageStatusFailed was processed and failed — the reason is Error, and the
	// task's own row says what it means for the task.
	MessageStatusFailed AgentMessageStatus = "failed"
	// MessageStatusStopped was cancelled (see MessageKindStop).
	MessageStatusStopped AgentMessageStatus = "stopped"
)

// AgentMessage is one message in one agent's inbox.
type AgentMessage struct {
	// ID is the row id: it is the message's place in the queue, and the order the
	// agent processes messages in.
	ID      int64
	AgentID int64
	// TaskID is the task the message is about (empty for a message that belongs
	// to no task).
	TaskID string
	// Sender is who addressed the agent, and SenderID who exactly: the user, the
	// delegating agent's name, or the runtime's own source.
	Sender   MessageSender
	SenderID string
	Kind     AgentMessageKind
	// Content is what was said, verbatim: the instruction, the delegated prompt,
	// or the reason for a stop.
	Content   string
	Status    AgentMessageStatus
	Error     string
	CreatedAt time.Time
	// StartedAt and EndedAt are when the agent picked it up and put it down.
	StartedAt time.Time
	EndedAt   time.Time
}

// open reports whether the message is still the agent's to process.
func (m AgentMessage) open() bool {
	return m.Status == "" || m.Status == MessageStatusQueued
}
