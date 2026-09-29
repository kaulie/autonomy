package autonomy

import (
	"fmt"
	"strings"
)

// AgentRuntimeResponse is the resolved LLM runtime for one agent: which harness,
// provider and model actually back its session, as assignAgentRuntime decides
// (src/agent_runtime.go). It answers "你背后是哪个模型" for a given agent id.
type AgentRuntimeResponse struct {
	AgentID     int64  `json:"agent_id"`
	Name        string `json:"name,omitempty"`
	Role        string `json:"role,omitempty"`
	Backend     string `json:"backend,omitempty"`
	LLMProvider string `json:"llm_provider,omitempty"`
	Model       string `json:"model,omitempty"`
	AccountID   string `json:"account_id,omitempty"`
	Account     string `json:"account,omitempty"`
	Why         string `json:"why,omitempty"`
}

// AgentRuntime resolves which model and provider back an agent, using the same
// plan the runtime applies before it opens a session. The store row is merged
// with the live handle when this process holds one (role, purpose, runtime policy).
func (r *Autonomy) AgentRuntime(agentID int64) (*AgentRuntimeResponse, error) {
	if r == nil || r.Store == nil {
		return nil, fmt.Errorf("store not ready")
	}
	if agentID == 0 {
		return nil, fmt.Errorf("empty agent id")
	}
	stored, err := r.agentStore().GetAgent(agentID)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, errAgentNotFound
	}
	agent := mergeAgentForRuntime(stored, r.liveAgent(agentID))
	policy := agent.runtimePolicy
	if policy.DelegatingAgent == nil {
		policy.DelegatingAgent = r.delegatingPlannerForWorker(agent)
	}
	plan, err := assignAgentRuntime(agent, policy)
	if err != nil {
		return nil, err
	}
	return agentRuntimeResponseFromPlan(agent, plan), nil
}

func agentRuntimeResponseFromPlan(agent *Agent, plan agentRuntimePlan) *AgentRuntimeResponse {
	out := &AgentRuntimeResponse{
		AgentID: agent.ID,
		Name:    agent.Name,
		Role:    string(agent.Role),
		Backend: string(plan.Backend),
		Model:   plan.Model,
		Why:     plan.Why,
	}
	if plan.Provider != "" {
		out.LLMProvider = string(plan.Provider)
	}
	if plan.Account != nil {
		out.AccountID = plan.Account.ID
		out.Account = plan.Account.Label
		if out.Account == "" {
			out.Account = plan.Account.ID
		}
	} else if agent.AccountID != "" {
		out.AccountID = agent.AccountID
		out.Account = agent.accountSummary()
	}
	if out.Role == "" {
		out.Role = string(AgentRolePlanner)
	}
	if out.Backend == "" && agent.Backend != "" {
		out.Backend = string(agent.Backend)
	}
	if out.LLMProvider == "" && agent.LLMProvider != "" {
		out.LLMProvider = string(agent.LLMProvider)
	}
	if out.Model == "" {
		out.Model = defaultAgentModel(plan.Backend)
	}
	return out
}

func (r *Autonomy) liveAgent(agentID int64) *Agent {
	if r == nil || r.AgentFactory == nil {
		return nil
	}
	for _, a := range r.AgentFactory.snapshot() {
		if a != nil && a.ID == agentID {
			return a
		}
	}
	return nil
}

// mergeAgentForRuntime overlays runtime-only fields from a live handle onto the
// persisted row without losing what the store alone carries (account id, task).
func mergeAgentForRuntime(stored, live *Agent) *Agent {
	if stored == nil {
		return live
	}
	if live == nil {
		out := *stored
		if out.Role == "" {
			out.Role = AgentRolePlanner
		}
		return &out
	}
	out := *stored
	if live.Role != "" {
		out.Role = live.Role
	} else if out.Role == "" {
		out.Role = AgentRolePlanner
	}
	if live.Purpose != "" {
		out.Purpose = live.Purpose
	}
	if live.Backend != "" {
		out.Backend = live.Backend
	}
	if live.LLMProvider != "" {
		out.LLMProvider = live.LLMProvider
	}
	if strings.TrimSpace(live.Model) != "" {
		out.Model = live.Model
	}
	if live.AccountID != "" {
		out.AccountID = live.AccountID
	}
	out.runtimePolicy = live.runtimePolicy
	if out.runtimePolicy.RequestedBackend == "" && live.Backend != "" {
		out.runtimePolicy.RequestedBackend = string(live.Backend)
	}
	return &out
}

// delegatingPlannerForWorker finds the planner an offline worker would inherit
// from: the task's own agent when the worker has no in-memory delegating handle.
func (r *Autonomy) delegatingPlannerForWorker(agent *Agent) *Agent {
	if r == nil || agent == nil || agent.Role != AgentRoleWorker {
		return nil
	}
	taskID := ""
	if agent.CurrentTask != nil {
		taskID = strings.TrimSpace(agent.CurrentTask.ID)
	}
	if taskID == "" {
		return nil
	}
	task, err := r.taskStore().GetTask(taskID)
	if err != nil || task == nil || task.AgentID == 0 || task.AgentID == agent.ID {
		return nil
	}
	planner, err := r.agentStore().GetAgent(task.AgentID)
	if err != nil || planner == nil {
		return nil
	}
	return mergeAgentForRuntime(planner, r.liveAgent(planner.ID))
}
