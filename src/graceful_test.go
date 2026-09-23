package autonomy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A graceful restart, end to end at the service's own level: an announced restart stops
// starting new runs, an instruction that arrives meanwhile is accepted and held (a row,
// not a run), the status endpoint keeps saying a restart is safe — and when the drain
// ends without one, the held instruction runs. src/graceful.go is the story; this is the
// proof that the pieces are wired to each other.
func TestRestartNotifyHoldsNewInstructionsUntilTheDrainEnds(t *testing.T) {
	store, err := openStore(t, filepath.Join(t.TempDir(), "graceful.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	t.Cleanup(func() { _store = prev })
	_store = store
	t.Setenv("AUTONOMY_REASONER", "local")
	t.Setenv("AUTONOMY_MAX_STEPS", "1")

	auto := &Autonomy{
		AgentFactory: NewAgentFactory(),
		Runtime:      NewRuntime(NewAgentFactory()),
		Store:        store,
		MaxSteps:     1,
	}
	defer auto.Drain.stop()
	// Nobody restarts this test's runtime: the drain waits for the test, not for a
	// timer (the timeout's own test is below).
	auto.restartDrain().timeout = 0

	// A restart is announced. Nothing is in flight, so the answer already says the
	// platform may go ahead — and it says who announced it.
	st := auto.RestartNotify(RestartNotice{
		ServiceID: "autonomy", RequestID: "deploy-1", Deployment: "deployment-abc12345",
		Version: "abc12345", Message: "deployment service will restart this runtime after graceful wait",
	})
	if !st.Draining || !st.CanRestart || !st.CanDeploy || !st.Ready || st.Running != 0 || st.Held != 0 {
		t.Fatalf("status after notify = %+v", st)
	}
	if st.Notice == nil || st.Notice.RequestID != "deploy-1" || st.NotifiedAt == nil || st.NotifiedAt.IsZero() {
		t.Fatalf("the announced notice was not kept: %+v", st)
	}

	// An instruction that arrives while draining is accepted and *not* started.
	accepted, err := auto.AcceptTask(AcceptTaskRequest{Description: "held while restarting", Domain: "server"})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if accepted.AgentID == 0 || accepted.MessageID == 0 {
		t.Fatalf("accepted = %+v", accepted)
	}
	st = auto.RestartStatus()
	if st.Running != 0 || st.Held != 1 || !st.CanRestart || !st.Draining {
		t.Fatalf("status while holding = %+v", st)
	}
	if !strings.Contains(st.Reason, "held") {
		t.Fatalf("reason does not mention the held instruction: %q", st.Reason)
	}
	messages, err := store.ListAgentMessages(accepted.AgentID, 0)
	if err != nil || len(messages) != 1 || messages[0].Status != MessageStatusQueued {
		t.Fatalf("the held instruction is not one queued row: %+v (err=%v)", messages, err)
	}
	task, err := store.GetTask(accepted.TaskID)
	if err != nil || task == nil || task.Status != TaskStatusPending {
		t.Fatalf("task = %+v (err=%v), want pending: a held instruction is not a started run", task, err)
	}

	// The drain ends without a restart (the platform's deploy died, or this very
	// process came back): the held instruction is the agent's to process again.
	auto.Drain.finish("test: no restart came")
	if st := auto.RestartStatus(); st.Draining || st.Held != 0 {
		t.Fatalf("status after the drain ended = %+v", st)
	}
	waitForAnyMessageStatus(t, store, accepted.AgentID, MessageStatusDone, MessageStatusFailed)
}

// The drain is bounded: a deploy that dies after the notice must not leave the service
// holding every new instruction forever. Nothing here restarts the process, so the
// safety timeout is what ends the drain — and the instruction it held starts.
func TestDrainTimeoutResumesOnItsOwn(t *testing.T) {
	store, err := openStore(t, filepath.Join(t.TempDir(), "timeout.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	t.Cleanup(func() { _store = prev })
	_store = store
	t.Setenv("AUTONOMY_REASONER", "local")
	t.Setenv("AUTONOMY_MAX_STEPS", "1")

	auto := &Autonomy{
		AgentFactory: NewAgentFactory(),
		Runtime:      NewRuntime(NewAgentFactory()),
		Store:        store,
		MaxSteps:     1,
	}
	defer auto.Drain.stop()
	auto.restartDrain().timeout = 50 * time.Millisecond

	if st := auto.RestartNotify(RestartNotice{RequestID: "deploy-timeout"}); !st.Draining {
		t.Fatalf("not draining after notify: %+v", st)
	}
	if _, err := auto.AcceptTask(AcceptTaskRequest{Description: "held, then let go", Domain: "server"}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if st := auto.RestartStatus(); st.Held != 1 {
		t.Fatalf("the instruction was not held: %+v", st)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && auto.RestartStatus().Draining {
		time.Sleep(10 * time.Millisecond)
	}
	if st := auto.RestartStatus(); st.Draining {
		t.Fatalf("the drain did not expire: %+v", st)
	}
}

// The two endpoints the deployment platform talks to: one notice before it stops this
// service, one poll while it waits. An empty body or a missing requestId is a mistake
// (the platform always sends one), and a server with no runtime answers as one.
func TestRestartEndpointsAreTheDeploymentContract(t *testing.T) {
	store, err := openStore(t, filepath.Join(t.TempDir(), "endpoints.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	t.Cleanup(func() { _store = prev })
	_store = store

	auto := &Autonomy{AgentFactory: NewAgentFactory(), Runtime: NewRuntime(NewAgentFactory()), Store: store}
	defer auto.Drain.stop()
	auto.restartDrain().timeout = 0
	srv := NewHTTPServer(auto)

	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/ops/restart-notify", strings.NewReader(body)))
		return rec
	}
	if rec := post(""); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty body: status=%d body=%s, want 400 (requestId is required)", rec.Code, rec.Body.String())
	}
	if rec := post("not json"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := post(`{"serviceId":"autonomy"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("no requestId: status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec := post(`{"serviceId":"autonomy","requestId":"deploy-9","deployment":"deployment-9","message":"restarting"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("notify: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var notified RestartStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &notified); err != nil {
		t.Fatal(err)
	}
	if !notified.Draining || !notified.CanRestart {
		t.Fatalf("notify answer = %+v", notified)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ops/restart-status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var polled map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &polled); err != nil {
		t.Fatal(err)
	}
	// The platform reads canRestart; the aliases are the other convention's names, and
	// a reader that learned either one has to find it.
	for _, field := range []string{"canRestart", "canDeploy", "ready", "draining", "running", "reason"} {
		if _, ok := polled[field]; !ok {
			t.Fatalf("status answer has no %q: %s", field, rec.Body.String())
		}
	}

	// A server with no runtime behind it says so instead of panicking.
	bare := NewHTTPServer(nil)
	rec = httptest.NewRecorder()
	bare.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ops/restart-status", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status without a runtime: status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	bare.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/ops/restart-notify", strings.NewReader(`{"requestId":"x"}`)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("notify without a runtime: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// The queue's own side (src/inbox.go): while the runtime drains, an instruction that
// arrives is held — and so is one the consumer has already taken off the queue when the
// drain starts, which is the harder half. The run in flight finishes; the loop behind it
// must not start the next one, and the agent waits to be told the drain is over.
func TestInboxHoldsInstructionsWhileTheRuntimeDrains(t *testing.T) {
	store, err := openStore(t, filepath.Join(t.TempDir(), "inbox-hold.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prev := _store
	t.Cleanup(func() { _store = prev })
	_store = store

	agent := NewAgentFactory().Create(&Task{ID: "task-held", Description: "d"})
	var paused atomic.Bool
	started := make(chan string, 8)
	release := make(chan struct{})
	inbox := NewInbox(store, func(_ context.Context, _ context.CancelFunc, _ *Agent, msg AgentMessage) (TurnResult, error) {
		started <- msg.Content
		if msg.Content == "first" {
			<-release // the run in flight: it is what the drain waits for
		}
		return TurnResult{}, nil
	}, nil)
	inbox.holdWhile(func() bool { return paused.Load() })

	enqueue := func(content string) {
		t.Helper()
		if _, err := inbox.Enqueue(agent, AgentMessage{
			TaskID: "task-held", Sender: MessageSenderUser, SenderID: "u",
			Kind: MessageKindInstruction, Content: content,
		}); err != nil {
			t.Fatalf("enqueue %q: %v", content, err)
		}
	}

	enqueue("first")
	select {
	case got := <-started:
		if got != "first" {
			t.Fatalf("started %q, want first", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the first instruction never started")
	}

	// The restart is announced while that run is in flight, and a second instruction
	// arrives: it must not start, neither when it is enqueued nor when the run ahead of
	// it finishes and the consumer goes looking for more work.
	paused.Store(true)
	enqueue("second")
	close(release)
	select {
	case got := <-started:
		t.Fatalf("a run started while draining: %q", got)
	case <-time.After(300 * time.Millisecond):
	}
	if held := inbox.heldAgents(); held != 1 {
		t.Fatalf("held agents = %d, want 1", held)
	}
	queued, err := store.CountQueuedMessages(agent.ID)
	if err != nil || queued != 1 {
		t.Fatalf("queued = %d (err=%v), want the held instruction still queued", queued, err)
	}

	// The drain ends without a restart: the held instruction is started again.
	paused.Store(false)
	if n := inbox.resumePaused(); n != 1 {
		t.Fatalf("resumePaused = %d, want 1", n)
	}
	select {
	case got := <-started:
		if got != "second" {
			t.Fatalf("started %q, want second", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the held instruction never started after the drain ended")
	}
	waitForAnyMessageStatus(t, store, agent.ID, MessageStatusDone, MessageStatusFailed)
	if held := inbox.heldAgents(); held != 0 {
		t.Fatalf("held agents after the resume = %d, want 0", held)
	}
}

// waitForAnyMessageStatus waits until the agent has a message that ended in one of the
// given statuses: for a test that only cares that the run the queue started finished.
func waitForAnyMessageStatus(t *testing.T, store Store, agentID int64, want ...AgentMessageStatus) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var messages []AgentMessage
	for time.Now().Before(deadline) {
		messages = nil
		var err error
		messages, err = store.ListAgentMessages(agentID, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, msg := range messages {
			for _, status := range want {
				if msg.Status == status {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no message reached %v: %+v", want, messages)
}
