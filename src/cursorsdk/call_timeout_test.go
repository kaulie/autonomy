package cursorsdk_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/kaulie/autonomy/src/cursorsdk"
	sdkv1 "github.com/kaulie/autonomy/src/cursorsdk/gen/sdk/v1"
	"github.com/kaulie/autonomy/src/cursorsdk/gen/sdk/v1/sdkv1connect"
	"github.com/kaulie/autonomy/src/llmrun"
)

// wedgedBridge is a bridge that accepts a call and never answers it — a
// CreateAgent stuck in a stalled tunnel, which is what a run loses its whole
// budget to when nothing bounds the call.
type wedgedBridge struct {
	sdkv1connect.UnimplementedSdkAgentServiceHandler
}

func (wedgedBridge) CreateAgent(ctx context.Context, _ *connect.Request[sdkv1.CreateAgentRequest]) (*connect.Response[sdkv1.CreateAgentResponse], error) {
	<-ctx.Done()
	return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
}

// fakeBridgeClient is a client on an in-process bridge with its own call timeout.
func fakeBridgeClient(t *testing.T, handler sdkv1connect.SdkAgentServiceHandler, callTimeout time.Duration) *cursorsdk.Client {
	t.Helper()
	_, h := sdkv1connect.NewSdkAgentServiceHandler(handler)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return cursorsdk.NewClient(
		cursorsdk.WithEndpoint(srv.URL, "test-token"),
		cursorsdk.WithCallTimeout(callTimeout),
	)
}

// A call is not a run: a bridge that never answers CreateAgent is given up on at
// the call timeout, not at the run's idle budget (and not never).
func TestCallIsGivenUpAtTheCallTimeout(t *testing.T) {
	const callTimeout = 150 * time.Millisecond
	client := fakeBridgeClient(t, wedgedBridge{}, callTimeout)

	started := time.Now()
	_, err := client.Agents().Create(context.Background(), cursorsdk.CreateOptions{Model: "composer-2"})
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("a bridge that never answered must not read as a created agent")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Create waited %s: the call timeout (%s) did not bound it", elapsed, callTimeout)
	}
	// The bound says what it is and names itself, so the report is actionable
	// rather than a bare transport deadline.
	msg := err.Error()
	if !strings.Contains(msg, "no answer from the bridge within") || !strings.Contains(msg, callTimeout.String()) {
		t.Fatalf("err=%q, want the bound named with the timeout it enforced (%s)", msg, callTimeout)
	}
}

// ... and the stream of the same client is not: the call timeout is for calls,
// while how long a run may last is the provider's business (the idle watchdog's,
// in fact).
func TestStreamIsNotBoundedByTheCallTimeout(t *testing.T) {
	const callTimeout = 120 * time.Millisecond
	svc := &fakeAgentService{
		onSend: func(ctx context.Context, stream *connect.ServerStream[sdkv1.RunStreamMessage]) error {
			for i := 0; i < 8; i++ { // 8×60ms ≈ 4× the call timeout
				if err := stream.Send(statusNudge("run-1", "working")); err != nil {
					return err
				}
				time.Sleep(60 * time.Millisecond)
			}
			if err := stream.Send(terminalResult("run-1", "hello")); err != nil {
				return err
			}
			return stream.Send(runDone())
		},
	}
	client := fakeBridgeClient(t, svc, callTimeout)

	wd := llmrun.NewIdleWatchdog(context.Background(), 5*time.Second)
	defer wd.Stop()
	ctx := llmrun.WithIdleWatchdog(wd.Context(), wd)

	run := startRun(t, client, ctx)
	res, err := run.WaitStream(ctx, nil)
	if err != nil {
		t.Fatalf("a run that kept producing events was cut by the call timeout: %v", err)
	}
	if res.Text != "hello" || !res.OK() {
		t.Fatalf("res=%+v, want a finished run with text %q", res, "hello")
	}
}

// recoveringBridge stops the live stream immediately but still has the run: the
// client's fallback is WaitLiveRun, which waits for the run instead of answering
// about it.
type recoveringBridge struct {
	sdkv1connect.UnimplementedSdkAgentServiceHandler

	waited time.Duration
}

func (recoveringBridge) CreateAgent(context.Context, *connect.Request[sdkv1.CreateAgentRequest]) (*connect.Response[sdkv1.CreateAgentResponse], error) {
	return connect.NewResponse(&sdkv1.CreateAgentResponse{AgentId: "agent-1"}), nil
}

// Send announces the run and then drops the stream: the run is known by id, but
// nothing terminal ever arrives on the live stream.
func (recoveringBridge) Send(_ context.Context, _ *connect.Request[sdkv1.SendRequest], stream *connect.ServerStream[sdkv1.RunStreamMessage]) error {
	return stream.Send(statusNudge("run-1", "working"))
}

func (b recoveringBridge) WaitLiveRun(ctx context.Context, _ *connect.Request[sdkv1.WaitLiveRunRequest]) (*connect.Response[sdkv1.WaitLiveRunResponse], error) {
	select {
	case <-time.After(b.waited):
	case <-ctx.Done():
		return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
	}
	return connect.NewResponse(&sdkv1.WaitLiveRunResponse{Result: &sdkv1.RunResult{
		RunId:  "run-1",
		Status: sdkv1.RunLifecycleStatus_RUN_LIFECYCLE_STATUS_FINISHED,
		Result: "recovered",
	}}), nil
}

// A run that lost its stream is recovered by waiting for the run — which is the
// run's business, not a call's: the call timeout must not cut the recovery short.
func TestRunWaitIsNotBoundedByTheCallTimeout(t *testing.T) {
	const callTimeout = 100 * time.Millisecond
	client := fakeBridgeClient(t, recoveringBridge{waited: 400 * time.Millisecond}, callTimeout)

	wd := llmrun.NewIdleWatchdog(context.Background(), 5*time.Second)
	defer wd.Stop()
	ctx := llmrun.WithIdleWatchdog(wd.Context(), wd)

	run := startRun(t, client, ctx)
	res, err := run.WaitStream(ctx, nil)
	if err != nil {
		t.Fatalf("a recovering run was cut by the call timeout (%s): %v", callTimeout, err)
	}
	if res.Text != "recovered" || !res.OK() {
		t.Fatalf("res=%+v, want the run's own result", res)
	}
}

// The call timeout is CURSOR_SDK_CALL_TIMEOUT, and it stays out of the way when it
// is not set: a duration overrides the default, junk falls back to it, and a
// non-positive value disconnects the bound.
func TestCallTimeoutComesFromTheEnv(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", cursorsdk.DefaultCallTimeout},
		{"90s", 90 * time.Second},
		{"2m", 2 * time.Minute},
		{"not-a-duration", cursorsdk.DefaultCallTimeout},
		{"sometime", cursorsdk.DefaultCallTimeout},
		{"-1s", 0},
		{"0", 0},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("CURSOR_SDK_CALL_TIMEOUT", tc.env)
			if got := cursorsdk.NewClient().CallTimeout; got != tc.want {
				t.Fatalf("CURSOR_SDK_CALL_TIMEOUT=%q → %s, want %s", tc.env, got, tc.want)
			}
		})
	}
}
