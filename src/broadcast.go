package autonomy

import (
	"fmt"
	"strings"
)

// A broadcast is one sentence said to many agents at once: the agents of one
// project, or of every project (docs/broadcast.md).
//
// It is not a new kind of thing — it is the plural of one instruction. Every
// target gets the very message POST /api/tasks would give it: the user's
// instruction for that target's own task (`instruction`, see docs/inbox.md),
// built on the same accept path (Autonomy.instruction) and queued in that agent's
// inbox, where its own consumer processes it one message at a time. What a
// broadcast adds is that it happens to several targets in one call, and that it
// answers with one line per target.
//
// "The agents of a project" is the owner agents of that project's tasks: which
// project a task belongs to is on its own row (`tasks.context_ref`,
// `{"project": "<id>"}`) and which agent is responsible for that task is
// `tasks.agent_id` — so a scope is resolved task first, and a task yields its
// agent. A worker acquired by a capability (Runtime.AcquireAgent) is not one of a
// project's agents: it is the execution detail of a single task, and what a
// broadcast addresses is that task's owner (docs/agent.md).
//
// The message a task is given is *about that task*, so a target that cannot be
// given one — a task with no agent, or one whose agent was let go — is skipped
// and said so rather than revived for the occasion: a broadcast hands a message
// to the agents that are there, it does not create agents to hand it to.

// The two scopes a broadcast can run with: one project, or every project. The
// scope is always one of them and always named in the request — "nobody said
// which project" is not the same request as "every project", and the second one
// is too wide to be what an omission means.
const (
	// BroadcastScopeProject is a broadcast limited to one project's agents
	// (BroadcastRequest.ProjectID).
	BroadcastScopeProject = "project"
	// BroadcastScopeAll is a broadcast to every project's agents
	// (BroadcastRequest.AllProjects).
	BroadcastScopeAll = "all"
)

// How one target's delivery ended (BroadcastDelivery.Status).
const (
	// BroadcastDelivered: the message is in that agent's inbox.
	BroadcastDelivered = "delivered"
	// BroadcastSkipped: this target was not delivered to, and the reason says
	// why — a fact about the target (its task has no agent, or its agent was let
	// go), not a failure of the delivery.
	BroadcastSkipped = "skipped"
	// BroadcastFailed: the delivery itself failed, with the runtime's own reason.
	// The other targets are unaffected: a broadcast finishes its rounds, it does
	// not roll back.
	BroadcastFailed = "failed"
)

// BroadcastRequest is the body of POST /api/broadcast: what to say, and to whom.
//
// The scope has to be said out loud — one project (ProjectID) or every project
// (AllProjects) — because a broadcast that forgot to name a project and a
// broadcast meant for the whole runtime are not the same request, and only one of
// them is safe to guess.
type BroadcastRequest struct {
	// Content is the message itself, said to every target verbatim: the same
	// words each target's agent receives as the input of the run it takes.
	Content string `json:"content"`
	// ProjectID limits the broadcast to the agents of one project.
	ProjectID string `json:"project_id,omitempty"`
	// AllProjects is the scope "every project", said explicitly.
	AllProjects bool `json:"all_projects,omitempty"`
}

// BroadcastDelivery is one target and what became of it: the task that was
// addressed, the agent that owns it, and where the message went — or why none
// did.
type BroadcastDelivery struct {
	TaskID string `json:"task_id"`
	// AgentID is the agent that task is paired with, when it still has one.
	AgentID int64 `json:"agent_id,omitempty"`
	// ProjectID is the project the target was found in: the one the task names
	// (empty for a task that names none, which only an "every project" broadcast
	// can reach).
	ProjectID string `json:"project_id,omitempty"`
	// Status is delivered | skipped | failed.
	Status string `json:"status"`
	// MessageID is the inbox row this message was written as, and Queued is how
	// many messages that agent still has in front of it — the same pair
	// POST /api/tasks answers with (0 means this message is what the agent is on,
	// or is about to take next; see docs/inbox.md).
	MessageID int64 `json:"message_id,omitempty"`
	Queued    int   `json:"queued,omitempty"`
	// Reason is why this target was skipped or failed: the runtime's own words.
	Reason string `json:"reason,omitempty"`
}

// BroadcastResponse is the report of one broadcast: the scope it ran with, how
// the targets came out, and one line per target.
type BroadcastResponse struct {
	// Scope is project (one project: ProjectID) or all (every project).
	Scope     string `json:"scope"`
	ProjectID string `json:"project_id,omitempty"`
	// Targets is how many tasks the scope resolved to; Delivered, Skipped and
	// Failed add up to it.
	Targets   int `json:"targets"`
	Delivered int `json:"delivered"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
	// Deliveries is one entry per target, in the order the scope found them (a
	// project's tasks, oldest first).
	Deliveries []BroadcastDelivery `json:"deliveries"`
}

// Broadcast delivers one message to many agents at once: the agents of one
// project, or of every project. It is the plural of AcceptTask — every target
// gets the instruction an HTTP accept would give it, and this call answers with
// where each one landed.
//
// Nothing is waited for: the message is in each agent's queue when this returns,
// and each agent processes it in its own time, in the order its messages arrived
// (an agent that is busy takes the broadcast behind what it is already doing —
// instructions are continuously acceptable). A target that could not be delivered
// to is reported, and does not stop the rest.
func (r *Autonomy) Broadcast(req BroadcastRequest) (*BroadcastResponse, error) {
	if r == nil || r.Store == nil {
		return nil, fmt.Errorf("store not ready")
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		return nil, fmt.Errorf("content is required")
	}
	projectID := strings.TrimSpace(req.ProjectID)
	switch {
	case projectID != "" && req.AllProjects:
		return nil, fmt.Errorf("a broadcast names one scope: project_id %q or all_projects, not both", projectID)
	case projectID == "" && !req.AllProjects:
		return nil, fmt.Errorf("a broadcast needs a scope: project_id (one project) or all_projects=true (every project)")
	}

	targets, err := r.broadcastTargets(projectID)
	if err != nil {
		return nil, err
	}
	resp := &BroadcastResponse{
		Scope:      BroadcastScopeAll,
		Targets:    len(targets),
		Deliveries: make([]BroadcastDelivery, 0, len(targets)),
	}
	if projectID != "" {
		resp.Scope = BroadcastScopeProject
		resp.ProjectID = projectID
	}
	for _, target := range targets {
		delivery := r.deliverBroadcast(target, content)
		switch delivery.Status {
		case BroadcastDelivered:
			resp.Delivered++
		case BroadcastSkipped:
			resp.Skipped++
		default:
			resp.Failed++
		}
		resp.Deliveries = append(resp.Deliveries, delivery)
	}
	return resp, nil
}

// broadcastTarget is one task a broadcast's scope resolved to, with what is known
// about delivering to it before anything is written.
type broadcastTarget struct {
	Task *Task
	// AgentID is the agent the task is paired with (tasks.agent_id), 0 when it
	// has none.
	AgentID int64
	// ProjectID is the project the task names.
	ProjectID string
	// Skip is why this target gets nothing. It is resolved with the scope rather
	// than discovered mid-delivery, because "this task has no agent" is a fact
	// about the target and not a failure of the delivery.
	Skip string
}

// broadcastTargets resolves a scope to targets: every task (of one project, or of
// every project), each with the agent it is paired with. It is the store that is
// asked what exists — the project a task names is read off the task's own row, so
// a task is in a project because it says so, not because a request says so.
func (r *Autonomy) broadcastTargets(projectID string) ([]broadcastTarget, error) {
	tasks, err := r.taskStore().ListTasks()
	if err != nil {
		return nil, err
	}
	targets := make([]broadcastTarget, 0, len(tasks))
	for _, task := range tasks {
		project := projectRefOf(task)
		if projectID != "" && project != projectID {
			continue
		}
		targets = append(targets, r.broadcastTarget(task, project))
	}
	return targets, nil
}

// broadcastTarget decides whether a task can be delivered to, and why not when it
// cannot: a task with no agent, or one whose agent was let go, is skipped — it is
// not given a new agent just because someone broadcast into its project.
func (r *Autonomy) broadcastTarget(task *Task, projectID string) broadcastTarget {
	target := broadcastTarget{Task: task, AgentID: task.AgentID, ProjectID: projectID}
	if task.AgentID == 0 {
		target.Skip = "the task has no agent"
		return target
	}
	agent, err := r.agentStore().GetAgent(task.AgentID)
	switch {
	case err != nil:
		target.Skip = fmt.Sprintf("read agent %d: %v", task.AgentID, err)
	case agent == nil:
		target.Skip = fmt.Sprintf("agent %d is not in the store", task.AgentID)
	case !agent.DeletedAt.IsZero():
		target.Skip = fmt.Sprintf("agent %d was let go", task.AgentID)
	}
	return target
}

// deliverBroadcast is one target's delivery. The message is built on the very
// path an HTTP instruction takes (Autonomy.instruction: the task's own agent,
// resumed when this process does not hold it yet) and queued in that agent's
// inbox — so what a target receives from a broadcast is indistinguishable from
// what it would receive from POST /api/tasks, which is the point.
//
// The accept path writes the task as pending, exactly as it does for a single
// instruction: every target of a broadcast has just been handed an instruction,
// and that is what pending says.
func (r *Autonomy) deliverBroadcast(target broadcastTarget, content string) BroadcastDelivery {
	delivery := BroadcastDelivery{
		TaskID:    target.Task.ID,
		AgentID:   target.AgentID,
		ProjectID: target.ProjectID,
		Status:    BroadcastDelivered,
	}
	if target.Skip != "" {
		delivery.Status = BroadcastSkipped
		delivery.Reason = target.Skip
		return delivery
	}
	agent, msg, err := r.instruction(target.Task, content)
	if err != nil {
		delivery.Status = BroadcastFailed
		delivery.Reason = err.Error()
		return delivery
	}
	inbox := r.agentInbox()
	id, err := inbox.Enqueue(agent, msg)
	if err != nil {
		delivery.Status = BroadcastFailed
		delivery.Reason = err.Error()
		return delivery
	}
	// The agent the task is actually paired with (a resume may have rebuilt the
	// handle) and this message's place in its queue.
	delivery.AgentID = agent.ID
	delivery.MessageID = id
	delivery.Queued = inbox.Ahead(agent, id)
	return delivery
}
