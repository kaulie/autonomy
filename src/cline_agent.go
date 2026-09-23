package autonomy

import "github.com/kaulie/autonomy/src/llmbackend"

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/clinesdk"
)

// AttachCline binds a resident Cline SDK session (through the Node bridge) to
// this autonomy agent, in the mode its turns run in: a worker acts (tools
// auto-approved). All Cline-backed agents go through here so the shared bridge
// client is the only bridge used.
//
// The model comes from AUTONOMY_CLINE_MODEL, or — when that is unset — from the
// provider/model saved by `cline auth`, resolved by the bridge.
func (a *Agent) AttachCline(ctx context.Context) error {
	return a.attachClineMode(ctx, clineModeFor(ReasonModeAgent))
}

// attachClineMode binds the session for an agent whose mode the caller knows: a
// planner's cycles decide, which is Cline's plan mode (read-only). A session is
// mode-sticky, so opening the one the agent will actually run on is what keeps a
// resumed agent from holding a second, unused session.
func (a *Agent) attachClineMode(ctx context.Context, mode string) error {
	if a == nil {
		return fmt.Errorf("nil agent")
	}
	if a.clineAgent != nil {
		return nil
	}
	client := llmbackend.ClineClient()
	if _, _, err := client.Ping(ctx); err != nil {
		return fmt.Errorf("cline bridge ping: %w", err)
	}
	cwd := a.Workspace
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	return a.attachClineSession(ctx, mode, cwd)
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
	resume := a.clineResumeSession(mode)
	agent, err := llmbackend.ClineClient().Agents().Create(ctx, clinesdk.CreateOptions{
		ProviderID:      llmbackend.ResolveClineProvider(),
		ModelID:         llmbackend.ResolveClineModel(),
		APIKey:          strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_API_KEY")),
		BaseURL:         strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_BASE_URL")),
		CWD:             cwd,
		SystemPrompt:    llmbackend.DefaultClineSystemPrompt(),
		Mode:            mode,
		ResumeSessionID: resume,
	})
	if err != nil {
		return fmt.Errorf("create cline agent (mode %s): %w", mode, err)
	}
	a.clineAgents[key] = agent
	a.setClineSession(agent)
	// A brand-new session starts with no instructions: the next decision cycle
	// sends the AGENT_V2 frame again (see Agent.needsLLMFrame).
	a.resetLLMFrame()
	a.Backend = llmbackend.Cline
	a.LLMProvider = llmbackend.ProviderCline
	a.Model = agent.ModelID
	a.recordClineSession()
	fmt.Fprintf(os.Stderr, "[autonomy] cline session agent=%s mode=%s provider=%s model=%s cwd=%s resume=%s\n",
		agent.ID, mode, agent.ProviderID, agent.ModelID, agent.CWD, llmbackend.FirstNonEmptyString(resume, "-"))
	return nil
}

func (a *Agent) setClineSession(agent *clinesdk.Agent) {
	a.clineAgent = agent
	a.Backend = llmbackend.Cline
	a.LLMProvider = llmbackend.ProviderCline
	a.Model = agent.ModelID
}

// clineResumeSession is the session a new one of this mode should continue, as this
// agent recorded it. Only the planner's session (Cline's plan mode) is continued: it is
// where the task's own conversation lives, and a worker's session belongs to one
// delegation — a fresh one costs that worker a prompt it already carries.
//
// The recorded value is a Cline session id (`cls-…`). An older row may hold the
// bridge's own handle (`cls_…`, minted by createAgent): that handle dies with the
// bridge process that made it, so it is not something to continue from. A value that is
// not a session id is therefore ignored rather than handed to the bridge.
func (a *Agent) clineResumeSession(mode string) string {
	if a == nil || mode != clineModeFor(ReasonModePlan) {
		return ""
	}
	recorded := strings.TrimSpace(a.LLMAgentID)
	if !strings.HasPrefix(recorded, "cls-") {
		return ""
	}
	return recorded
}

// recordClineSession keeps the session the task's own conversation is on, so the next
// process can continue it (src/clinesdk/bridge/resume.mjs): a Cline session lives inside
// the bridge process, so a bridge restart takes the conversation with it, and the id on
// the agent row is what the next bridge reads the transcript back with.
func (a *Agent) recordClineSession() {
	if a == nil || a.clineAgent == nil || strings.TrimSpace(a.clineAgent.SessionID) == "" {
		return
	}
	if a.clineAgent.Mode != clineModeFor(ReasonModePlan) {
		return
	}
	if a.LLMAgentID == a.clineAgent.SessionID {
		return
	}
	a.LLMAgentID = a.clineAgent.SessionID
	persistAgent(a)
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
	return llmbackend.FirstNonEmptyString(a.clineAgent.SessionID, a.clineAgent.ID), nil
}

// PromptClineStream sends a prompt on the attached Cline session and streams the
// provider's events to onEvent as neutral LLMEvents, returning the final text and
// run-level metadata (mirrors PromptCursorStream).
func (a *Agent) PromptClineStream(ctx context.Context, prompt, mode string, onEvent func(llmbackend.Event)) (string, llmbackend.RunResult, error) {
	failure := func(err error) (string, llmbackend.RunResult, error) {
		return "", llmbackend.RunResult{
			Status: llmbackend.StatusError, ErrorMessage: err.Error(), StartedAt: time.Now(), EndedAt: time.Now(),
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
		if mapped, ok := llmbackend.MapNativeLLMEvent(a.LLMProvider, ev, started); ok {
			onEvent(mapped)
		}
	}
	run, err := a.clineAgent.Send(ctx, prompt)
	if err != nil {
		return "", llmbackend.RunResult{
			Status: llmbackend.StatusError, ErrorMessage: err.Error(), StartedAt: started, EndedAt: time.Now(),
		}, fmt.Errorf("cline send: %w", err)
	}
	result, err := run.WaitStream(ctx, sink)
	// The session id exists from the first run on: keep it on the agent row, so the
	// process that comes next continues this conversation instead of starting a new one
	// (src/clinesdk/bridge/resume.mjs).
	a.recordClineSession()
	if err != nil {
		meta := llmbackend.RunResult{Status: llmbackend.StatusError, ErrorMessage: err.Error(), StartedAt: started, EndedAt: time.Now()}
		if result != nil {
			meta = llmbackend.ClineRunResultToLLMRun(*result, started)
		}
		return "", meta, fmt.Errorf("cline wait: %w", err)
	}
	if result == nil {
		return "", llmbackend.RunResult{Status: llmbackend.StatusError, StartedAt: started, EndedAt: time.Now()},
			fmt.Errorf("cline run ended without a result")
	}
	text := strings.TrimSpace(result.Text)
	meta := llmbackend.ClineRunResultToLLMRun(*result, started)
	if text == "" {
		// A run that answered nothing is a failed run, whatever the provider calls
		// it: an exhausted account ends a session as "finished" with a message and
		// no text at all. Recording that as finished would leave the only account
		// of the failure in the run's message.
		meta.Status = llmbackend.StatusError
		return "", meta, emptyModelResponseErr(result.Status, result.ErrorMessage)
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
