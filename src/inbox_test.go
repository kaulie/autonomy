package autonomy

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The inbox is one agent's queue of messages — from the user, from another agent,
// from the runtime — processed one at a time, in the order they arrived
// (src/inbox.go). These tests pin that order, what happens to a message that
// arrives while the agent is busy, what a restart does with what a dead process had
// claimed, and what a stop does to the message being processed.

// inboxProbe is a handler a test drives: it records what it was given, can hold a
// message open until the test releases it, and answers with the message it read.
type inboxProbe struct {
	mu      sync.Mutex
	seen    []string
	started chan string
	release chan struct{}
}

func (p *inboxProbe) handle(_ context.Context, _ context.CancelFunc, _ *Agent, msg AgentMessage) (TurnResult, error) {
	p.mu.Lock()
	p.seen = append(p.seen, msg.Content)
	p.mu.Unlock()
	if p.started != nil {
		p.started <- msg.Content
	}
	if p.release != nil {
		<-p.release
	}
	return TurnResult{Text: "answer:" + msg.Content}, nil
}

func (p *inboxProbe) processed() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

// inboxStore opens a store and an agent row to queue messages for.
func inboxStore(t *testing.T) (rawStore, int64, string) {
	t.Helper()
	store, err := openStore(t, filepath.Join(t.TempDir(), "inbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	agent := &Agent{State: "idle", Lifecycle: AgentLifecyclePersistent}
	if err := store.UpsertAgent(agent); err != nil {
		t.Fatal(err)
	}
	return store, agent.ID, agent.Name
}

// waitForMessageStatus waits until the message has the status a test expects, so no
// test depends on how fast a consumer goroutine is.
func waitForMessageStatus(t *testing.T, store rawStore, agentID, id int64, want AgentMessageStatus) AgentMessage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		messages, err := store.ListAgentMessages(agentID, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, msg := range messages {
			if msg.ID == id && msg.Status == want {
				return msg
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("message %d never reached status %q", id, want)
	return AgentMessage{}
}

// TestMessagesAreProcessedInTheOrderTheyArrived: the queue is the order, whoever
// sent the message.
func TestMessagesAreProcessedInTheOrderTheyArrived(t *testing.T) {
	store, agentID, name := inboxStore(t)
	probe := &inboxProbe{}
	inbox := NewInbox(store, probe.handle, nil)
	agent := &Agent{ID: agentID, Name: name}

	for _, msg := range []AgentMessage{
		{Sender: MessageSenderUser, Kind: MessageKindInstruction, Content: "first"},
		{Sender: MessageSenderAgent, SenderID: "agent-10000", Kind: MessageKindDelegation, Content: "second"},
		{Sender: MessageSenderSystem, SenderID: "runtime", Kind: MessageKindStop, Content: "third"},
	} {
		if _, err := inbox.Enqueue(agent, msg); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := store.ListAgentMessages(agentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	waitForMessageStatus(t, store, agentID, messages[len(messages)-1].ID, MessageStatusDone)

	want := []string{"first", "second", "third"}
	if got := probe.processed(); len(got) != len(want) {
		t.Fatalf("processed %v, want %v", got, want)
	}
	for i, content := range want {
		if got := probe.processed()[i]; got != content {
			t.Fatalf("processed[%d]=%q, want %q (in the order they arrived)", i, got, content)
		}
	}
}

// TestAMessageArrivingWhileTheAgentIsBusyWaitsItsTurn: an instruction that arrives
// while the agent is working is accepted and queued behind what it is doing — which
// is what makes instructions continuously acceptable.
func TestAMessageArrivingWhileTheAgentIsBusyWaitsItsTurn(t *testing.T) {
	store, agentID, name := inboxStore(t)
	probe := &inboxProbe{started: make(chan string, 4), release: make(chan struct{})}
	inbox := NewInbox(store, probe.handle, nil)
	agent := &Agent{ID: agentID, Name: name}

	first, err := inbox.Enqueue(agent, AgentMessage{Sender: MessageSenderUser, Kind: MessageKindInstruction, Content: "working"})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-probe.started; got != "working" {
		t.Fatalf("the agent picked up %q, want %q", got, "working")
	}
	second, err := inbox.Enqueue(agent, AgentMessage{Sender: MessageSenderUser, Kind: MessageKindInstruction, Content: "while you are at it"})
	if err != nil {
		t.Fatal(err)
	}
	messages, err := store.ListAgentMessages(agentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("inbox=%d messages, want both", len(messages))
	}
	if messages[1].Status != MessageStatusQueued {
		t.Fatalf("the second message is %q while the agent is busy, want queued", messages[1].Status)
	}
	if ahead := inbox.Ahead(agent, first); ahead != 0 {
		t.Fatalf("Ahead(first)=%d, want 0 (nothing is in front of it: the agent is on it)", ahead)
	}
	if ahead := inbox.Ahead(agent, second); ahead != 1 {
		t.Fatalf("Ahead(second)=%d, want 1 (the message being processed is in front of it)", ahead)
	}

	close(probe.release)
	waitForMessageStatus(t, store, agentID, second, MessageStatusDone)
	if got := probe.processed(); len(got) != 2 || got[0] != "working" || got[1] != "while you are at it" {
		t.Fatalf("processed %v, want both messages in order", got)
	}
}

// TestANewProcessTakesBackWhatTheLastOneClaimed: a message a process claimed and
// died on is still the agent's to process — the queue survives the restart, the
// same way the agent does.
func TestANewProcessTakesBackWhatTheLastOneClaimed(t *testing.T) {
	store, agentID, name := inboxStore(t)
	if _, err := store.EnqueueMessage(AgentMessage{
		AgentID: agentID, Sender: MessageSenderUser, Kind: MessageKindInstruction, Content: "left behind",
	}); err != nil {
		t.Fatal(err)
	}
	claimed, found, err := store.ClaimNextMessage(agentID)
	if err != nil || !found || claimed.Status != MessageStatusRunning {
		t.Fatalf("claim=%+v found=%v err=%v, want a running message", claimed, found, err)
	}

	// The next process: a fresh inbox, and a message arriving starts its consumer.
	probe := &inboxProbe{started: make(chan string, 4)}
	inbox := NewInbox(store, probe.handle, nil)
	agent := &Agent{ID: agentID, Name: name}
	id, err := inbox.Enqueue(agent, AgentMessage{Sender: MessageSenderUser, Kind: MessageKindInstruction, Content: "after the restart"})
	if err != nil {
		t.Fatal(err)
	}
	waitForMessageStatus(t, store, agentID, id, MessageStatusDone)

	got := probe.processed()
	want := []string{"left behind", "after the restart"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("processed %v, want %v (the reclaimed message first)", got, want)
	}
}

// TestSendWaitsForTheMessageItQueued: another agent's message is answered to
// whoever sent it, while still being a row in the agent's inbox.
func TestSendWaitsForTheMessageItQueued(t *testing.T) {
	store, agentID, name := inboxStore(t)
	probe := &inboxProbe{}
	inbox := NewInbox(store, probe.handle, nil)
	agent := &Agent{ID: agentID, Name: name}

	res, err := inbox.Send(context.Background(), agent, AgentMessage{
		TaskID: "task-1", Sender: MessageSenderAgent, SenderID: "agent-10000",
		Kind: MessageKindDelegation, Content: "rename the events",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "answer:rename the events" {
		t.Fatalf("result=%q, want the handler's answer", res.Text)
	}
	messages, err := store.ListAgentMessages(agentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("inbox=%d messages, want the delegated one", len(messages))
	}
	msg := messages[0]
	if msg.Sender != MessageSenderAgent || msg.SenderID != "agent-10000" || msg.Kind != MessageKindDelegation {
		t.Fatalf("stored message=%+v, want the delegating agent as its sender", msg)
	}
	if msg.Status != MessageStatusDone || msg.TaskID != "task-1" {
		t.Fatalf("stored message=%+v, want done and about task-1", msg)
	}
}

// TestStopEndsTheMessageBeingProcessed: a stop cancels the message the agent is on,
// and it is recorded as stopped.
func TestStopEndsTheMessageBeingProcessed(t *testing.T) {
	store, agentID, name := inboxStore(t)
	started := make(chan struct{})
	handle := func(ctx context.Context, _ context.CancelFunc, _ *Agent, _ AgentMessage) (TurnResult, error) {
		close(started)
		<-ctx.Done()
		return TurnResult{}, ctx.Err()
	}
	inbox := NewInbox(store, handle, nil)
	agent := &Agent{ID: agentID, Name: name}

	id, err := inbox.Enqueue(agent, AgentMessage{Sender: MessageSenderUser, Kind: MessageKindInstruction, Content: "long job"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if !inbox.Stop(agent) {
		t.Fatal("Stop found nothing to cancel while a message was being processed")
	}
	msg := waitForMessageStatus(t, store, agentID, id, MessageStatusStopped)
	if msg.Error == "" {
		t.Fatal("a stopped message records why it stopped")
	}
}

// TestTheInboxIsDryWhenTheQueueEmpties: the owner is told when the agent has
// nothing left, which is when it may let the agent go — and told once.
// TestAFailedMessageIsRecordedAsFailed: a message that failed without being stopped
// says so — which is how a failure is told from a stop in the inbox.
func TestAFailedMessageIsRecordedAsFailed(t *testing.T) {
	store, agentID, name := inboxStore(t)
	boom := errors.New("provider is down")
	handle := func(context.Context, context.CancelFunc, *Agent, AgentMessage) (TurnResult, error) {
		return TurnResult{}, boom
	}
	inbox := NewInbox(store, handle, nil)
	agent := &Agent{ID: agentID, Name: name}

	id, err := inbox.Enqueue(agent, AgentMessage{Sender: MessageSenderUser, Kind: MessageKindInstruction, Content: "one"})
	if err != nil {
		t.Fatal(err)
	}
	msg := waitForMessageStatus(t, store, agentID, id, MessageStatusFailed)
	if msg.Error != boom.Error() {
		t.Fatalf("error=%q, want %q", msg.Error, boom.Error())
	}
}

func TestTheInboxIsDryWhenTheQueueEmpties(t *testing.T) {
	store, agentID, name := inboxStore(t)
	probe := &inboxProbe{}
	drained := make(chan string, 4)
	inbox := NewInbox(store, probe.handle, func(agent *Agent) { drained <- agent.Name })
	agent := &Agent{ID: agentID, Name: name}

	if _, err := inbox.Enqueue(agent, AgentMessage{Sender: MessageSenderUser, Kind: MessageKindInstruction, Content: "one"}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-drained:
		if got != name {
			t.Fatalf("drained=%q, want %q", got, name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the inbox never reported that it ran dry")
	}
}
