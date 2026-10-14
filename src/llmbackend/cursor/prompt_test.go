package cursor

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/kaulie/autonomy/src/cursorsdk"
	sdkv1 "github.com/kaulie/autonomy/src/cursorsdk/gen/sdk/v1"
	"github.com/kaulie/autonomy/src/cursorsdk/gen/sdk/v1/sdkv1connect"
	"github.com/kaulie/autonomy/src/llmbackend"
)

// busyThenFree is the provider state after an idle cutoff that never cancelled:
// the first Send is refused, ListRuns still shows the stuck run, and the next
// Send succeeds once that run is cancelled.
type busyThenFree struct {
	sdkv1connect.UnimplementedSdkAgentServiceHandler

	sends atomic.Int32

	mu        sync.Mutex
	runs      []*sdkv1.RunSnapshot
	cancelIDs []string
}

func (f *busyThenFree) CreateAgent(context.Context, *connect.Request[sdkv1.CreateAgentRequest]) (*connect.Response[sdkv1.CreateAgentResponse], error) {
	return connect.NewResponse(&sdkv1.CreateAgentResponse{AgentId: "agent-1"}), nil
}

func (f *busyThenFree) ListRuns(context.Context, *connect.Request[sdkv1.ListRunsRequest]) (*connect.Response[sdkv1.ListRunsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	items := append([]*sdkv1.RunSnapshot(nil), f.runs...)
	return connect.NewResponse(&sdkv1.ListRunsResponse{Items: items}), nil
}

func (f *busyThenFree) CancelRun(_ context.Context, req *connect.Request[sdkv1.CancelRunRequest]) (*connect.Response[sdkv1.CancelRunResponse], error) {
	id := req.Msg.GetRunId()
	f.mu.Lock()
	f.cancelIDs = append(f.cancelIDs, id)
	kept := f.runs[:0]
	for _, run := range f.runs {
		if run.GetRunId() != id {
			kept = append(kept, run)
		}
	}
	f.runs = kept
	f.mu.Unlock()
	return connect.NewResponse(&sdkv1.CancelRunResponse{}), nil
}

func (f *busyThenFree) Send(_ context.Context, _ *connect.Request[sdkv1.SendRequest], stream *connect.ServerStream[sdkv1.RunStreamMessage]) error {
	if f.sends.Add(1) == 1 {
		return connect.NewError(connect.CodeInternal, errors.New("Agent agent-1 already has active run"))
	}
	result := &sdkv1.RunStreamMessage{Envelope: &sdkv1.RunStreamMessage_Result{
		Result: &sdkv1.RunStreamResult{
			Status: sdkv1.RunLifecycleStatus_RUN_LIFECYCLE_STATUS_FINISHED,
			Result: &sdkv1.RunResult{
				RunId:   "run-2",
				AgentId: "agent-1",
				Status:  sdkv1.RunLifecycleStatus_RUN_LIFECYCLE_STATUS_FINISHED,
				Result:  "recovered",
			},
		},
	}}
	if err := stream.Send(result); err != nil {
		return err
	}
	return stream.Send(&sdkv1.RunStreamMessage{Envelope: &sdkv1.RunStreamMessage_Done{Done: &sdkv1.RunStreamDone{}}})
}

type stubHost struct{}

func (stubHost) Facts() llmbackend.Facts                            { return llmbackend.Facts{} }
func (stubHost) SetBackend(llmbackend.Backend, llmbackend.Provider) {}
func (stubHost) SetWorkspace(string)                                {}
func (stubHost) SetModel(string)                                    {}
func (stubHost) SetSessionID(string)                                {}
func (stubHost) SetFrameSent(bool)                                  {}
func (stubHost) Persist()                                           {}

// A turn whose provider still holds the previous run must cancel that run and
// deliver this prompt once, instead of failing the turn and leaving the agent busy.
func TestPromptRetriesOnceAfterClearingActiveRun(t *testing.T) {
	bridge := &busyThenFree{
		runs: []*sdkv1.RunSnapshot{{
			RunId:   "run-stuck",
			AgentId: "agent-1",
			Status:  sdkv1.RunLifecycleStatus_RUN_LIFECYCLE_STATUS_RUNNING,
		}},
	}
	_, handler := sdkv1connect.NewSdkAgentServiceHandler(bridge)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client := cursorsdk.NewClient(cursorsdk.WithEndpoint(srv.URL, "test-token"))
	agent, err := client.Agents().Create(context.Background(), cursorsdk.CreateOptions{Model: "composer-2"})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	session := &cursorSession{host: stubHost{}, agent: agent}

	out, _, err := session.Prompt(context.Background(), "continue the edit", llmbackend.ModeAgent, nil)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if out != "recovered" {
		t.Fatalf("out=%q, want the second send's answer", out)
	}
	if n := bridge.sends.Load(); n != 2 {
		t.Fatalf("sends=%d, want the refused send plus one retry", n)
	}
	bridge.mu.Lock()
	ids := append([]string(nil), bridge.cancelIDs...)
	bridge.mu.Unlock()
	if len(ids) == 0 || ids[0] != "run-stuck" {
		t.Fatalf("CancelRun ids=%v, want the stuck run cleared before the retry", ids)
	}
}
