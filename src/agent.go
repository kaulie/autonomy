package autonomy

import (
	"fmt"
	"os"

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
)

// LLMProvider identifies which LLM provider backs an agent.
type LLMProvider string

const (
	LLMProviderCursor          LLMProvider = "cursor"
	LLMProviderCline           LLMProvider = "cline"
	LLMProviderDeepseekHarness LLMProvider = "deepseek_harness"
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
func (f *AgentFactory) Create(task *Task) *Agent {
	agent := f.NewAgent()
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
func (f *AgentFactory) NewAgent() *Agent {
	agent := &Agent{
		State:     "idle",
		Lifecycle: AgentLifecycleEphemeral,
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
	CurrentTask *Task
	Context     string
	DecideMaker *DecisionMaker

	cursorClient *cursorsdk.Client
	cursorAgent  *cursorsdk.Agent
	// LLMAgentID is retained for persistent agents after Close (Resume later).
	LLMAgentID string
}

func (a *Agent) IsEphemeral() bool {
	return a.Lifecycle == "" || a.Lifecycle == AgentLifecycleEphemeral
}

func (a *Agent) Observe(result Result) {
	fmt.Println("Agent observed: ", result.Message)
}

func (a *Agent) Decide() (Decision, error) {
	return a.DecideAtStep(0)
}

func (a *Agent) DecideAtStep(step int) (Decision, error) {
	decision, err := a.DecideMaker.Decide(DecisionContext{
		Task:  a.CurrentTask,
		Agent: a,
		Step:  step,
	})
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
