package autonomy

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/clinesdk"
	"github.com/kaulie/autonomy/src/cursorsdk"
)

// AgentLifecycle controls whether an agent survives past the current task.
type AgentLifecycle string

const (
	AgentLifecycleEphemeral  AgentLifecycle = "ephemeral"
	AgentLifecyclePersistent AgentLifecycle = "persistent"
)

// AgentBackend identifies how an autonomy agent is backed.
type AgentBackend string

const (
	AgentBackendLocal  AgentBackend = "local"
	AgentBackendCursor AgentBackend = "cursor"
	AgentBackendCline  AgentBackend = "cline"
)

// LLMProvider identifies which LLM provider backs an agent.
type LLMProvider string

const (
	LLMProviderCursor          LLMProvider = "cursor"
	LLMProviderCline           LLMProvider = "cline"
	LLMProviderDeepseekHarness LLMProvider = "deepseek_harness"
)

// AgentRole is what an agent is in the runtime's division of labour — the first
// thing an agent's own prompt says about it (see the ## Agent section of
// src/agent_policy/AGENT_V2.md, CODE_EDIT.md, DEPLOYMENT_MONITOR.md).
type AgentRole string

const (
	// AgentRolePlanner is the agent a task's decision cycles belong to: it
	// understands the task, defines the completion contract and plans.
	AgentRolePlanner AgentRole = "planner"
	// AgentRoleWorker is an agent a capability acquired for one delegated job
	// through the broker (Runtime.AcquireAgent). What the job is, is its Purpose.
	AgentRoleWorker AgentRole = "worker"
)

// AgentFactory creates and caches agents by name. All agents (including Cursor-backed) register here.
type AgentFactory struct {
	agents map[string]*Agent
	// nextID is a process-local fallback used only when no Store is wired in
	// (e.g. lightweight unit tests); production always allocates via SQLite
	// AUTOINCREMENT starting at 10000.
	nextID int64
}

func NewAgentFactory() *AgentFactory {
	return &AgentFactory{
		agents: make(map[string]*Agent),
		nextID: 9999,
	}
}

// Create registers a new agent and optionally binds it to a task.
// Agent ID is independent of Task ID (agents can exist without tasks).
//
// The agent a task is created for is the one that decides that task's cycles, so
// it is a planner: the role its own prompt states (## Agent). An agent a
// capability acquires through the broker is the other kind — a worker for one job
// (Runtime.AcquireAgent).
func (f *AgentFactory) Create(task *Task) *Agent {
	agent := f.NewAgent()
	agent.Role = AgentRolePlanner
	if task != nil {
		agent.CurrentTask = task
		task.AgentID = agent.ID
		persistTask(task)
		persistAgent(agent)
	}
	return agent
}

// NewAgent registers a local autonomy agent with AGENT_WORKSPACE.
// The numeric ID and name (agent-{id}) are allocated by the Store; without a
// Store a process-local counter starting at 10000 is used as a fallback.
//
// An agent is kept by default (`persistent`): the row it was given, the task it
// was paired with, and the provider session id it was recorded with all outlive
// the run that created it, so a later instruction for the same task — in this
// process or in the one after a restart — finds the same agent and resumes it
// (resumeAgentForTask). A capability that wants a throwaway worker asks for one
// explicitly (broker.AcquireAgentOpts.Ephemeral).
func (f *AgentFactory) NewAgent() *Agent {
	agent := &Agent{
		State:     "idle",
		Lifecycle: AgentLifecyclePersistent,
		Backend:   AgentBackendLocal,
	}
	persistAgent(agent) // Store assigns ID and Name when available
	if agent.ID == 0 {
		f.nextID++
		agent.ID = f.nextID
	}
	if agent.Name == "" {
		agent.Name = fmt.Sprintf("agent-%d", agent.ID)
	}
	if a, ok := f.agents[agent.Name]; ok {
		return a
	}
	ws, err := ensureAgentWorkspace(agent.Name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] AGENT_WORKSPACE: %v\n", err)
		ws = AgentWorkspacePath(agent.Name)
	}
	agent.Workspace = ws
	f.agents[agent.Name] = agent
	agent.DecideMaker = NewDecideMaker()
	return agent
}

// Delete removes an agent from the factory cache (local only).
func (f *AgentFactory) Delete(name string) {
	delete(f.agents, name)
}

// Get returns a cached agent by name, or nil.
func (f *AgentFactory) Get(name string) *Agent {
	return f.agents[name]
}

// snapshot is the agents this factory holds, for a walk that does not touch the
// registry (teardown, see Autonomy.Close).
func (f *AgentFactory) snapshot() []*Agent {
	if f == nil {
		return nil
	}
	out := make([]*Agent, 0, len(f.agents))
	for _, agent := range f.agents {
		out = append(out, agent)
	}
	return out
}

// ForTask returns the agent this factory already holds for one task's cycles, or
// nil. It is the planner — the agent that task's decisions belong to — because a
// delegated worker carries the delegating task id too and is not that task's
// agent.
//
// A factory that just started holds nothing: the agent a task was paired with
// before a restart is in the store, not here (see resumeAgentForTask).
func (f *AgentFactory) ForTask(taskID string) *Agent {
	taskID = strings.TrimSpace(taskID)
	if f == nil || taskID == "" {
		return nil
	}
	for _, agent := range f.agents {
		if agent == nil || agent.Role != AgentRolePlanner || agent.CurrentTask == nil {
			continue
		}
		if agent.CurrentTask.ID == taskID {
			return agent
		}
	}
	return nil
}

// Adopt registers an agent that was rebuilt from its stored row (see
// restoredAgent), under the name that row already has, so the handle the runtime
// is about to use is the one later instructions find. Unlike NewAgent it
// allocates nothing: the agent has its id, name and workspace already, and the
// store has its row.
//
// A handle already registered under that name wins: two instances of one agent
// would be two conversations on one record.
func (f *AgentFactory) Adopt(agent *Agent) *Agent {
	if f == nil || agent == nil {
		return nil
	}
	if agent.Name == "" {
		agent.Name = fmt.Sprintf("agent-%d", agent.ID)
	}
	if existing, ok := f.agents[agent.Name]; ok {
		return existing
	}
	agent.DecideMaker = NewDecideMaker()
	if agent.Workspace == "" {
		if ws, err := ensureAgentWorkspace(agent.Name); err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] AGENT_WORKSPACE: %v\n", err)
			agent.Workspace = AgentWorkspacePath(agent.Name)
		} else {
			agent.Workspace = ws
		}
	}
	f.agents[agent.Name] = agent
	return agent
}

// Agent is the subject that owns a task and decision authority.
// Cursor-backed agents still register here; Cursor SDK is only the backend.
type Agent struct {
	ID          int64  // SQLite AUTOINCREMENT primary key (starts at 10000)
	Name        string // human identifier, agent-{id}
	State       string // runtime: idle | running | ...
	Lifecycle   AgentLifecycle
	Backend     AgentBackend
	LLMProvider LLMProvider
	Model       string // LLM model in use (e.g. composer-2)
	Workspace   string // AGENT_WORKSPACE for this agent (code sandbox)
	// DeletedAt is when this agent was let go (agents.deleted_at), zero while it
	// is live. It is a row fact, not runtime state: it is read with the row and
	// says whether the agent a task names can still be resumed (see
	// resumeAgentForTask).
	DeletedAt time.Time
	// Role and Purpose are what this agent is here to do: role is planner or
	// worker, purpose is the label the acquiring capability gave it
	// (broker.AcquireAgentOpts.Purpose). They are runtime state — what the
	// agent's own prompt is rendered from — and are not persisted.
	Role        AgentRole
	Purpose     string
	CurrentTask *Task
	Context     string
	DecideMaker *DecisionMaker
	// Session is this agent's conversation with its LLM — where its turns are taken
	// and recorded. Every agent has one: the runtime gives the task's own agent a
	// session when it starts the task (Autonomy.Run), and Runtime.AcquireAgent gives
	// a delegated worker one when a capability acquires it. What the agent is
	// (Role) is what makes its turns plan turns or delegated turns, so there is no
	// second kind of session to pick from.
	Session *LLMSession

	cursorAgent *cursorsdk.Agent
	// clineAgent is the Cline session for the mode in use; clineAgents keeps one
	// session handle per (mode, cwd) for the agent's lifetime, because closing a
	// session while another one is starting can stall that run.
	clineAgent  *clinesdk.Agent
	clineAgents map[string]*clinesdk.Agent
	// LLMAgentID is retained for persistent agents after Close (Resume later).
	LLMAgentID string
	// llmFrameSent records that the AGENT_V2 frame (the instructions that do not
	// change per cycle) has already been delivered on the current LLM session, so
	// later decision cycles send only the per-cycle delta. It is reset when a
	// session is created (or replaced) — see AttachCursor / attachClineSession —
	// and set only after a run actually succeeded, so a failed first cycle
	// resends the frame instead of leaving the session without instructions.
	llmFrameSent bool
}

// needsLLMFrame reports whether this cycle's message must carry the AGENT_V2
// frame: true until the frame has been delivered on the current LLM session.
func (a *Agent) needsLLMFrame() bool {
	return a == nil || !a.llmFrameSent
}

// markLLMFrameSent records that the frame reached the session.
func (a *Agent) markLLMFrameSent() {
	if a != nil {
		a.llmFrameSent = true
	}
}

// resetLLMFrame marks the current session as having no frame yet: the next
// decision cycle sends it again.
func (a *Agent) resetLLMFrame() {
	if a != nil {
		a.llmFrameSent = false
	}
}

func (a *Agent) IsEphemeral() bool {
	return a.Lifecycle == "" || a.Lifecycle == AgentLifecycleEphemeral
}

// closeAgent ends an agent's life: it stops and its provider sessions are torn
// down. What is left of it depends on its lifecycle, and the default is to keep
// it: a persistent agent (the default — see NewAgent) is only closed, so its row
// and its durable provider-side state stay for a later instruction to resume,
// while an ephemeral one is deleted (soft-deleted in the store, dropped from the
// factory) because nothing is meant to come back to it.
//
// Every agent ends here, whichever door it came in through: the task's own agent when
// its run is over, a delegated worker when the capability releases its session. There
// is nothing in it that depends on who the agent was, which is why there is one of it.
func closeAgent(agent *Agent, factory *AgentFactory, ctx context.Context) {
	if agent == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	agent.Stop()
	agent.disposeCursorSession(ctx)
	agent.disposeClineSession(ctx)
	if agent.IsEphemeral() {
		softDeleteAgent(agent.ID)
		if factory != nil {
			factory.Delete(agent.Name)
		}
		return
	}
	persistAgent(agent)
}

func (a *Agent) Observe(result Result) {
	fmt.Println("Agent observed: ", result.Message)
}

func (a *Agent) Decide() (Decision, error) {
	return a.DecideAtCycle(0, nil)
}

func (a *Agent) DecideAtCycle(cycle int, history []Result) (Decision, error) {
	return a.decide(context.Background(), cycle, history, "")
}

// decide is one cycle of this agent: which cycle it is, what already happened, and
// the message it answers (the inbox message being processed — see DecisionContext.
// Input).
func (a *Agent) decide(ctx context.Context, cycle int, history []Result, input string) (Decision, error) {
	decisionContext := DecisionContext{
		Context: ctx,
		Task:    a.CurrentTask,
		Agent:   a,
		Cycle:   cycle,
		Input:   input,
		History: history,
	}
	// The world this cycle reasons about is resolved first: the task's context_ref,
	// through the context builder (src/context_resolver.go) — the prompt is built from
	// what that found, not from the reference string.
	fillContextSections(&decisionContext)
	decision, err := a.DecideMaker.Decide(decisionContext)
	if err != nil {
		return Decision{}, err
	}
	fmt.Printf("Agent decided: reason=%q\n", decision.Reason)
	return decision, nil
}

func (a *Agent) Result() (bool, error) {
	return true, nil
}

func (a *Agent) Start() {
	a.State = "running"
	persistAgent(a)
}

func (a *Agent) Stop() {
	a.State = "idle"
	persistAgent(a)
}

func (a *Agent) IsRunning() bool {
	return a.State == "running"
}
