package llmbackend

import (
	"context"
	"errors"
	"fmt"

	"github.com/kaulie/autonomy/src/llmrun"
)

// ErrNoSession is a prompt on a session that was never attached.
var ErrNoSession = errors.New("llm session not attached")

// BridgeCallErr is a failed session-setup call on a bridge, read against the context the
// run gave it. When the call ended because that context ended, the reason it ended is the
// answer — a run cut for being idle says "run idle for 3m0s: no provider activity", where
// the transport can only report the bare cancellation it saw ("context canceled"), which
// reads like the caller's own doing and hides three minutes of silence behind it. It is
// the same reading the stream paths make (src/cursorsdk/run.go → llmrun.CtxErr).
//
// A call that failed while the run was still alive keeps its own error: that is the
// bridge's word about the bridge, and nothing here improves on it.
func BridgeCallErr(ctx context.Context, err error) error {
	if err == nil || ctx == nil || ctx.Err() == nil {
		return err
	}
	return llmrun.CtxErr(ctx)
}

// EmptyModelResponseErr is what a run that produced no text at all is: a failed run,
// whatever the provider calls it. It is the failure whose fix is not in the prompt — an
// exhausted account, a spending limit or a model that is gone ends a run exactly this way,
// with the provider's own words in msg and nothing else — so the error says where the
// answer is: the backend belongs to the runtime process, not to the caller or to the agent
// (AUTONOMY_LLM_BACKEND, GET /health, docs/llm-backend.md).
func EmptyModelResponseErr(status, msg string) error {
	return fmt.Errorf("empty model response (status=%s msg=%s) — the model answered nothing; "+
		"if the account is out of quota or the model is gone, the LLM backend is a runtime "+
		"setting (AUTONOMY_LLM_BACKEND, see docs/llm-backend.md)", status, msg)
}
