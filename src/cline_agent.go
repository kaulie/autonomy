package autonomy

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/clinesdk"
)

// AttachCline binds a resident Cline SDK session (through the Node bridge) to
// this autonomy agent. All Cline-backed agents go through here so the shared
// bridge client is the only bridge used.
func (a *Agent) AttachCline(ctx context.Context, model string) error {
	if a == nil {
		return fmt.Errorf("nil agent")
	}
	if a.clineAgent != nil {
		return nil
	}
	client := sharedClineClient()
	if _, _, err := client.Ping(ctx); err != nil {
		return fmt.Errorf("cline bridge ping: %w", err)
	}
	cwd := a.Workspace
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if model == "" {
		model = defaultClineModel()
	}
	agent, err := client.Agents().Create(ctx, clinesdk.CreateOptions{
		ProviderID:   defaultClineProvider(),
		ModelID:      model,
		CWD:          cwd,
		SystemPrompt: defaultClineSystemPrompt(),
		Mode:         clineModeFor(ReasonModeAgent),
	})
	if err != nil {
		return fmt.Errorf("create cline agent: %w", err)
	}
	a.clineAgent = agent
	a.LLMAgentID = agent.ID
	a.Backend = AgentBackendCline
	a.LLMProvider = LLMProviderCline
	a.Model = model
	persistAgent(a)
	return nil
}

// clineModeFor maps an autonomy reasoning mode onto a Cline session mode. Plan
// work stays read-only (Cline's plan mode); everything else runs with tools
// auto-approved, which is what a capability prompt expects.
func clineModeFor(mode ReasonMode) string {
	if mode == ReasonModePlan {
		return "plan"
	}
	return clinesdk.DefaultMode
}

// ensureClineSession attaches a session if needed and makes sure it runs in the
// requested mode. A Cline session is mode-sticky, so a mode switch replaces the
// session (the prompt carries the context, so nothing is lost).
func (a *Agent) ensureClineSession(ctx context.Context, model, cwd, mode string) (string, error) {
	if a == nil {
		return "", fmt.Errorf("nil agent")
	}
	if a.clineAgent == nil {
		// AttachCline pins the agent-level defaults; the requested mode is
		// applied by promptClineMode below.
		if err := a.AttachCline(ctx, model); err != nil {
			return "", err
		}
	}
	if cwd != "" {
		a.Workspace = cwd
	}
	if a.clineAgent.Mode != mode {
		modeAgent, err := sharedClineClient().Agents().Create(ctx, clinesdk.CreateOptions{
			ProviderID:   a.clineAgent.ProviderID,
			ModelID:      a.clineAgent.ModelID,
			CWD:          a.clineAgent.CWD,
			SystemPrompt: defaultClineSystemPrompt(),
			Mode:         mode,
		})
		if err != nil {
			return "", fmt.Errorf("create cline agent (mode %s): %w", mode, err)
		}
		old := a.clineAgent
		a.clineAgent = modeAgent
		a.LLMAgentID = modeAgent.ID
		persistAgent(a)
		go func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := old.Close(closeCtx); err != nil {
				fmt.Fprintf(os.Stderr, "[autonomy] close cline session %s failed: %v\n", old.ID, err)
			}
		}()
	}
	return firstNonEmptyString(a.clineAgent.SessionID, a.clineAgent.ID), nil
}

// PromptClineStream sends a prompt on the attached Cline session and streams the
// provider's events to onEvent as neutral LLMEvents, returning the final text and
// run-level metadata (mirrors PromptCursorStream).
func (a *Agent) PromptClineStream(ctx context.Context, prompt, mode string, onEvent func(LLMEvent)) (string, LLMRunResult, error) {
	failure := func(err error) (string, LLMRunResult, error) {
		return "", LLMRunResult{
			Status: LLMStatusError, ErrorMessage: err.Error(), StartedAt: time.Now(), EndedAt: time.Now(),
		}, err
	}
	if a == nil {
		return failure(fmt.Errorf("cline session not attached"))
	}
	// A Cline session is mode-sticky, so make sure the attached session runs in
	// the requested mode before sending.
	if _, err := a.ensureClineSession(ctx, "", a.Workspace, mode); err != nil {
		return failure(err)
	}
	if a.clineAgent == nil {
		return failure(fmt.Errorf("cline session not attached"))
	}
	started := time.Now()
	sink := func(ev clinesdk.RunEvent) {
		if onEvent == nil {
			return
		}
		if mapped, ok := MapNativeLLMEvent(a.LLMProvider, ev, started); ok {
			onEvent(mapped)
		}
	}
	run, err := a.clineAgent.Send(ctx, prompt)
	if err != nil {
		return "", LLMRunResult{
			Status: LLMStatusError, ErrorMessage: err.Error(), StartedAt: started, EndedAt: time.Now(),
		}, fmt.Errorf("cline send: %w", err)
	}
	result, err := run.WaitStream(ctx, sink)
	if err != nil {
		meta := LLMRunResult{Status: LLMStatusError, ErrorMessage: err.Error(), StartedAt: started, EndedAt: time.Now()}
		if result != nil {
			meta = clineRunResultToLLMRun(*result, started)
		}
		return "", meta, fmt.Errorf("cline wait: %w", err)
	}
	if result == nil {
		return "", LLMRunResult{Status: LLMStatusError, StartedAt: started, EndedAt: time.Now()},
			fmt.Errorf("cline run ended without a result")
	}
	text := strings.TrimSpace(result.Text)
	meta := clineRunResultToLLMRun(*result, started)
	if text == "" {
		return "", meta, fmt.Errorf("empty model response (status=%s msg=%s)", result.Status, result.ErrorMessage)
	}
	return text, meta, nil
}

// disposeClineSession ends the Cline session for this task.
func (a *Agent) disposeClineSession(ctx context.Context) {
	if a == nil || a.clineAgent == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := a.clineAgent.Close(cctx); err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] close cline agent %s failed: %v\n", a.clineAgent.ID, err)
	}
	// The shared bridge client is process-wide and must NOT be closed here.
	a.clineAgent = nil
}
