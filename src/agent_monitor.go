package autonomy

import (
	"fmt"
	"time"
)

// The monitoring panel's own vocabulary for what an agent is doing right now. It
// is a projection of the facts the runtime already holds (the agent row's state,
// the in-flight reason turn, the inbox backlog, the task's status, whether the
// agent was let go) onto the four states a person watching the system cares
// about — not a second source of truth.
const (
	// AgentMonitorRunning: the agent is in a run right now (its runtime state is
	// running, or it has an in-flight reason turn).
	AgentMonitorRunning = "running"
	// AgentMonitorIdle: the agent is live and has nothing to do — no run, no
	// queued message, and no task conclusion that says otherwise.
	AgentMonitorIdle = "idle"
	// AgentMonitorBlocked: the agent is not running but work is waiting for it
	// that it cannot proceed on — messages queued behind it, or a current task
	// the runtime itself concluded is blocked / need_input.
	AgentMonitorBlocked = "blocked"
	// AgentMonitorDone: the agent is finished — it was let go (deleted_at), or
	// its current task ended and nothing is behind it.
	AgentMonitorDone = "done"
)

// AgentMonitorSourceAutonomy names this runtime as the origin of an entry in the
// aggregation. The API is shaped so other sources (the agent-control-plane) can
// be merged in behind the same shape later without the panel changing.
const AgentMonitorSourceAutonomy = "autonomy"

// The SSE stream's push interval bounds (GET /api/agents/stream?interval=N,
// seconds): the default is one frame every two seconds — live enough to watch,
// cheap enough not to hammer the store — and the bounds keep a caller from
// asking for a flood or a stall.
const (
	DefaultAgentMonitorInterval = 2
	MaxAgentMonitorInterval     = 300
)

// AgentMonitorEntry is one agent as the monitoring panel shows it: identity,
// what it is (role / backend / model / lifecycle), what it is doing (status and
// the task it is on), and the two liveness facts a person watching needs — when
// it last reported in and where its sandbox is.
type AgentMonitorEntry struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Role   string `json:"role,omitempty"`
	Source string `json:"source"`
	// Backend / Model / LLMProvider are the agent's backing: backend is how it is
	// run (local / cursor / cline), provider is the LLM vendor, model is the
	// model in use.
	Backend     string `json:"backend,omitempty"`
	LLMProvider string `json:"llm_provider,omitempty"`
	Model       string `json:"model,omitempty"`
	Lifecycle   string `json:"lifecycle"`
	// Status is one of running | idle | blocked | done (the panel's vocabulary).
	Status string `json:"status"`
	// State is the raw runtime state the row carries (idle / running / deleted…),
	// kept for the detail view so the projection is inspectable.
	State string `json:"state,omitempty"`
	// CurrentTask is the task the agent owns right now, when it has one; TaskStatus
	// is that task's own status.
	CurrentTask string `json:"current_task,omitempty"`
	TaskStatus  string `json:"task_status,omitempty"`
	// LastHeartbeat is the last time the runtime wrote this agent's row
	// (agents.updated_at): the freshest liveness fact the store holds.
	LastHeartbeat time.Time `json:"last_heartbeat"`
	Workspace     string    `json:"workspace,omitempty"`
	// QueuedMessages is the backlog waiting for the agent (inbox messages it has
	// not started); ActiveRunID is the run it is on, when it is on one.
	QueuedMessages int    `json:"queued_messages,omitempty"`
	ActiveRunID    string `json:"active_run_id,omitempty"`
	DeletedAt      string `json:"deleted_at,omitempty"`
}

// AgentMonitorSummary is the tally the panel renders as its header: how many
// agents are in each state. Total is their sum.
type AgentMonitorSummary struct {
	Total   int `json:"total"`
	Running int `json:"running"`
	Idle    int `json:"idle"`
	Blocked int `json:"blocked"`
	Done    int `json:"done"`
}

// AgentMonitorResponse is the whole aggregation: when it was taken, the tally,
// which sources answered, and one entry per agent.
type AgentMonitorResponse struct {
	GeneratedAt time.Time           `json:"generated_at"`
	Summary     AgentMonitorSummary `json:"summary"`
	Sources     []string            `json:"sources"`
	Agents      []AgentMonitorEntry `json:"agents"`
}

// AgentsOverview aggregates every agent this runtime knows about into the
// snapshot the monitoring panel renders (GET /api/agents). It is read-only and
// builds from facts the runtime already holds, so it adds no new state:
//
//   - the persisted agent rows (agents table) are the authoritative set — id,
//     name, lifecycle, state, provider, model, current task, updated_at;
//   - the in-memory factory adds what is runtime-only and never persisted —
//     role, backend and the sandbox workspace — for the agents this process
//     actually holds;
//   - the tasks table says which agents are planners and how a task ended;
//   - the inbox says whether work is queued behind an agent;
//   - an in-flight reason turn is what "running right now" really means.
//
// The per-agent projection onto running | idle | blocked | done is documented on
// the status constants above.
func (r *Autonomy) AgentsOverview() (*AgentMonitorResponse, error) {
	if r == nil || r.Store == nil {
		return nil, fmt.Errorf("store not ready")
	}
	rows, err := r.agentStore().ListAgents()
	if err != nil {
		return nil, err
	}

	// Live runtime agents by id: role, backend and workspace live here and are
	// not on the row.
	live := map[int64]*Agent{}
	if r.AgentFactory != nil {
		for _, a := range r.AgentFactory.snapshot() {
			if a != nil && a.ID != 0 {
				live[a.ID] = a
			}
		}
	}

	// Tasks: each task's status, and which agents are planners (a task names its
	// own agent). Both are best-effort — the panel still answers with the agent
	// rows when the task read fails.
	taskStatus := map[string]string{}
	plannerIDs := map[int64]bool{}
	if tasks, err := r.taskStore().ListTasks(); err == nil {
		for _, t := range tasks {
			if t == nil {
				continue
			}
			taskStatus[t.ID] = t.Status
			if t.AgentID != 0 {
				plannerIDs[t.AgentID] = true
			}
		}
	}

	resp := &AgentMonitorResponse{
		GeneratedAt: time.Now().UTC(),
		Sources:     []string{AgentMonitorSourceAutonomy},
		Agents:      make([]AgentMonitorEntry, 0, len(rows)),
	}
	for _, a := range rows {
		if a == nil {
			continue
		}
		entry := r.monitorEntry(a, live[a.ID], taskStatus, plannerIDs)
		resp.Agents = append(resp.Agents, entry)
		switch entry.Status {
		case AgentMonitorRunning:
			resp.Summary.Running++
		case AgentMonitorIdle:
			resp.Summary.Idle++
		case AgentMonitorBlocked:
			resp.Summary.Blocked++
		case AgentMonitorDone:
			resp.Summary.Done++
		}
	}
	resp.Summary.Total = len(resp.Agents)
	return resp, nil
}

// monitorEntry projects one stored agent row (plus the live handle for it, when
// this process holds one) onto a panel entry. It never fails: whatever it cannot
// read it leaves at its zero value, because a monitoring read must still answer
// when one of its inputs is missing.
func (r *Autonomy) monitorEntry(a, live *Agent, taskStatus map[string]string, plannerIDs map[int64]bool) AgentMonitorEntry {
	entry := AgentMonitorEntry{
		ID:            a.ID,
		Name:          a.Name,
		Lifecycle:     string(a.Lifecycle),
		State:         a.State,
		Source:        AgentMonitorSourceAutonomy,
		LLMProvider:   string(a.LLMProvider),
		Model:         a.Model,
		LastHeartbeat: a.UpdatedAt,
		Workspace:     AgentWorkspacePath(a.Name),
	}
	if a.CurrentTask != nil {
		entry.CurrentTask = a.CurrentTask.ID
		entry.TaskStatus = taskStatus[a.CurrentTask.ID]
	}
	if !a.DeletedAt.IsZero() {
		entry.DeletedAt = a.DeletedAt.UTC().Format(time.RFC3339)
	}

	// Role / backend / workspace: the live handle knows them; otherwise fall back
	// to what the row implies (a task's agent is a planner; the provider names a
	// backend).
	if live != nil {
		if live.Role != "" {
			entry.Role = string(live.Role)
		}
		if live.Backend != "" {
			entry.Backend = string(live.Backend)
		}
		if live.Workspace != "" {
			entry.Workspace = live.Workspace
		}
		if entry.Model == "" {
			entry.Model = live.Model
		}
		if entry.State == "" {
			entry.State = live.State
		}
	}
	if entry.Role == "" {
		if plannerIDs[a.ID] {
			entry.Role = string(AgentRolePlanner)
		} else {
			entry.Role = string(AgentRoleWorker)
		}
	}
	if entry.Backend == "" {
		entry.Backend = string(backendForProvider(a.LLMProvider))
	}

	// Running right now: the row's state, the live handle's state, or an
	// in-flight reason turn on the agent's task.
	working := a.State == TaskStatusRunning || (live != nil && live.State == TaskStatusRunning)
	if entry.CurrentTask != "" {
		if turn, err := r.conversationStore().ActiveReasonTurn(entry.CurrentTask, a.ID); err == nil && turn != nil {
			working = true
			entry.ActiveRunID = turn.RunID
		}
	}

	// The backlog waiting for this agent (best-effort).
	if n, err := r.Store.CountQueuedMessages(a.ID); err == nil {
		entry.QueuedMessages = n
	}

	switch {
	case entry.DeletedAt != "":
		entry.Status = AgentMonitorDone
	case working:
		entry.Status = AgentMonitorRunning
	case entry.QueuedMessages > 0:
		entry.Status = AgentMonitorBlocked
	case entry.TaskStatus == TaskStatusBlocked || entry.TaskStatus == TaskStatusNeedInput:
		entry.Status = AgentMonitorBlocked
	case isTerminalTaskStatus(entry.TaskStatus):
		entry.Status = AgentMonitorDone
	default:
		entry.Status = AgentMonitorIdle
	}
	return entry
}

// isTerminalTaskStatus reports whether a task status is an ending — the run
// concluded (however it concluded) and nothing continues it on its own.
func isTerminalTaskStatus(status string) bool {
	switch status {
	case TaskStatusCompleted, TaskStatusStopped, TaskStatusError, TaskStatusUnverified:
		return true
	default:
		return false
	}
}

// backendForProvider maps an LLM provider onto the runtime backend that drives
// it, for a stored agent whose live handle this process does not hold. A local
// agent has no provider, so it is the local backend.
func backendForProvider(p LLMProvider) AgentBackend {
	switch p {
	case LLMProviderCursor:
		return AgentBackendCursor
	case LLMProviderCline:
		return AgentBackendCline
	default:
		return AgentBackendLocal
	}
}
