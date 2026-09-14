package cursorsdk_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/kaulie/autonomy/src/cursorsdk"
	sdkv1 "github.com/kaulie/autonomy/src/cursorsdk/gen/sdk/v1"
	"github.com/kaulie/autonomy/src/cursorsdk/gen/sdk/v1/sdkv1connect"
)

// fakeAgentService is a minimal in-process bridge: only the RPCs used by the
// idle-watchdog tests are implemented, the rest stay unimplemented.
type fakeAgentService struct {
	sdkv1connect.UnimplementedSdkAgentServiceHandler

	onSend     func(ctx context.Context, stream *connect.ServerStream[sdkv1.RunStreamMessage]) error
	waitLiveDB atomic.Int32
}

func (f *fakeAgentService) CreateAgent(context.Context, *connect.Request[sdkv1.CreateAgentRequest]) (*connect.Response[sdkv1.CreateAgentResponse], error) {
	return connect.NewResponse(&sdkv1.CreateAgentResponse{AgentId: "agent-1"}), nil
}

// WaitLiveRun returns a finished result: tests assert it is never reached once a
// run has been cut for being idle.
func (f *fakeAgentService) WaitLiveRun(context.Context, *connect.Request[sdkv1.WaitLiveRunRequest]) (*connect.Response[sdkv1.WaitLiveRunResponse], error) {
	f.waitLiveDB.Add(1)
	return connect.NewResponse(&sdkv1.WaitLiveRunResponse{Result: &sdkv1.RunResult{
		RunId:  "run-1",
		Status: sdkv1.RunLifecycleStatus_RUN_LIFECYCLE_STATUS_FINISHED,
		Result: "recovered-by-waitlive",
	}}), nil
}

func (f *fakeAgentService) Send(ctx context.Context, _ *connect.Request[sdkv1.SendRequest], stream *connect.ServerStream[sdkv1.RunStreamMessage]) error {
	if f.onSend == nil {
		return connect.NewError(connect.CodeInternal, errors.New("no onSend handler"))
	}
	return f.onSend(ctx, stream)
}

func newFakeClient(t *testing.T, svc *fakeAgentService) *cursorsdk.Client {
	t.Helper()
	_, handler := sdkv1connect.NewSdkAgentServiceHandler(svc)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return cursorsdk.NewClient(cursorsdk.WithEndpoint(srv.URL, "test-token"))
}

func statusNudge(runID, message string) *sdkv1.RunStreamMessage {
	payload, _ := structpb.NewStruct(map[string]any{"message": message, "run_id": runID})
	return &sdkv1.RunStreamMessage{Envelope: &sdkv1.RunStreamMessage_SdkMessage{
		SdkMessage: &sdkv1.SdkMessage{Type: "status", Message: payload},
	}}
}

func terminalResult(runID, text string) *sdkv1.RunStreamMessage {
	return &sdkv1.RunStreamMessage{Envelope: &sdkv1.RunStreamMessage_Result{
		Result: &sdkv1.RunStreamResult{
			Status: sdkv1.RunLifecycleStatus_RUN_LIFECYCLE_STATUS_FINISHED,
			Result: &sdkv1.RunResult{
				RunId:   runID,
				AgentId: "agent-1",
				Status:  sdkv1.RunLifecycleStatus_RUN_LIFECYCLE_STATUS_FINISHED,
				Result:  text,
			},
		},
	}}
}

func runDone() *sdkv1.RunStreamMessage {
	return &sdkv1.RunStreamMessage{Envelope: &sdkv1.RunStreamMessage_Done{Done: &sdkv1.RunStreamDone{}}}
}

func startRun(t *testing.T, client *cursorsdk.Client, ctx context.Context) *cursorsdk.Run {
	t.Helper()
	agent, err := client.Agents().Create(context.Background(), cursorsdk.CreateOptions{Model: "composer-2"})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	run, err := agent.Send(ctx, "hi")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	return run
}

// TestRunSurvivesActivityPastIdleGap is the regression test for the old hard
// wall clock: a run that keeps producing events outlives several idle gaps.
func TestRunSurvivesActivityPastIdleGap(t *testing.T) {
	const idle = 500 * time.Millisecond
	svc := &fakeAgentService{
		onSend: func(ctx context.Context, stream *connect.ServerStream[sdkv1.RunStreamMessage]) error {
			for i := 0; i < 12; i++ { // 12×80ms ≈ 2× the idle gap in total
				if err := stream.Send(statusNudge("run-1", "working")); err != nil {
					return err
				}
				time.Sleep(80 * time.Millisecond)
			}
			if err := stream.Send(terminalResult("run-1", "hello")); err != nil {
				return err
			}
			return stream.Send(runDone())
		},
	}
	client := newFakeClient(t, svc)

	wd := cursorsdk.NewIdleWatchdog(context.Background(), idle)
	defer wd.Stop()
	ctx := cursorsdk.WithIdleWatchdog(wd.Context(), wd)

	run := startRun(t, client, ctx)
	res, err := run.WaitStream(ctx, nil)
	if err != nil {
		t.Fatalf("active run was aborted: %v", err)
	}
	if res.Text != "hello" || !res.OK() {
		t.Fatalf("res=%+v, want a finished run with text %q", res, "hello")
	}
}

// TestRunAbortedAfterIdleGap proves the watchdog still cuts a run that goes
// silent, that the cancel reaches the live stream, and that no recovery via
// WaitLiveRun is attempted afterwards.
func TestRunAbortedAfterIdleGap(t *testing.T) {
	const idle = 200 * time.Millisecond
	silent := make(chan struct{})
	svc := &fakeAgentService{
		onSend: func(ctx context.Context, stream *connect.ServerStream[sdkv1.RunStreamMessage]) error {
			// One event, then silence: the run is alive but idle.
			if err := stream.Send(statusNudge("run-1", "working")); err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				close(silent)
				return ctx.Err()
			case <-time.After(10 * time.Second):
				return errors.New("run was never cancelled")
			}
		},
	}
	client := newFakeClient(t, svc)

	wd := cursorsdk.NewIdleWatchdog(context.Background(), idle)
	defer wd.Stop()
	ctx := cursorsdk.WithIdleWatchdog(wd.Context(), wd)

	run := startRun(t, client, ctx)
	started := time.Now()
	res, err := run.WaitStream(ctx, nil)
	elapsed := time.Since(started)

	if err == nil {
		t.Fatalf("silent run was not aborted: res=%+v", res)
	}
	if !strings.Contains(err.Error(), "idle") {
		t.Fatalf("err=%v, want an idle-timeout reason", err)
	}
	if res != nil {
		t.Fatalf("res=%+v, want nil for an aborted run", res)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("abort took %s, expected roughly the %s idle gap", elapsed, idle)
	}
	if n := svc.waitLiveDB.Load(); n != 0 {
		t.Fatalf("WaitLiveRun called %d times; an idle cutoff must not fall back", n)
	}
	select {
	case <-silent:
	case <-time.After(2 * time.Second):
		t.Fatal("provider stream was not torn down by the idle cutoff")
	}
}

// TestRunCollectsTerminalResultRacingIdleCutoff makes sure a cutoff that lands
// after the terminal result does not throw that result away.
func TestRunCollectsTerminalResultRacingIdleCutoff(t *testing.T) {
	const idle = 200 * time.Millisecond
	release := make(chan struct{})
	svc := &fakeAgentService{
		onSend: func(ctx context.Context, stream *connect.ServerStream[sdkv1.RunStreamMessage]) error {
			if err := stream.Send(statusNudge("run-1", "working")); err != nil {
				return err
			}
			if err := stream.Send(terminalResult("run-1", "late")); err != nil {
				return err
			}
			// Hold the stream open past the idle gap instead of closing it, so
			// the cutoff fires while a terminal result is already in hand.
			select {
			case <-ctx.Done():
			case <-release:
			case <-time.After(10 * time.Second):
			}
			return ctx.Err()
		},
	}
	client := newFakeClient(t, svc)

	wd := cursorsdk.NewIdleWatchdog(context.Background(), idle)
	defer wd.Stop()
	ctx := cursorsdk.WithIdleWatchdog(wd.Context(), wd)

	run := startRun(t, client, ctx)
	res, err := run.WaitStream(ctx, nil)
	close(release)
	if err != nil {
		t.Fatalf("terminal result was discarded: %v", err)
	}
	if res == nil || res.Text != "late" {
		t.Fatalf("res=%+v, want the terminal result %q", res, "late")
	}
}
