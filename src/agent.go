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

// AgentFactory creates and caches agents by id. All agents (including Cursor-backed) register here.
type AgentFactory struct {
	agents map[string]*Agent
}

func NewAgentFactory() *AgentFactory {
	return &AgentFactory{
		agents: make(map[string]*Agent),
	}
}

func (f *AgentFactory) Create(task *Task) *Agent {
	agentName := fmt.Sprintf("agent-%s", task.ID)
	agent := f.NewAgent(agentName)
	agent.CurrentTask = task
	persistAgent(agent)
	return agent
}

// NewAgent registers a local autonomy agent with AGENT_WORKSPACE.
func (f *AgentFactory) NewAgent(id string) *Agent {
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
	Workspace   string // AGENT_WORKSPACE for this agent (code sandbox)
	CurrentTask *Task
	Context     string
	DecideMaker *DecisionMaker

	cursorClient *cursorsdk.Client
	cursorAgent  *cursorsdk.Agent
	// CursorAgentID is retained for persistent agents after Close (Resume later).
	CursorAgentID string
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
