package llmbackend

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/clinesdk"
)

// clineSession is the Cline backend's session: a resident session per mode/cwd, held by the
// bridge process. A Cline session is sticky in both, so those two define the handle, and
// sessions for other modes/cwds are kept alive (closing one while a freshly started session
// initialises can stall that run, and an idle handle costs a bridge-side map entry).
type clineSession struct {
	session *Session
	agents  map[string]*clinesdk.Agent
	current *clinesdk.Agent
	resumed bool
	// nextResume is the session the created session was asked to continue ("" for a fresh one).
	nextResume string
}

func newClineSession(s *Session) *clineSession {
	return &clineSession{session: s, agents: map[string]*clinesdk.Agent{}}
}

// clineModeFor maps an autonomy reasoning mode onto a Cline session mode. Plan work stays
// read-only (Cline's plan mode); everything else runs with tools auto-approved, which is
// what a capability prompt expects.
func clineModeFor(mode Mode) string {
	if mode == ModePlan {
		return "plan"
	}
	return clinesdk.DefaultMode
}

// attach makes sure the session for this mode is the current one, opening it if needed.
func (c *clineSession) attach(ctx context.Context, mode Mode) (bool, error) {
	facts := c.session.host.Facts()
	cwd := facts.Workspace
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	// A Cline session is sticky in mode *and* cwd, so the current one is reused only when
	// both match: a different cwd is a different session (which is what the (mode, cwd) key
	// meant before this lived here).
	if c.current != nil && c.current.Mode == clineModeFor(mode) && c.current.CWD == cwd {
		return c.resumed, nil
	}
	return c.attachFor(ctx, clineModeFor(mode), cwd)
}

// attachFor creates (or reuses) the session for one mode/cwd and makes it the current one.
func (c *clineSession) attachFor(ctx context.Context, mode, cwd string) (bool, error) {
	if _, _, err := ClineClient().Ping(ctx); err != nil {
		return false, fmt.Errorf("cline bridge ping: %w", bridgeCallErr(ctx, err))
	}
	key := mode + "\x00" + cwd
	if existing, ok := c.agents[key]; ok {
		c.current, c.resumed = existing, true
		c.session.host.SetBackend(Cline, ProviderCline)
		return true, nil
	}
	resume := c.resumeSession(mode)
	c.nextResume = resume
	agent, err := ClineClient().Agents().Create(ctx, clinesdk.CreateOptions{
		ProviderID:      ResolveClineProvider(),
		ModelID:         ResolveClineModel(),
		APIKey:          strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_API_KEY")),
		BaseURL:         strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_BASE_URL")),
		CWD:             cwd,
		SystemPrompt:    DefaultClineSystemPrompt(),
		Mode:            mode,
		ResumeSessionID: resume,
	})
	if err != nil {
		return false, fmt.Errorf("create cline agent (mode %s): %w", mode, err)
	}
	c.agents[key] = agent
	c.current = agent
	c.resumed = resume != ""
	host := c.session.host
	host.SetWorkspace(cwd)
	host.SetBackend(Cline, ProviderCline)
	host.SetModel(agent.ModelID)
	// A session this process created starts with no instructions: the next decision cycle
	// sends the AGENT_V2 frame again (Agent.needsLLMFrame). A Cline session is always a new
	// one here — a resumed transcript is *seeded into* a fresh session, which has not been
	// told the rules — so this is unconditional, exactly as resetLLMFrame() was.
	host.SetFrameSent(false)
	c.record()
	fmt.Fprintf(os.Stderr, "[autonomy] cline session agent=%s mode=%s provider=%s model=%s cwd=%s resume=%s\n",
		agent.ID, mode, agent.ProviderID, agent.ModelID, agent.CWD, FirstNonEmptyString(resume, "-"))
	return c.resumed, nil
}

// resumeSession is the session a new one of this mode should continue, as the agent
// recorded it. Only the planner's session (Cline's plan mode) is continued: that is where
// the task's own conversation lives, and a worker's session belongs to one delegation — a
// fresh one costs that worker a prompt it already carries.
//
// The recorded value is a Cline session id (`cls-…`). An older row may hold the bridge's
// own handle (`cls_…`, minted by createAgent): that handle dies with the bridge process
// that made it, so it is not something to continue from. A value that is not a session id
// is therefore ignored rather than handed to the bridge.
func (c *clineSession) resumeSession(mode string) string {
	if mode != clineModeFor(ModePlan) {
		return ""
	}
	recorded := strings.TrimSpace(c.session.host.Facts().SessionID)
	if !strings.HasPrefix(recorded, "cls-") {
		return ""
	}
	return recorded
}

// record keeps the session the task's own conversation is on, so the next process can
// continue it (src/clinesdk/bridge/resume.mjs): a Cline session lives inside the bridge
// process, so a bridge restart takes the conversation with it, and the id on the agent row
// is what the next bridge reads the transcript back with.
func (c *clineSession) record() {
	if c.current == nil || strings.TrimSpace(c.current.SessionID) == "" {
		return
	}
	if c.current.Mode != clineModeFor(ModePlan) {
		return
	}
	host := c.session.host
	if host.Facts().SessionID == c.current.SessionID {
		return
	}
	host.SetSessionID(c.current.SessionID)
	host.Persist()
}

// prompt sends one turn on the session for this mode, streaming neutral Events.
func (c *clineSession) prompt(ctx context.Context, text string, mode Mode, onEvent func(Event)) (string, RunResult, error) {
	failure := func(err error) (string, RunResult, error) {
		return "", RunResult{
			Status: StatusError, ErrorMessage: err.Error(), StartedAt: time.Now(), EndedAt: time.Now(),
		}, err
	}
	// A Cline session is mode-sticky, so make sure the current session runs in the mode
	// this turn wants before sending.
	if _, err := c.attach(ctx, mode); err != nil {
		return failure(err)
	}
	if c.current == nil {
		return failure(errNoSession)
	}
	facts := c.session.host.Facts()
	provider := facts.Provider
	if provider == "" {
		provider = ProviderCline
	}
	started := time.Now()
	sink := func(ev clinesdk.RunEvent) {
		if onEvent == nil {
			return
		}
		if mapped, ok := MapNativeLLMEvent(provider, ev, started); ok {
			onEvent(mapped)
		}
	}
	run, err := c.current.Send(ctx, text)
	if err != nil {
		return "", RunResult{
			Status: StatusError, ErrorMessage: err.Error(), StartedAt: started, EndedAt: time.Now(),
		}, fmt.Errorf("cline send: %w", err)
	}
	result, err := run.WaitStream(ctx, sink)
	// The session id exists from the first run on: keep it on the agent row, so the process
	// that comes next continues this conversation instead of starting a new one
	// (src/clinesdk/bridge/resume.mjs).
	c.record()
	if err != nil {
		meta := RunResult{Status: StatusError, ErrorMessage: err.Error(), StartedAt: started, EndedAt: time.Now()}
		if result != nil {
			meta = ClineRunResultToLLMRun(*result, started)
		}
		return "", meta, fmt.Errorf("cline wait: %w", err)
	}
	if result == nil {
		return "", RunResult{Status: StatusError, StartedAt: started, EndedAt: time.Now()},
			fmt.Errorf("cline run ended without a result")
	}
	out := strings.TrimSpace(result.Text)
	meta := ClineRunResultToLLMRun(*result, started)
	if out == "" {
		// A run that answered nothing is a failed run, whatever the provider calls it.
		meta.Status = StatusError
		return "", meta, emptyModelResponseErr(result.Status, result.ErrorMessage)
	}
	return out, meta, nil
}

// dispose closes every Cline session this agent opened (they are resident on the bridge;
// the process-wide client is not this session's to close).
func (c *clineSession) dispose(ctx context.Context, _ bool) {
	for key, agent := range c.agents {
		if err := agent.Close(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] close cline agent %s (%s) failed: %v\n", agent.ID, key, err)
		}
	}
	c.agents = map[string]*clinesdk.Agent{}
	c.current = nil
}

// ClineModeFor is the Cline session mode a runtime reasoning mode runs in: plan work stays
// read-only (Cline's plan mode); everything else runs with tools auto-approved.
func ClineModeFor(mode Mode) string { return clineModeFor(mode) }

func (c *clineSession) sessionID() string {
	if c.current == nil {
		return ""
	}
	return FirstNonEmptyString(c.current.SessionID, c.current.ID)
}

func (c *clineSession) mode() string {
	if c.current == nil {
		return ""
	}
	return c.current.Mode
}

// resumedFrom is the transcript this attach asked the bridge to seed into its new session.
func (c *clineSession) resumedFrom() string { return c.nextResume }
