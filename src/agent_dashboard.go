package autonomy

import (
	"fmt"
	"strings"
	"time"
)

// The agent-status dashboard: a read-only view of every agent this runtime knows
// about, for a human watching the fleet work. It is the data half of the two
// endpoints the page uses — GET /api/agents (the JSON) and GET /dashboard (the
// page that polls it, src/http_server.go) — and it adds nothing to the runtime:
// every field it reports is read from what the runtime already holds, the agent
// rows in the store (AgentStore.ListAgents) merged with the live handles in the
// agent factory (the row does not persist role or purpose, the runtime handle does).

// queryBool reads a truthy query flag: "1", "true", "yes" and "on" are true, and
// anything else (including the empty string) is false. It is the one flag the
// dashboard feed takes (include_deleted).
func queryBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// AgentHealth is the coarse liveness verdict the dashboard colours a row by: an
// agent that still exists is ok, one that has been let go is deleted.
type AgentHealth string

const (
	AgentHealthOK      AgentHealth = "ok"
	AgentHealthDeleted AgentHealth = "deleted"
)

// AgentStatus is one agent as the dashboard shows it: identity (id, name), what it
// is here to do (role, purpose, lifecycle, backend), what it is doing right now
// (state, working, current_task, the live provider run id when one is in flight)
// and its liveness (health). Every field is optional-tolerant: an agent read from
// the store after a restart has no live handle, so role and purpose are empty until
// it is resumed, and the page renders that as "—" rather than inventing a value.
type AgentStatus struct {
	AgentID int64  `json:"agent_id"`
	Name    string `json:"name,omitempty"`
	// Role / Purpose are runtime state (planner / worker, and the label the
	// acquiring capability gave a worker) — not persisted on the agent row — so
	// they are filled from the live handle when this process holds one.
	Role    string `json:"role,omitempty"`
	Purpose string `json:"purpose,omitempty"`
	Backend string `json:"backend,omitempty"`
	// Account is the pool entry this agent's credentials come from (src/accounts.go), as
	// "acct-… (harness/label)": which account a run bills is part of an agent's status.
	Account string `json:"account,omitempty"`
	// Lifecycle is persistent / ephemeral: whether the agent survives past its task.
	Lifecycle string `json:"lifecycle"`
	// State is the runtime state: idle | running | deleted.
	State string `json:"state"`
	// Health is ok while the agent exists, deleted once it has been let go.
	Health AgentHealth `json:"health"`
	// Working is true while an LLM run of this agent is in flight (an active
	// reason turn on its current task), and AgentRunID is that run's id.
	Working     bool   `json:"working"`
	AgentRunID  string `json:"agent_run_id,omitempty"`
	CurrentTask string `json:"current_task,omitempty"`

	LLMProvider string `json:"llm_provider,omitempty"`
	Model       string `json:"model,omitempty"`
}

// AgentStatusListResponse is the dashboard feed: one row per agent, plus the count
// and when it was taken, so a poller can tell a fresh snapshot from a stale one.
type AgentStatusListResponse struct {
	Agents      []AgentStatus `json:"agents"`
	Count       int           `json:"count"`
	GeneratedAt time.Time     `json:"generated_at"`
}

// AgentStatusList reads every agent and reports its live status. Soft-deleted
// agents are included only when includeDeleted is true: the fleet a human watches
// is the live one, and a caller that wants the history asks for it explicitly
// (?include_deleted=1). A store that cannot be read is an error, not an empty
// fleet — an empty page must mean "no agents", never "the read failed".
func (r *Autonomy) AgentStatusList(includeDeleted bool) (*AgentStatusListResponse, error) {
	if r == nil || r.Store == nil {
		return nil, fmt.Errorf("store not ready")
	}
	rows, err := r.agentStore().ListAgents()
	if err != nil {
		return nil, err
	}

	// The live handles, by id: the row carries what the runtime wrote to the
	// store, and the handle carries what it never persists (role, purpose).
	live := map[int64]*Agent{}
	if r.AgentFactory != nil {
		for _, a := range r.AgentFactory.snapshot() {
			if a != nil {
				live[a.ID] = a
			}
		}
	}

	agents := make([]AgentStatus, 0, len(rows))
	for _, a := range rows {
		if a == nil {
			continue
		}
		deleted := !a.DeletedAt.IsZero()
		if deleted && !includeDeleted {
			continue
		}
		status := AgentStatus{
			AgentID:     a.ID,
			Name:        a.Name,
			Lifecycle:   string(a.Lifecycle),
			Backend:     string(a.Backend),
			State:       a.State,
			LLMProvider: string(a.LLMProvider),
			Account:     a.accountSummary(),
			Model:       a.Model,
			Health:      AgentHealthOK,
		}
		if handle := live[a.ID]; handle != nil {
			status.Role = string(handle.Role)
			status.Purpose = handle.Purpose
			if handle.Backend != "" {
				status.Backend = string(handle.Backend)
			}
			if handle.State != "" {
				status.State = handle.State
			}
		}
		if a.CurrentTask != nil {
			status.CurrentTask = a.CurrentTask.ID
		}
		if deleted {
			status.Health = AgentHealthDeleted
			status.State = "deleted"
			status.Working = false
		} else if status.CurrentTask != "" {
			// The live run, when there is one: the turn header's run_id is what a
			// "working" badge and its run id come from.
			if turn, err := r.conversationStore().ActiveReasonTurn(status.CurrentTask, a.ID); err == nil && turn != nil {
				status.Working = true
				status.AgentRunID = turn.RunID
				if status.State == "" || status.State == "idle" {
					status.State = "running"
				}
			}
		}
		agents = append(agents, status)
	}
	return &AgentStatusListResponse{
		Agents:      agents,
		Count:       len(agents),
		GeneratedAt: time.Now().UTC(),
	}, nil
}
