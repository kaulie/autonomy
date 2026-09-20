package autonomy

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// This file is the task ↔ agent pairing across process lifetimes. A task's own
// agent is the one whose decisions are that task's cycles, and it outlives the
// run that created it: the row it was given is where a later instruction starts
// from.
//
// One instruction, end to end (Autonomy.Run):
//
//	this process already holds the task's agent (AgentFactory.ForTask)
//	      → the same handle, and so the same conversation
//	the task row names an agent (tasks.agent_id) and that row is live
//	      → rebuild the handle from the row and register it (AgentFactory.Adopt)
//	nothing to resume (the first instruction for this task)
//	      → AgentFactory.Create, a new planner agent
//
// Opening the provider session is not on that list: it belongs to the turn that
// needs it (LLMSession.Say → ensureLLMSession, idempotent). Accepting an
// instruction is queueing it — one accept per instruction, and a broadcast is one
// accept per agent it reaches — so it must not wait on a bridge, and a bridge that
// will not answer must fail the run rather than refuse the instruction.
//
// Agents being kept by default (AgentLifecyclePersistent — see NewAgent) is what
// makes the middle branch reachable at all: an agent that was deleted when its
// run ended is one no instruction can come back to.

// resumeAgentForTask returns the agent this task's cycles belong to, resuming the
// one it was paired with when a previous instruction (in this process) or a
// previous process created it.
//
// What it hands back is that agent's record — its row, its task, the provider
// session it was recorded with — and no live provider session: this is the accept
// path, and opening a session here would make POST /api/tasks (and every accept a
// broadcast fans out) wait on the bridge. The first cycle of the run attaches,
// on the run's own context, where a bridge that cannot be reached is the run's
// failure and a session that has since expired becomes a fresh one
// (resumeCursorSession, attachClineMode).
func (r *Autonomy) resumeAgentForTask(task *Task) (*Agent, error) {
	if r == nil || r.AgentFactory == nil {
		return nil, fmt.Errorf("agent factory not ready")
	}
	if task == nil {
		return nil, fmt.Errorf("nil task")
	}
	if agent := r.AgentFactory.ForTask(task.ID); agent != nil {
		agent.CurrentTask = task
		return agent, nil
	}
	store := r.Store
	if store == nil {
		store = activeStore()
	}
	stored, err := storedAgentForTask(store, task)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return r.AgentFactory.Create(task), nil
	}
	// The task keeps the pairing it was found by, in memory as well as in its row:
	// every writer downstream (the loop, a stop, a failure) upserts this Task value.
	task.AgentID = stored.ID
	agent := r.AgentFactory.Adopt(restoredAgent(stored, task))
	if agent == nil {
		return nil, fmt.Errorf("adopt agent %d for task %s", stored.ID, task.ID)
	}
	if stored.LLMAgentID != "" {
		fmt.Fprintf(os.Stderr, "[autonomy] agent %s (%s) continues task %s on the session %s it was left with\n",
			agent.Name, agent.effectiveBackend(), task.ID, stored.LLMAgentID)
	} else {
		fmt.Fprintf(os.Stderr, "[autonomy] agent %s (%s) continues task %s on a new session\n",
			agent.Name, agent.effectiveBackend(), task.ID)
	}
	return agent, nil
}

// storedAgentForTask is the agent a task is paired with, as the store has it: the
// id the task's own row carries, read back as an agent row. It returns nil when
// the task is new, when its row names no agent, and when the agent it names was
// let go — a deleted agent is not something to resume, so the task gets a new one
// instead.
func storedAgentForTask(store Store, task *Task) (*Agent, error) {
	if store == nil || task == nil || strings.TrimSpace(task.ID) == "" {
		return nil, nil
	}
	agentID := task.AgentID
	if agentID == 0 {
		storedTask, err := store.GetTask(task.ID)
		if err != nil {
			return nil, err
		}
		if storedTask == nil {
			return nil, nil
		}
		agentID = storedTask.AgentID
	}
	if agentID == 0 {
		return nil, nil
	}
	agent, err := store.GetAgent(agentID)
	if err != nil {
		return nil, err
	}
	if agent == nil || !agent.DeletedAt.IsZero() {
		return nil, nil
	}
	return agent, nil
}

// restoredAgent is the runtime handle for an agent row: the same identity (id,
// name, provider, model, the provider session it was recorded with) with the
// runtime state a process has to make for itself left to be made — the provider
// handle and the live session are exactly what a restart has to re-establish, and
// the turn that needs them does (resumeCursorSession / attachClineMode), not the
// accept that read this row.
//
// The lifecycle is this runtime's policy rather than the row's: an agent is kept,
// which is what a task needs to be continuable. A row written by a build that
// deleted agents at the end of a run still says `ephemeral`; that is a record of
// that build's policy, not an instruction to this one.
func restoredAgent(stored *Agent, task *Task) *Agent {
	return &Agent{
		ID:          stored.ID,
		Name:        stored.Name,
		State:       "idle",
		Lifecycle:   AgentLifecyclePersistent,
		LLMProvider: stored.LLMProvider,
		Model:       stored.Model,
		LLMAgentID:  stored.LLMAgentID,
		CurrentTask: task,
		Role:        AgentRolePlanner,
	}
}

// resumeCursorSession opens this agent's Cursor session for the turn that needs it:
// re-attaching the provider agent it was recorded with, and — when the provider no
// longer has it — continuing on a fresh session instead of failing, because a
// session that expired while the runtime was down is a reason to start talking
// again, not a reason to give up the task it is here to continue.
func (a *Agent) resumeCursorSession(ctx context.Context) (bool, error) {
	model := a.Model
	if model == "" {
		model = defaultCursorModel()
	}
	resumed := a.LLMAgentID != "" && !a.IsEphemeral()
	err := a.AttachCursor(ctx, model)
	if err == nil {
		return resumed, nil
	}
	if !resumed {
		return false, err
	}
	fmt.Fprintf(os.Stderr, "[autonomy] cursor session %s of agent %s is gone (%v); starting a new one\n",
		a.LLMAgentID, a.Name, err)
	a.LLMAgentID = ""
	return false, a.AttachCursor(ctx, model)
}
