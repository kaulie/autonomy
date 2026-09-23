package autonomy

import "github.com/kaulie/autonomy/src/llmbackend"

import (
	"context"
	"errors"
	"github.com/kaulie/autonomy/src/llmbackend/cursor"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	sdkv1 "github.com/kaulie/autonomy/src/cursorsdk/gen/sdk/v1"
	"github.com/kaulie/autonomy/src/cursorsdk/gen/sdk/v1/sdkv1connect"
	"github.com/kaulie/autonomy/src/llmrun"
)

// wedgedBridge is a bridge that takes a call and never answers it: a CreateAgent
// stuck behind a stalled tunnel, which is what an agent turn loses its run to.
type wedgedBridge struct {
	sdkv1connect.UnimplementedSdkAgentServiceHandler
}

func (wedgedBridge) CreateAgent(ctx context.Context, _ *connect.Request[sdkv1.CreateAgentRequest]) (*connect.Response[sdkv1.CreateAgentResponse], error) {
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second): // never a success: the call is only ever refused
	}
	return nil, connect.NewError(connect.CodeCanceled, context.Canceled)
}

// liveBridge answers the calls that are not the session itself (Ping).
type liveBridge struct {
	sdkv1connect.UnimplementedSdkBridgeControlServiceHandler
}

func (liveBridge) Ping(context.Context, *connect.Request[sdkv1.PingRequest]) (*connect.Response[sdkv1.PingResponse], error) {
	return connect.NewResponse(&sdkv1.PingResponse{}), nil
}

// pointSharedCursorClientAt makes this process's one bridge client attach to an
// in-process bridge for the duration of the test, and restores it afterwards.
func pointSharedCursorClientAt(t *testing.T, handler http.Handler) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv("CURSOR_SDK_BRIDGE_URL", srv.URL)
	t.Setenv("CURSOR_SDK_BRIDGE_TOKEN", "test-token")

	previous := cursor.SwapCursorClient(nil)
	t.Cleanup(func() {
		cursor.SwapCursorClient(previous)
	})
}

// A session that is never opened is reported as what ended the run that needed it.
// The bridge call itself can only see its own context die — "canceled: context
// canceled" — so a run cut for being idle used to be recorded as a bare
// cancellation, three minutes of silence explained by nothing at all. The reason
// the run's context carries is what this reports (see bridgeCallErr).
func TestAttachCursorReportsWhyTheRunEnded(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(sdkv1connect.NewSdkAgentServiceHandler(wedgedBridge{}))
	mux.Handle(sdkv1connect.NewSdkBridgeControlServiceHandler(liveBridge{}))
	pointSharedCursorClientAt(t, mux)

	wd := llmrun.NewIdleWatchdog(context.Background(), 50*time.Millisecond)
	defer wd.Stop()

	agent := NewAgentFactory().NewAgent()
	err := agent.AttachCursor(wd.Context(), "composer-2")
	if err == nil {
		t.Fatal("a bridge that never answered must not read as an attached session")
	}
	if !strings.Contains(err.Error(), "run idle for") {
		t.Fatalf("err=%v, want the idle cut named instead of the transport's words", err)
	}
}

// countingBridge refuses every session call and counts it: what an accept must not
// make. It answers immediately (rather than hanging) so a regression shows up as a
// count, not as a slow test.
type countingBridge struct {
	sdkv1connect.UnimplementedSdkAgentServiceHandler

	opens atomic.Int32
}

func (b *countingBridge) CreateAgent(context.Context, *connect.Request[sdkv1.CreateAgentRequest]) (*connect.Response[sdkv1.CreateAgentResponse], error) {
	b.opens.Add(1)
	return nil, connect.NewError(connect.CodeInternal, errors.New("no session for you"))
}

func (b *countingBridge) ResumeAgent(context.Context, *connect.Request[sdkv1.ResumeAgentRequest]) (*connect.Response[sdkv1.ResumeAgentResponse], error) {
	b.opens.Add(1)
	return nil, connect.NewError(connect.CodeInternal, errors.New("no session for you"))
}

// Accepting an instruction is queueing it, and that must not open the agent's
// provider session: otherwise a bridge that will not answer holds POST /api/tasks —
// and every accept a broadcast fans out, one per agent — for as long as it feels
// like it. That is what happened on task-29: the accept waited on a wedged
// CreateAgent, the CLI's own 60s client timeout fired first ("context deadline
// exceeded while awaiting headers"), and the instruction was never queued at all.
func TestAcceptingAnInstructionOpensNoProviderSession(t *testing.T) {
	store := resumeTestStore(t)
	bridge := &countingBridge{}
	mux := http.NewServeMux()
	mux.Handle(sdkv1connect.NewSdkAgentServiceHandler(bridge))
	mux.Handle(sdkv1connect.NewSdkBridgeControlServiceHandler(liveBridge{}))
	pointSharedCursorClientAt(t, mux)

	// A task this process did not create: its row names an agent, and that agent was
	// left with a Cursor session — both of which the accept used to re-open here.
	task := &Task{ID: "task-restart", Description: "ship it", Status: TaskStatusRunning}
	agent := &Agent{
		State:       "idle",
		Lifecycle:   AgentLifecyclePersistent,
		Backend:     llmbackend.Cursor,
		LLMProvider: llmbackend.ProviderCursor,
		Model:       "composer-2",
		LLMAgentID:  "cursor-session-from-before",
		CurrentTask: task,
	}
	if err := store.UpsertAgent(agent); err != nil {
		t.Fatal(err)
	}
	task.AgentID = agent.ID
	if err := store.UpsertTask(task); err != nil {
		t.Fatal(err)
	}

	f := NewAgentFactory()
	auto := &Autonomy{AgentFactory: f, Runtime: NewRuntime(f), Store: store}

	started := time.Now()
	accepted, got, msg, err := auto.accept(AcceptTaskRequest{ID: task.ID, Description: "and again"})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if accepted.ID != task.ID || got.ID != agent.ID || msg.TaskID != task.ID || msg.Content != "and again" {
		t.Fatalf("accept returned task=%+v agent=%+v message=%+v, want the instruction for %s on agent %d",
			accepted, got, msg, task.ID, agent.ID)
	}
	if n := bridge.opens.Load(); n != 0 {
		t.Fatalf("accepting an instruction asked the bridge to open a session %d times: it must not wait on one", n)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("accept took %s: it is queueing an instruction, not opening a session", elapsed)
	}
}

// A bridge call that failed while the run was still alive keeps its own error: the
// bridge's word about the bridge is the one to keep.
func TestBridgeCallErrKeepsTheBridgesOwnFailure(t *testing.T) {
	own := context.Canceled
	err := bridgeCallErr(context.Background(), own)
	if err != own {
		t.Fatalf("bridgeCallErr=%v, want the error it was given (%v)", err, own)
	}
	if err := bridgeCallErr(nil, own); err != own {
		t.Fatalf("bridgeCallErr with no context=%v, want %v", err, own)
	}
}
