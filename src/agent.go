package autonomy

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/kaulie/autonomy/src/cursorsdk"
)

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

// Delete removes an agent from the factory cache (local only).
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

	// Cursor SDK session for the current task (LLMReasoner).
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
	decision, err := a.DecideMaker.Decide(DecisionContext{
		Task:  a.CurrentTask,
		Agent: a,
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

// ensureCursorSession creates or resumes a Cursor SDK agent for this autonomy agent.
func (a *Agent) ensureCursorSession(ctx context.Context, model, cwd string) (*cursorsdk.Agent, error) {
	if a.cursorAgent != nil {
		return a.cursorAgent, nil
	}
	client := a.cursorClient
	if client == nil {
		client = cursorsdk.NewClient(
			cursorsdk.WithAPIKey(os.Getenv("CURSOR_API_KEY")),
			cursorsdk.WithWorkspace(cwd),
			cursorsdk.WithBridgeBin(os.Getenv("CURSOR_SDK_BRIDGE_BIN")),
		)
		a.cursorClient = client
	}
	if err := client.Ping(ctx); err != nil {
		return nil, fmt.Errorf("cursor bridge ping: %w", err)
	}

	var (
		agent *cursorsdk.Agent
		err   error
	)
	if a.CursorAgentID != "" && !a.IsEphemeral() {
		agent, err = client.Agents().Resume(ctx, a.CursorAgentID, model)
		if err != nil {
			return nil, fmt.Errorf("resume cursor agent: %w", err)
		}
	} else {
		agent, err = client.Agents().Create(ctx, cursorsdk.CreateOptions{
			Model: model,
			CWD:   cwd,
		})
		if err != nil {
			return nil, fmt.Errorf("create cursor agent: %w", err)
		}
	}
	a.cursorAgent = agent
	a.CursorAgentID = agent.ID
	return agent, nil
}

// disposeCursorSession ends the Cursor SDK session for this task.
// Ephemeral agents are permanently deleted via DeleteAgent; persistent agents are only Closed.
func (a *Agent) disposeCursorSession(ctx context.Context) {
	if a.cursorAgent != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		// Bound cleanup so task teardown cannot hang forever.
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if a.IsEphemeral() {
			_ = a.cursorAgent.Delete(cctx)
			a.CursorAgentID = ""
		} else {
			_ = a.cursorAgent.Close(cctx)
		}
		a.cursorAgent = nil
	}
	if a.cursorClient != nil {
		_ = a.cursorClient.Close()
		a.cursorClient = nil
	}
}
