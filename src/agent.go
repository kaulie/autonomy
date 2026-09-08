package autonomy

import (
	"fmt"
	"os"

	"github.com/google/uuid"
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

// AgentFactory creates and caches agents by id. All agents (including Cursor-backed) register here.
type AgentFactory struct {
	agents map[string]*Agent
}

func NewAgentFactory() *AgentFactory {
	return &AgentFactory{
		agents: make(map[string]*Agent),
	}
}

// Create registers a new agent and optionally binds it to a task.
// Agent ID is independent of Task ID (agents can exist without tasks).
func (f *AgentFactory) Create(task *Task) *Agent {
	agent := f.NewAgent(newAgentID("agent"))
	if task != nil {
		agent.CurrentTask = task
		persistAgent(agent)
	}
	return agent
}

// newAgentID allocates a unique autonomy agent identity (not derived from task id).
func newAgentID(kind string) string {
	if kind == "" {
		kind = "agent"
	}
	return fmt.Sprintf("%s-%s", kind, uuid.NewString())
}

// NewAgent registers a local autonomy agent with AGENT_WORKSPACE.
// If id is empty, a unique id is allocated.
func (f *AgentFactory) NewAgent(id string) *Agent {
	if id == "" {
		id = newAgentID("agent")
	}
	if a, ok := f.agents[id]; ok {
		return a
	}
	ws, err := ensureAgentWorkspace(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] AGENT_WORKSPACE: %v\n", err)
		ws = AgentWorkspacePath(id)
	}
	agent := &Agent{
		ID:        id,
		State:     "idle",
		Lifecycle: AgentLifecycleEphemeral,
		Workspace: ws,
		Backend:   AgentBackendLocal,
	}
	f.agents[id] = agent
	agent.DecideMaker = NewDecideMaker()
	persistAgent(agent)
	return agent
}

// Delete removes an agent from the factory cache (local only).
func (f *AgentFactory) Delete(id string) {
	delete(f.agents, id)
}

// Get returns a cached agent by id, or nil.
func (f *AgentFactory) Get(id string) *Agent {
	return f.agents[id]
}

// Agent is the subject that owns a task and decision authority.
// Cursor-backed agents still register here; Cursor SDK is only the backend.
type Agent struct {
	ID          string
	State       string // runtime: idle | running | ...
	Lifecycle   AgentLifecycle
	Backend     AgentBackend
	LLMProvider LLMProvider
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
