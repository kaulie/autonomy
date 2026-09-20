package cursorsdk

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"

	"github.com/kaulie/autonomy/src/cursorsdk/gen/sdk/v1/sdkv1connect"
)

// bearerTransport injects Authorization on every request (unary and stream).
type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

func newBridgeHTTPClient(token string) *http.Client {
	return &http.Client{
		Transport: bearerTransport{
			token: token,
			base: &http.Transport{
				Proxy:             nil, // never proxy loopback bridge traffic
				ForceAttemptHTTP2: false,
			},
		},
	}
}

// connectOpts is how every RPC of a client is built: the transport rules a call
// obeys, applied on the client that makes it.
func connectOpts(callTimeout time.Duration) []connect.ClientOption {
	return []connect.ClientOption{connect.WithInterceptors(bridgeCallTimeout(callTimeout))}
}

// bridgeCallTimeout bounds one *call* to the bridge (opening or resuming a session,
// closing or deleting one, pinging, listing or cancelling runs) with the client's
// call timeout.
//
// A call is not a run. A run is a stream, and what bounds it is the run's own idle
// watchdog (src/llmrun: AUTONOMY_LLM_TIMEOUT, default 3m of silence) — which is why
// this is a unary-only interceptor and Send cannot be touched by it. WaitLiveRun is
// the other exception: it does not answer *about* a run, it waits *for* one (the
// recovery path for a dropped stream — src/cursorsdk/run.go), so it belongs to the
// run's budget, not to a call's.
//
// A call, on the other hand, is answered now or never: opening a session should take
// seconds, so a bridge that has not answered one after the whole call timeout is
// wedged, and waiting longer only spends the caller's run on it. Left unbounded, a
// single wedged CreateAgent would sit there until the idle watchdog fired, and the
// run it belonged to would be lost reporting the transport's words ("context
// canceled") instead of what happened.
//
// A non-positive timeout disables the bound: the call then follows only its
// caller's context.
func bridgeCallTimeout(callTimeout time.Duration) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if callTimeout <= 0 || isRunWait(req.Spec().Procedure) {
				return next(ctx, req)
			}
			cctx, cancel := context.WithTimeout(ctx, callTimeout)
			defer cancel()
			resp, err := next(cctx, req)
			// The bound is this side's doing, so it says so in its own words — and
			// names the number, which is the knob (CURSOR_SDK_CALL_TIMEOUT) — instead
			// of the bare "context deadline exceeded" the transport saw. A call the
			// caller's own context ended is left alone: whoever ended it says why
			// (src/cursor_agent.go → bridgeCallErr).
			if err != nil && ctx.Err() == nil && cctx.Err() == context.DeadlineExceeded {
				return nil, connect.NewError(connect.CodeDeadlineExceeded,
					fmt.Errorf("no answer from the bridge within %s", callTimeout))
			}
			return resp, err
		}
	})
}

// isRunWait reports whether this RPC waits for a run rather than answering about
// one. Today that is WaitLiveRun alone.
func isRunWait(procedure string) bool {
	return procedure == sdkv1connect.SdkAgentServiceWaitLiveRunProcedure
}
