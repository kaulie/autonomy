package autonomy

import (
	"fmt"
	"strings"
)

// This file is agent initialization, independent of any task: an agent is built
// and given its system prompt *before* anything is accepted for it. A task is a
// later, separate step (Autonomy.accept / AcceptTaskRequest): initialization names
// no task, sets no CurrentTask, and needs none to exist.
//
// The flow the runtime runs in is therefore:
//
//	Initialize                     an agent exists, with its system prompt
//	  → the system prompt           carried in the reasoning frame (buildReasoningFrame)
//	  → accept a task               Autonomy.accept / POST /api/tasks
//	  → the agent plans first       RequirePlanApproval pauses the run on its first plan
//	  → the user confirms           MessageKindApproval releases it
//	  → the plan is implemented     the approved plan executes, then the loop runs on
//
// InitializeAgent is the entry point onto a running Autonomy.

// AgentInitOptions is what an agent is initialized with — before, and without, any
// task. Nothing here refers to a task, because initialization does not.
type AgentInitOptions struct {
	// Role is what the agent is in the runtime's division of labour. Empty means
	// planner: the agent a task's decision cycles belong to.
	Role AgentRole
	// Purpose is the label an acquiring capability gave it (empty for a planner).
	Purpose string
	// Workspace is the sandbox the agent works in; empty keeps the workspace the
	// factory allocated for it (AGENT_WORKSPACE).
	Workspace string
	// Model is the model the agent runs on; empty keeps the backend's default.
	Model string
	// SystemPrompt is the agent's own instructions, given before any task: it is
	// carried in the reasoning frame, so it reaches the agent's session on its first
	// turn, ahead of the task. Empty is allowed — the agent policy is the prompt.
	SystemPrompt string
	// RequirePlanApproval makes the agent plan first and wait: its run pauses on the
	// first plan (status awaiting_approval) until a confirmation arrives, so
	// implementation starts only after the plan is approved.
	RequirePlanApproval bool
}

// AgentInitializer initializes agents independently of tasks.
type AgentInitializer struct {
	factory *AgentFactory
}

// NewAgentInitializer builds an initializer over a factory.
func NewAgentInitializer(factory *AgentFactory) *AgentInitializer {
	return &AgentInitializer{factory: factory}
}

// Initialize creates an agent and gives it its system prompt, with no task in
// sight: it never binds a task (CurrentTask stays nil), so the same call works
// before anything has been accepted for the agent. The agent a later instruction
// pairs with a task is this one (resumeAgentForTask → forInitialization).
func (in *AgentInitializer) Initialize(opts AgentInitOptions) (*Agent, error) {
	if in == nil || in.factory == nil {
		return nil, fmt.Errorf("agent initializer: no factory")
	}
	agent := in.factory.NewAgent()
	agent.Role = opts.Role
	if strings.TrimSpace(string(agent.Role)) == "" {
		agent.Role = AgentRolePlanner
	}
	agent.Purpose = strings.TrimSpace(opts.Purpose)
	if ws := strings.TrimSpace(opts.Workspace); ws != "" {
		agent.Workspace = ws
	}
	if model := strings.TrimSpace(opts.Model); model != "" {
		agent.Model = model
	}
	agent.GiveSystemPrompt(opts.SystemPrompt)
	agent.RequirePlanApproval = opts.RequirePlanApproval
	persistAgent(agent)
	return agent, nil
}

// GiveSystemPrompt records the agent's system prompt — step 2 of the flow, after
// initialization and before any task. The prompt is not sent anywhere here: it is
// folded into the reasoning frame, so it is delivered on the agent's first turn,
// ahead of the task's own words (buildReasoningFrame). It is safe to call again;
// the last prompt given is the one the frame carries.
func (a *Agent) GiveSystemPrompt(prompt string) {
	if a == nil {
		return
	}
	a.SystemPrompt = strings.TrimSpace(prompt)
	persistAgent(a)
}

// agentSystemPrompt is the agent's system prompt as the frame carries it.
func agentSystemPrompt(agent *Agent) string {
	if agent == nil {
		return ""
	}
	return strings.TrimSpace(agent.SystemPrompt)
}

// needsPlanApproval reports whether this decision must wait for the user's
// confirmation before it is implemented: the agent was initialized to require it,
// the run has not been approved, and the decision is a plan. A concluding decision
// (done / blocked / need_input) is an answer, not a plan, so it is never held back.
func needsPlanApproval(agent *Agent, decision Decision) bool {
	if agent == nil || !agent.RequirePlanApproval || agent.planApproved {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(decision.Type), decisionPlanType)
}

// decisionPlanType is the decision type that plans work (AGENT_V2). Named here
// because the plan-approval gate reads it directly.
const decisionPlanType = "plan"

// InitializeAgent initializes an agent through a running Autonomy — the entry
// point onto the process's factory and store.
func (r *Autonomy) InitializeAgent(opts AgentInitOptions) (*Agent, error) {
	if r == nil || r.AgentFactory == nil {
		return nil, fmt.Errorf("autonomy not initialized")
	}
	return NewAgentInitializer(r.AgentFactory).Initialize(opts)
}
