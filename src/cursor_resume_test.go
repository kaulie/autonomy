package autonomy

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	sdkv1 "github.com/kaulie/autonomy/src/cursorsdk/gen/sdk/v1"
	"github.com/kaulie/autonomy/src/cursorsdk/gen/sdk/v1/sdkv1connect"
)

// resumeBridge answers the session calls a resumed task makes and records which one it
// was asked for: the point of these tests is that a session the agent row names is
// *re-attached*, and only one the provider no longer has becomes a new session.
type resumeBridge struct {
	sdkv1connect.UnimplementedSdkAgentServiceHandler

	mu     sync.Mutex
	calls  []string // "resume:<id>" or "create"
	gone   bool     // ResumeAgent answers not_found: the session expired while we were down
	nextID string   // the id a created agent gets
}

func (b *resumeBridge) ResumeAgent(_ context.Context, req *connect.Request[sdkv1.ResumeAgentRequest]) (*connect.Response[sdkv1.ResumeAgentResponse], error) {
	b.mu.Lock()
	b.calls = append(b.calls, "resume:"+req.Msg.GetAgentId())
	gone := b.gone
	b.mu.Unlock()
	if gone {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such agent"))
	}
	return connect.NewResponse(&sdkv1.ResumeAgentResponse{AgentId: req.Msg.GetAgentId()}), nil
}

func (b *resumeBridge) CreateAgent(context.Context, *connect.Request[sdkv1.CreateAgentRequest]) (*connect.Response[sdkv1.CreateAgentResponse], error) {
	b.mu.Lock()
	b.calls = append(b.calls, "create")
	id := b.nextID
	if id == "" {
		id = "cursor-agent-fresh"
	}
	b.mu.Unlock()
	return connect.NewResponse(&sdkv1.CreateAgentResponse{AgentId: id}), nil
}

func (b *resumeBridge) callLog() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.calls...)
}

// cursorResumeSetup is the state a restart leaves for the cursor backend: a task whose
// agent row names a Cursor agent recorded before the process went away.
func cursorResumeSetup(t *testing.T, store rawStore, bridge *resumeBridge, taskID string) (*Task, *Agent) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(sdkv1connect.NewSdkAgentServiceHandler(bridge))
	mux.Handle(sdkv1connect.NewSdkBridgeControlServiceHandler(liveBridge{}))
	pointSharedCursorClientAt(t, mux)

	task, stored := pairedTask(t, store, taskID)
	stored.Backend = AgentBackendCursor
	stored.LLMProvider = LLMProviderCursor
	stored.Model = "composer-2"
	stored.LLMAgentID = "cursor-agent-before"
	if err := store.UpsertAgent(stored); err != nil {
		t.Fatal(err)
	}
	return task, stored
}

// TestAResumedCursorTaskReattachesTheSessionItWasRecordedWith: the session half of a
// restart for the cursor backend. The task's agent row names the Cursor agent it was
// left with, so the next turn re-attaches *that* agent — one ResumeAgent call, no new
// session, and no reasoning frame re-sent, because the session it continues already
// holds the frame.
func TestAResumedCursorTaskReattachesTheSessionItWasRecordedWith(t *testing.T) {
	store := resumeTestStore(t)
	bridge := &resumeBridge{}
	task, _ := cursorResumeSetup(t, store, bridge, "t-cursor-resume")

	f := NewAgentFactory()
	rt := &Autonomy{AgentFactory: f, Runtime: NewRuntime(f), Store: store}
	agent, err := rt.resumeAgentForTask(task)
	if err != nil {
		t.Fatalf("resumeAgentForTask: %v", err)
	}
	if agent.LLMAgentID != "cursor-agent-before" {
		t.Fatalf("LLMAgentID=%q, want the session the row names carried into the handle", agent.LLMAgentID)
	}

	resumed, err := agent.resumeCursorSession(context.Background())
	if err != nil {
		t.Fatalf("resumeCursorSession: %v", err)
	}
	if !resumed {
		t.Fatal("a re-attached session must report itself as resumed")
	}
	if calls := bridge.callLog(); len(calls) != 1 || calls[0] != "resume:cursor-agent-before" {
		t.Fatalf("bridge calls=%v, want exactly one ResumeAgent on the recorded id", calls)
	}
	if agent.LLMAgentID != "cursor-agent-before" {
		t.Fatalf("LLMAgentID=%q, want the re-attached session kept", agent.LLMAgentID)
	}
	if !agent.llmFrameSent {
		t.Fatal("a resumed session already holds the reasoning frame: it must not be re-sent")
	}
}

// TestASessionTheProviderNoLongerHasBecomesANewOne: a session that expired while the
// runtime was down is a reason to start talking again, not to give up the task — the row
// stops naming it, a fresh agent is opened, and the task's own record (the briefing) is
// still what the next cycle plans from.
func TestASessionTheProviderNoLongerHasBecomesANewOne(t *testing.T) {
	store := resumeTestStore(t)
	bridge := &resumeBridge{gone: true, nextID: "cursor-agent-fresh"}
	task, _ := cursorResumeSetup(t, store, bridge, "t-cursor-expired")
	// A round the task already ran: the record that has to survive a lost session.
	if _, _, err := saveExecutionPlan(ExecutionPlan{
		TaskID: task.ID, AgentID: task.AgentID, Cycle: 1, DecisionType: "plan",
		Reason: "deliver the dashboard as a pull request", StepCount: 1, CreatedAt: time.Now(),
	}, []ExecutionStepPlan{
		{Idx: 1, Name: "implement", Capability: "code_edit", Input: `{"instruction":"dashboard"}`},
	}); err != nil {
		t.Fatal(err)
	}

	f := NewAgentFactory()
	rt := &Autonomy{AgentFactory: f, Runtime: NewRuntime(f), Store: store}
	agent, err := rt.resumeAgentForTask(task)
	if err != nil {
		t.Fatalf("resumeAgentForTask: %v", err)
	}

	resumed, err := agent.resumeCursorSession(context.Background())
	if err != nil {
		t.Fatalf("resumeCursorSession: %v", err)
	}
	if resumed {
		t.Fatal("a session that had to be created is not a resumed one")
	}
	calls := bridge.callLog()
	if len(calls) != 2 || calls[0] != "resume:cursor-agent-before" || calls[1] != "create" {
		t.Fatalf("bridge calls=%v, want the recorded session tried, then a fresh agent", calls)
	}
	if agent.LLMAgentID != "cursor-agent-fresh" {
		t.Fatalf("LLMAgentID=%q, want the fresh session recorded instead of the gone one", agent.LLMAgentID)
	}
	// The session is gone; the record is not. This is what the continuing cycle plans from.
	brief := rt.taskBriefing(task.ID)
	if brief == nil || len(brief.EarlierRounds) != 1 {
		t.Fatalf("briefing=%+v, want the task's own record after the session was lost", brief)
	}
	if brief.EarlierRounds[0].Reason == "" {
		t.Fatalf("briefing=%+v, want the earlier round's own reason", brief.EarlierRounds[0])
	}
}
