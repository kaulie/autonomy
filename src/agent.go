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
	persistAgent(agent)
	return agent
}

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
// Decision making is a capability of the agent, not of the loop.
type Agent struct {
	ID          string
	State       string // runtime: idle | running | ...
	Lifecycle   AgentLifecycle
	Workspace   string // AGENT_WORKSPACE for this agent (code sandbox)
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

// ensureCursorSession creates or resumes a Cursor SDK agent for this autonomy agent.
func (a *Agent) ensureCursorSession(ctx context.Context, model, cwd string) (*cursorsdk.Agent, error) {
	if a.cursorAgent != nil {
		return a.cursorAgent, nil
	}
	if cwd == "" && a.Workspace != "" {
		cwd = a.Workspace
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
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
	persistAgent(a)
	return agent, nil
}

// disposeCursorSession ends the Cursor SDK session for this task.
// Ephemeral agents are permanently deleted via DeleteAgent; persistent agents are only Closed.
// Cleanup failures are logged to stderr (they must not hide the original task error).
func (a *Agent) disposeCursorSession(ctx context.Context) {
	if a.cursorAgent != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		// Bound cleanup so task teardown cannot hang forever.
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		agentID := a.cursorAgent.ID
		if a.IsEphemeral() {
			if err := a.cursorAgent.Delete(cctx); err != nil {
				fmt.Fprintf(os.Stderr, "[autonomy] DeleteAgent %s failed: %v\n", agentID, err)
			} else {
				fmt.Fprintf(os.Stderr, "[autonomy] DeleteAgent %s ok\n", agentID)
			}
			a.CursorAgentID = ""
		} else {
			if err := a.cursorAgent.Close(cctx); err != nil {
				fmt.Fprintf(os.Stderr, "[autonomy] CloseAgent %s failed: %v\n", agentID, err)
			}
		}
		a.cursorAgent = nil
	}
	if a.cursorClient != nil {
		if err := a.cursorClient.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] cursor client Close failed: %v\n", err)
		}
		a.cursorClient = nil
	}
}
