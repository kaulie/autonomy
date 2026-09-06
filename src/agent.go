package autonomy

import "fmt"

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

	agent := f.NewAgent("agent-0")
	agent.CurrentTask = task
	return agent
}

func (f *AgentFactory) NewAgent(id string) *Agent {
	if a, ok := f.agents[id]; ok {
		return a
	}
	agent := &Agent{ID: id, State: "idle"}
	f.agents[id] = agent
	agent.DecideMaker = NewDecideMaker()
	return agent
}

// Agent is the subject that owns a task and decision authority.
// Decision making is a capability of the agent, not of the loop.
type Agent struct {
	ID          string
	State       string // lifecycle: idle | running | ...
	CurrentTask *Task
	Context     string
	DecideMaker *DecisionMaker
}

func (a *Agent) Observe(result Result) {
	fmt.Println("Agent observed: ", result.Message)
}

func (a *Agent) Decide() Decision {
	decision, err := a.DecideMaker.Decide(DecisionContext{
		Task: a.CurrentTask,
	})
	// fmt.Println("Agent decided: ", decision, ", error: ", err)
	if err != nil {
		return Decision{}
	}
	return decision
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
