package autonomy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

	sharedCursorMu.Lock()
	previous := sharedCursorClnt
	sharedCursorClnt = nil
	sharedCursorMu.Unlock()
	t.Cleanup(func() {
		sharedCursorMu.Lock()
		sharedCursorClnt = previous
		sharedCursorMu.Unlock()
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
