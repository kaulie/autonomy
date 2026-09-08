package autonomy

import "fmt"

// AgentLifecycle controls whether an agent survives past the current task.
type AgentLifecycle string

const (
	AgentLifecycleEphemeral  AgentLifecycle = "ephemeral"
	AgentLifecyclePersistent AgentLifecycle = "persistent"
)

// AgentFactory creates and caches agents by id.
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
	return agent
}

func (f *AgentFactory) NewAgent(id string) *Agent {
	if a, ok := f.agents[id]; ok {
		return a
	}
	agent := &Agent{
		ID:        id,
		State:     "idle",
		Lifecycle: AgentLifecycleEphemeral,
	}
	f.agents[id] = agent
	agent.DecideMaker = NewDecideMaker()
	return agent
}

// Delete removes an agent from the factory cache.
func (f *AgentFactory) Delete(id string) {
	delete(f.agents, id)
}

// Get returns a cached agent by id, or nil.
func (f *AgentFactory) Get(id string) *Agent {
	return f.agents[id]
}

// Agent is the subject that owns a task and decision authority.
// Decision making is a capability of the agent, not of the loop.
type Agent struct {
	ID          string
	State       string // runtime: idle | running | ...
	Lifecycle   AgentLifecycle
	CurrentTask *Task
	Context     string
	DecideMaker *DecisionMaker
}

func (a *Agent) IsEphemeral() bool {
	return a.Lifecycle == "" || a.Lifecycle == AgentLifecycleEphemeral
}

func (a *Agent) Observe(result Result) {
	fmt.Println("Agent observed: ", result.Message)
}

func (a *Agent) Decide() (Decision, error) {
	decision, err := a.DecideMaker.Decide(DecisionContext{
		Task: a.CurrentTask,
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
}

func (a *Agent) Stop() {
	a.State = "idle"
}

func (a *Agent) IsRunning() bool {
	return a.State == "running"
}
