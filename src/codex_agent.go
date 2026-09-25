package autonomy

import (
	"context"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// The Codex entry points for the runtime: what a Codex session *is* — attach, resume by
// thread id, one prompt stream — lives in src/llmbackend/codex; this says "this agent, this
// backend", the same shape as AttachCline (src/cline_agent.go).
//
// It exists because an agent can be *acquired* on Codex: a task whose account is a codex
// account delegates its work to a worker that runs on that same account (Runtime.AcquireAgent
// inherits the delegating agent's account), so the worker's backend is the account's harness —
// which can be codex, not only the process default.

// AttachCodex binds a Codex session to this agent, in the mode an agent's turns run in.
func (a *Agent) AttachCodex(ctx context.Context) error {
	a.SetBackend(llmbackend.Codex, llmbackend.ProviderCodex)
	_, _, err := a.llmSession().Attach(ctx, llmMode(ReasonModeAgent))
	return err
}
