package autonomy

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// Inbox is the runtime's side of the per-agent message queue: it owns the
// goroutine that drains one agent's inbox — one message at a time, in the order
// the messages arrived — and enqueueing is the only way a message reaches an
// agent (src/message.go has the senders and the kinds).
//
// A message is a row (InboxStore), not memory, so an inbox survives a restart:
// the next process takes back whatever a dead one had claimed and carries on,
// the same way it resumes the agent itself (src/agent_resume.go).
//
// One consumer per busy agent, started on demand and gone when the inbox runs
// dry: an idle agent holds no goroutine, and an agent is busy with exactly one
// message at a time — which is what "the agent processes its messages in order"
// means in code.
type Inbox struct {
	store InboxStore
	// handle is what one message is processed by: the owner's own loop, so the
	// queue itself knows nothing about decision cycles or prompts.
	handle MessageHandler
	// drained is told when an agent's inbox has run dry, so the owner can let the
	// agent go (its provider sessions) until the next message arrives.
	drained func(agent *Agent)

	mu      sync.Mutex
	running map[string]bool               // agent name → a consumer is draining
	reclaim map[string]bool               // agent name → claims of dead processes taken back
	gen     map[string]int                // agent name → bumped by every enqueue
	current map[string]context.CancelFunc // agent name → cancel of the message being processed
	waiters map[int64]*messageWaiter      // message id → the caller waiting for it
}

// MessageHandler processes one message: the plane the queue hands work to. cancel
// stops the message being processed (a stop, a cancelled caller) and ctx is the
// message's own — a queued message gets a fresh one, a delegated one inherits its
// caller's, so a capability that gives up also gives up the work it asked for.
type MessageHandler func(ctx context.Context, cancel context.CancelFunc, agent *Agent, msg AgentMessage) (TurnResult, error)

// messageWaiter is one caller waiting for one message's result — in-process only:
// a wait nobody holds (the process died) is nobody's, and the message is still
// processed when the queue is drained again.
type messageWaiter struct {
	ctx context.Context
	ch  chan *messageOutcome
}

type messageOutcome struct {
	result TurnResult
	err    error
}

// NewInbox builds the inbox of one runtime. handle and drained are the owner's
// (see MessageHandler); store is where the messages wait.
func NewInbox(store InboxStore, handle MessageHandler, drained func(agent *Agent)) *Inbox {
	return &Inbox{
		store:   store,
		handle:  handle,
		drained: drained,
		running: map[string]bool{},
		reclaim: map[string]bool{},
		gen:     map[string]int{},
		current: map[string]context.CancelFunc{},
		waiters: map[int64]*messageWaiter{},
	}
}

// Enqueue adds one message to an agent's inbox and makes sure that agent has a
// consumer. It returns the message's id, which is its place in the queue.
func (i *Inbox) Enqueue(agent *Agent, msg AgentMessage) (int64, error) {
	if i == nil || i.store == nil {
		return 0, fmt.Errorf("no inbox")
	}
	if agent == nil || agent.ID == 0 {
		return 0, fmt.Errorf("enqueue message: unknown agent")
	}
	msg.AgentID = agent.ID
	if msg.Status == "" {
		msg.Status = MessageStatusQueued
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}
	id, err := i.store.EnqueueMessage(msg)
	if err != nil {
		return 0, err
	}
	i.mu.Lock()
	i.gen[agent.Name]++
	i.mu.Unlock()
	i.start(agent)
	return id, nil
}

// Send puts one message in an agent's inbox and waits for the agent to process
// it: this is what a capability's prompt to a worker is (sender agent, kind
// delegation) — the worker's own consumer runs the turn, and the capability sees
// it as an ordinary call that returns when the turn is done. The waiter is
// registered before the message is enqueued, so a consumer that is already
// draining cannot process the message before anyone is waiting for it.
func (i *Inbox) Send(ctx context.Context, agent *Agent, msg AgentMessage) (TurnResult, error) {
	if i == nil || i.store == nil {
		return TurnResult{}, fmt.Errorf("no inbox")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if agent == nil || agent.ID == 0 {
		return TurnResult{}, fmt.Errorf("send message: unknown agent")
	}
	msg.AgentID = agent.ID
	if msg.Status == "" {
		msg.Status = MessageStatusQueued
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}
	waiter := &messageWaiter{ctx: ctx, ch: make(chan *messageOutcome, 1)}
	i.mu.Lock()
	id, err := i.store.EnqueueMessage(msg)
	if err != nil {
		i.mu.Unlock()
		return TurnResult{}, err
	}
	i.waiters[id] = waiter
	i.gen[agent.Name]++
	i.mu.Unlock()
	i.start(agent)

	select {
	case out := <-waiter.ch:
		return out.result, out.err
	case <-ctx.Done():
		i.dropWaiter(id)
		return TurnResult{}, ctx.Err()
	}
}

// Stop cancels the message an agent is processing right now, and reports whether
// there was one. The cancelled message ends as stopped, and the messages behind
// it stay in the queue: they were accepted, and a queue does not lose what it
// holds.
func (i *Inbox) Stop(agent *Agent) bool {
	if i == nil || agent == nil {
		return false
	}
	i.mu.Lock()
	cancel := i.current[agent.Name]
	i.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// Queued is how many messages the agent still has behind the one with this id
// (queued or being processed): 0 means that message is what the agent is doing or
// about to do, and n means n messages came in before the agent gets to it.
func (i *Inbox) Queued(agent *Agent, afterID int64) int {
	if i == nil || i.store == nil || agent == nil {
		return 0
	}
	messages, err := i.store.ListAgentMessages(agent.ID, 200)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] %s inbox: %v\n", agent.Name, err)
		return 0
	}
	queued := 0
	for _, msg := range messages {
		if msg.ID > afterID && msg.Status != MessageStatusDone && msg.Status != MessageStatusFailed && msg.Status != MessageStatusStopped {
			queued++
		}
	}
	return queued
}

// start makes sure the agent has a consumer. The first message of an idle agent
// starts one; while one is running this is a no-op, and the message it just
// enqueued is picked up by the drain loop (or by the re-check in finished).
func (i *Inbox) start(agent *Agent) {
	if i == nil || agent == nil {
		return
	}
	i.mu.Lock()
	if i.running[agent.Name] {
		i.mu.Unlock()
		return
	}
	i.running[agent.Name] = true
	gen := i.gen[agent.Name]
	i.mu.Unlock()
	go i.drain(agent, gen)
}

// drain is one agent's consumer: claim the next message, process it, repeat, and
// leave when there is nothing left. It is this loop — one per agent, never two —
// that makes the order messages are processed in the order they arrived.
func (i *Inbox) drain(agent *Agent, gen int) {
	if i.takeReclaim(agent) {
		// Whatever a process that is gone had claimed is still the agent's to
		// process: a restart resumes the queue, not just the agent.
		if err := i.store.RequeueRunningMessages(agent.ID); err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] %s inbox: %v\n", agent.Name, err)
		}
	}
	for {
		msg, found, err := i.store.ClaimNextMessage(agent.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] %s inbox: %v\n", agent.Name, err)
			i.endDrain(agent, gen)
			return
		}
		if !found {
			i.endDrain(agent, gen)
			return
		}
		waiter, outcome := i.process(agent, msg)
		if waiter == nil {
			continue
		}
		if i.queueIsDry(agent) {
			// Nothing is behind this message, so the agent is let go (endDrain) before
			// the caller is released: a synchronous caller — Autonomy.Run — must not
			// return while a goroutine is still writing the agent's row for a run the
			// caller is about to inspect, or to close the store under.
			i.endDrain(agent, gen)
			waiter.ch <- outcome
			return
		}
		waiter.ch <- outcome
	}
}

// queueIsDry reports whether the agent has nothing waiting. A read that fails is
// treated as dry: the drain still ends, and a message that is there is picked up by
// the consumer the next enqueue starts (src/agent.go — the re-check in endDrain).
func (i *Inbox) queueIsDry(agent *Agent) bool {
	queued, err := i.store.CountQueuedMessages(agent.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] %s inbox: %v\n", agent.Name, err)
		return true
	}
	return queued == 0
}

// endDrain ends one consumer: if a message landed while the last read said
// "empty", it starts a new consumer rather than leaving the message for a consumer
// that has already left. Otherwise the owner is told the inbox is dry, which is
// when the agent may be let go.
func (i *Inbox) endDrain(agent *Agent, gen int) {
	i.mu.Lock()
	i.running[agent.Name] = false
	arrived := i.gen[agent.Name] != gen
	i.mu.Unlock()
	if arrived {
		i.start(agent)
		return
	}
	if i.drained != nil {
		i.drained(agent)
	}
}

// process runs one message, records how it ended, and returns who was waiting for
// it (nil when nobody was: a queued instruction has no caller). The message's own
// context is the waiter's when there is one — a delegated prompt inherits the
// capability's cancellation — else a fresh one that only a stop can cancel.
func (i *Inbox) process(agent *Agent, msg AgentMessage) (*messageWaiter, *messageOutcome) {
	waiter := i.takeWaiter(msg.ID)
	base := context.Background()
	if waiter != nil && waiter.ctx != nil {
		base = waiter.ctx
	}
	ctx, cancel := context.WithCancel(base)
	i.mu.Lock()
	i.current[agent.Name] = cancel
	i.mu.Unlock()

	result, err := i.handle(ctx, cancel, agent, msg)
	// Read before cancel(): whether this message was stopped is what it was when the
	// handler returned, not what this cleanup makes it look like afterwards.
	cancelled := ctx.Err() != nil

	i.mu.Lock()
	delete(i.current, agent.Name)
	i.mu.Unlock()
	cancel()

	status := MessageStatusDone
	errText := ""
	switch {
	case err != nil && cancelled:
		status, errText = MessageStatusStopped, err.Error()
	case err != nil:
		status, errText = MessageStatusFailed, err.Error()
	}
	if ferr := i.store.FinishAgentMessage(msg.ID, status, errText); ferr != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] %s inbox: finish message %d: %v\n", agent.Name, msg.ID, ferr)
	}
	if waiter == nil {
		return nil, nil
	}
	return waiter, &messageOutcome{result: result, err: err}
}

func (i *Inbox) takeWaiter(id int64) *messageWaiter {
	i.mu.Lock()
	defer i.mu.Unlock()
	waiter := i.waiters[id]
	delete(i.waiters, id)
	return waiter
}

func (i *Inbox) dropWaiter(id int64) {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.waiters, id)
}

func (i *Inbox) takeReclaim(agent *Agent) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.reclaim[agent.Name] {
		return false
	}
	i.reclaim[agent.Name] = true
	return true
}
