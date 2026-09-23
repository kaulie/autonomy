package autonomy

import (
	"context"

	"github.com/kaulie/autonomy/src/llmbackend"
	"github.com/kaulie/autonomy/src/llmbackend/cline"
)

// The Cline entry points the runtime's own tests use. What a Cline session *is* — a
// resident session per mode/cwd, the plan session kept for the next process, the bridge's
// create/resume — lives in src/llmbackend/cline.go; these only say "this agent, this
// mode".

// clineModeToLLMMode maps a Cline session mode onto the runtime's reasoning mode.
func clineModeToLLMMode(mode string) llmbackend.Mode {
	if mode == cline.ClineModeFor(llmbackend.ModePlan) {
		return llmbackend.ModePlan
	}
	return llmbackend.ModeAgent
}

// clineModeFor is the Cline mode a reasoning mode runs in (the backend owns the mapping;
// the runtime's tests still read it from here).
func clineModeFor(mode ReasonMode) string { return cline.ClineModeFor(llmMode(mode)) }

// AttachCline binds a resident Cline session in the mode an agent's turns run in.
func (a *Agent) AttachCline(ctx context.Context) error {
	return a.attachClineMode(ctx, clineModeFor(ReasonModeAgent))
}

// attachClineMode binds the session for a mode the caller names in Cline's own words.
func (a *Agent) attachClineMode(ctx context.Context, mode string) error {
	a.SetBackend(llmbackend.Cline, llmbackend.ProviderCline)
	_, _, err := a.llmSession().Attach(ctx, clineModeToLLMMode(mode))
	return err
}

// attachClineSession binds the session for one (mode, cwd).
func (a *Agent) attachClineSession(ctx context.Context, mode, cwd string) error {
	a.SetWorkspace(cwd)
	return a.attachClineMode(ctx, mode)
}

// ensureClineSession attaches if needed and reports the provider session it runs on.
func (a *Agent) ensureClineSession(ctx context.Context, cwd, mode string) (string, error) {
	a.SetWorkspace(cwd)
	a.SetBackend(llmbackend.Cline, llmbackend.ProviderCline)
	id, _, err := a.llmSession().Attach(ctx, clineModeToLLMMode(mode))
	return id, err
}

// PromptClineStream sends a prompt on the attached Cline session and streams its events.
func (a *Agent) PromptClineStream(ctx context.Context, prompt, mode string, onEvent func(llmbackend.Event)) (string, llmbackend.RunResult, error) {
	return a.llmSession().Prompt(ctx, prompt, clineModeToLLMMode(mode), onEvent)
}
