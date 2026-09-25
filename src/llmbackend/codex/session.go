package codex

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/codexsdk"
	"github.com/kaulie/autonomy/src/llmbackend"
)

// codexSession is the Codex harness's session: one resident Codex thread per (mode, cwd),
// held by the bridge process. A Codex thread is sticky in both — its instructions and its
// working directory — so those two define the handle; threads for other modes/cwds are kept
// alive (an idle handle costs a bridge-side map entry, and closing one while a freshly
// started thread initialises can stall that run).
//
// Continuing across restarts is the provider's own mechanism, not a replay: the agent row
// records the thread id and Attach hands it back as `resumeSessionId`, which the bridge
// turns into `codex.resumeThread(id)` — threads are persisted under ~/.codex/sessions, so a
// restart (and a new bridge process) continues the same conversation.
type codexSession struct {
	host    llmbackend.Host
	agents  map[string]*codexsdk.Agent
	current *codexsdk.Agent
	resumed bool
	// nextResume is the thread the created session was asked to continue ("" for a fresh one).
	nextResume string
}

func newCodexSession(host llmbackend.Host) *codexSession {
	return &codexSession{host: host, agents: map[string]*codexsdk.Agent{}}
}

// Attach makes sure the session for this mode is the current one, opening it if needed.
func (c *codexSession) Attach(ctx context.Context, mode llmbackend.Mode) (bool, error) {
	cwd := c.workspace()
	if c.current != nil && c.current.Mode == CodexModeFor(mode) && c.current.CWD == cwd {
		return c.resumed, nil
	}
	return c.attachFor(ctx, CodexModeFor(mode), cwd)
}

func (c *codexSession) workspace() string {
	if w := strings.TrimSpace(c.host.Facts().Workspace); w != "" {
		return w
	}
	wd, _ := os.Getwd()
	return wd
}

// attachFor opens (or reuses) the session for one mode/cwd and makes it the current one.
func (c *codexSession) attachFor(ctx context.Context, mode, cwd string) (bool, error) {
	if _, _, err := codexClient().Ping(ctx); err != nil {
		return false, fmt.Errorf("codex bridge ping: %w", llmbackend.BridgeCallErr(ctx, err))
	}
	key := mode + "\x00" + cwd
	if existing, ok := c.agents[key]; ok {
		c.current, c.resumed = existing, true
		c.host.SetBackend(llmbackend.Codex, llmbackend.ProviderCodex)
		return true, nil
	}
	resume := c.resumeThread(mode)
	c.nextResume = resume
	// Credentials are the account's (src/accounts.go): the pool is the runtime's only source,
	// and a Codex account without a key runs on whatever `codex auth` saved.
	creds := c.host.Facts().Creds
	// Whether this turn may reach the network from inside its sandbox is a deployment decision
	// (src/llmbackend/codex/network.go): a sandboxed turn that cannot fetch the repository has
	// nothing to work on.
	network := NetworkFor(mode)
	agent, err := codexClient().Agents().Create(ctx, codexsdk.CreateOptions{
		ModelID:         creds.Model,
		APIKey:          creds.APIKey,
		BaseURL:         creds.BaseURL,
		CWD:             cwd,
		SystemPrompt:    defaultCodexSystemPrompt(),
		Mode:            mode,
		ResumeSessionID: resume,
		NetworkAccess:   &network,
	})
	if err != nil {
		return false, fmt.Errorf("create codex thread (mode %s): %w", mode, err)
	}
	c.agents[key] = agent
	c.current = agent
	c.resumed = resume != ""
	host := c.host
	host.SetWorkspace(cwd)
	host.SetBackend(llmbackend.Codex, llmbackend.ProviderCodex)
	host.SetModel(agent.ModelID)
	// A thread this process opened has not been told the rules: the next decision cycle
	// sends the AGENT_V2 frame again. A resumed thread already carries its history (the
	// frame included), so it keeps what the row recorded — the same rule as cline.
	if !c.resumed {
		host.SetFrameSent(false)
	}
	c.record()
	fmt.Fprintf(os.Stderr, "[autonomy] codex thread=%s mode=%s sandbox=%s model=%s cwd=%s resume=%s\n",
		agent.ID, mode, sandboxFor(mode), agent.ModelID, cwd, llmbackend.FirstNonEmptyString(resume, "-"))
	return c.resumed, nil
}

// sandboxFor is what the bridge turns the mode into, for the log line only.
func sandboxFor(mode string) string {
	if mode == "plan" {
		return "read-only"
	}
	return "workspace-write"
}

// resumeThread is the thread a new session of this mode should continue, as the agent
// recorded it.
//
// Only the planner's thread (plan mode) is continued: that is where the task's own
// conversation lives, and a worker's thread belongs to one delegation — a fresh one costs
// that worker nothing, because its prompt carries the frame it needs.
//
// The recorded value is a Codex thread id (a uuid). An older row may hold the bridge's own
// handle (`cdx_…`, minted by createAgent): that handle dies with the bridge process that
// made it, so it is not something to continue from. Anything with that prefix is ignored
// rather than handed to the bridge.
func (c *codexSession) resumeThread(mode string) string {
	if mode != CodexModeFor(llmbackend.ModePlan) {
		return ""
	}
	recorded := strings.TrimSpace(c.host.Facts().SessionID)
	if recorded == "" || strings.HasPrefix(recorded, "cdx_") {
		return ""
	}
	return recorded
}

// record keeps the thread the task's own conversation is on, so the next process continues
// it: a Codex thread is persisted by the CLI under ~/.codex/sessions, and the id on the
// agent row is what the next bridge resumes.
func (c *codexSession) record() {
	if c.current == nil || strings.TrimSpace(c.current.SessionID) == "" {
		return
	}
	if c.current.Mode != CodexModeFor(llmbackend.ModePlan) {
		return
	}
	host := c.host
	if host.Facts().SessionID == c.current.SessionID {
		return
	}
	host.SetSessionID(c.current.SessionID)
	host.Persist()
}

// Prompt sends one turn on the session for this mode, streaming neutral Events.
func (c *codexSession) Prompt(ctx context.Context, text string, mode llmbackend.Mode, onEvent func(llmbackend.Event)) (string, llmbackend.RunResult, error) {
	failure := func(err error) (string, llmbackend.RunResult, error) {
		return "", llmbackend.RunResult{
			Status: llmbackend.StatusError, ErrorMessage: err.Error(), StartedAt: time.Now(), EndedAt: time.Now(),
		}, err
	}
	if _, err := c.Attach(ctx, mode); err != nil {
		return failure(err)
	}
	if c.current == nil {
		return failure(llmbackend.ErrNoSession)
	}
	provider := llmbackend.FirstNonEmptyString(string(c.host.Facts().Provider), string(llmbackend.ProviderCodex))
	started := time.Now()
	sink := func(ev codexsdk.RunEvent) {
		if onEvent == nil {
			return
		}
		if mapped, ok := llmbackend.MapNativeLLMEvent(llmbackend.Provider(provider), ev, started); ok {
			onEvent(mapped)
		}
	}
	run, err := c.current.Send(ctx, text)
	if err != nil {
		return "", llmbackend.RunResult{
			Status: llmbackend.StatusError, ErrorMessage: err.Error(), StartedAt: started, EndedAt: time.Now(),
		}, fmt.Errorf("codex send: %w", err)
	}
	result, err := run.WaitStream(ctx, sink)
	// The thread id exists from the first run on: keep it on the agent row, so the process
	// that comes next resumes this thread instead of starting a new one.
	c.record()
	if err != nil {
		meta := llmbackend.RunResult{Status: llmbackend.StatusError, ErrorMessage: err.Error(), StartedAt: started, EndedAt: time.Now()}
		if result != nil {
			meta = CodexRunResultToLLMRun(*result, started)
		}
		return "", meta, fmt.Errorf("codex wait: %w", err)
	}
	if result == nil {
		return "", llmbackend.RunResult{Status: llmbackend.StatusError, StartedAt: started, EndedAt: time.Now()},
			fmt.Errorf("codex run ended without a result")
	}
	out := strings.TrimSpace(result.Text)
	meta := CodexRunResultToLLMRun(*result, started)
	if out == "" {
		// A run that answered nothing is a failed run, whatever the provider calls it.
		meta.Status = llmbackend.StatusError
		return "", meta, llmbackend.EmptyModelResponseErr(result.Status, result.ErrorMessage)
	}
	return out, meta, nil
}

// Dispose closes every thread this agent opened (they are resident on the bridge; the
// process-wide client is not this session's to close). A Codex thread lives in the CLI's
// session store, so closing the handle keeps it resumable.
func (c *codexSession) Dispose(ctx context.Context, _ bool) {
	for key, agent := range c.agents {
		if err := agent.Close(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] close codex agent %s (%s) failed: %v\n", agent.ID, key, err)
		}
	}
	c.agents = map[string]*codexsdk.Agent{}
	c.current = nil
}

func (c *codexSession) SessionID() string {
	if c.current == nil {
		return ""
	}
	return llmbackend.FirstNonEmptyString(c.current.SessionID, c.current.ID)
}

func (c *codexSession) Resumed() bool { return c.resumed }

func (c *codexSession) Mode() string {
	if c.current == nil {
		return ""
	}
	return c.current.Mode
}

// ResumedFrom is the thread this attach asked the bridge to continue.
func (c *codexSession) ResumedFrom() string { return c.nextResume }
