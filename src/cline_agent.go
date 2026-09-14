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
//
// The model comes from AUTONOMY_CLINE_MODEL, or — when that is unset — from the
// provider/model saved by `cline auth`, resolved by the bridge.
func (a *Agent) AttachCline(ctx context.Context) error {
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
	return a.attachClineSession(ctx, clineModeFor(ReasonModeAgent), cwd)
}

// clineSessionKey identifies one resident session: a Cline session is sticky in
// both mode and cwd, so those two define the handle.
func clineSessionKey(mode, cwd string) string {
	return mode + "\x00" + cwd
}

// attachClineSession creates (or reuses) the session for one mode/cwd and makes
// it the current one. Sessions for other modes/cwds are kept alive: closing one
// while a freshly started session is initialising can stall that run, and an
// idle handle costs a bridge-side map entry.
func (a *Agent) attachClineSession(ctx context.Context, mode, cwd string) error {
	if a.clineAgents == nil {
		a.clineAgents = map[string]*clinesdk.Agent{}
	}
	key := clineSessionKey(mode, cwd)
	if existing, ok := a.clineAgents[key]; ok {
		a.setClineSession(existing)
		return nil
	}
	agent, err := sharedClineClient().Agents().Create(ctx, clinesdk.CreateOptions{
		ProviderID:   resolveClineProvider(),
		ModelID:      resolveClineModel(),
		APIKey:       strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_API_KEY")),
		BaseURL:      strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_BASE_URL")),
		CWD:          cwd,
		SystemPrompt: defaultClineSystemPrompt(),
		Mode:         mode,
	})
	if err != nil {
		return fmt.Errorf("create cline agent (mode %s): %w", mode, err)
	}
	a.clineAgents[key] = agent
	a.setClineSession(agent)
	a.LLMAgentID = agent.ID
	a.Backend = AgentBackendCline
	a.LLMProvider = LLMProviderCline
	a.Model = agent.ModelID
	persistAgent(a)
	fmt.Fprintf(os.Stderr, "[autonomy] cline session agent=%s mode=%s provider=%s model=%s cwd=%s\n",
		agent.ID, mode, agent.ProviderID, agent.ModelID, agent.CWD)
	return nil
}

func (a *Agent) setClineSession(agent *clinesdk.Agent) {
	a.clineAgent = agent
	a.LLMAgentID = agent.ID
	a.Backend = AgentBackendCline
	a.LLMProvider = LLMProviderCline
	a.Model = agent.ModelID
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
// requested mode. A Cline session is sticky in mode and cwd, so a change means
// switching to another resident session (the prompt carries the context).
func (a *Agent) ensureClineSession(ctx context.Context, cwd, mode string) (string, error) {
	if a == nil {
		return "", fmt.Errorf("nil agent")
	}
	if cwd != "" {
		a.Workspace = cwd
	}
	if err := a.attachClineSession(ctx, mode, a.Workspace); err != nil {
		return "", err
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
	if _, err := a.ensureClineSession(ctx, a.Workspace, mode); err != nil {
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

// disposeClineSession ends every Cline session this agent opened for this task.
func (a *Agent) disposeClineSession(ctx context.Context) {
	if a == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for key, agent := range a.clineAgents {
		if err := agent.Close(cctx); err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] close cline agent %s (%s) failed: %v\n", agent.ID, key, err)
		}
	}
	a.clineAgents = nil
	a.clineAgent = nil
	// The shared bridge client is process-wide and must NOT be closed here.
}
