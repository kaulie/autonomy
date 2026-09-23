package autonomy

import (
	"context"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// The Cursor entry points the runtime's own tests (and a Cursor-flavoured caller) use.
// What a Cursor session *is* — attach, re-attach by id, one prompt stream, Close/Delete —
// lives in src/llmbackend/cursor.go; these only say "this agent, this backend".

// AttachCursor binds a Cursor session to this agent: the session the agent row recorded
// when the provider still has it, a fresh one when it is gone.
func (a *Agent) AttachCursor(ctx context.Context, model string) error {
	a.SetBackend(llmbackend.Cursor, llmbackend.ProviderCursor)
	a.SetModel(model)
	_, _, err := a.llmSession().Attach(ctx, llmbackend.ModeAgent)
	return err
}

// resumeCursorSession attaches and says whether it re-attached a session that existed.
func (a *Agent) resumeCursorSession(ctx context.Context) (bool, error) {
	_, resumed, err := a.llmSession().Attach(ctx, llmbackend.ModeAgent)
	return resumed, err
}

// PromptCursor sends a prompt on the attached Cursor session and waits for the result.
func (a *Agent) PromptCursor(ctx context.Context, prompt string) (string, error) {
	return a.llmSession().PromptText(ctx, prompt, llmbackend.ModeAgent)
}

// PromptCursorStream sends a prompt and streams the provider's run events as neutral
// LLMEvents while the run is live.
func (a *Agent) PromptCursorStream(ctx context.Context, prompt string, onEvent func(llmbackend.Event)) (string, llmbackend.RunResult, error) {
	return a.llmSession().Prompt(ctx, prompt, llmbackend.ModeAgent, onEvent)
}
