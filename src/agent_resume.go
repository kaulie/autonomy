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
//	      → rebuild the handle from the row, register it (AgentFactory.Adopt),
//	        and re-attach its provider session (resumeAgentSession)
//	nothing to resume (the first instruction for this task)
//	      → AgentFactory.Create, a new planner agent
//
// Agents being kept by default (AgentLifecyclePersistent — see NewAgent) is what
// makes the middle branch reachable at all: an agent that was deleted when its
// run ended is one no instruction can come back to.

// resumeAgentForTask returns the agent this task's cycles belong to, resuming the
// one it was paired with when a previous instruction (in this process) or a
// previous process created it.
//
// Only an agent rebuilt from the store is attached here. A freshly created one
// still attaches on its first turn (LLMSession.Say), which is what lets a run
// with no provider at all — the local reasoner — take its cycles.
func (r *Autonomy) resumeAgentForTask(ctx context.Context, task *Task) (*Agent, error) {
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
	resumed, err := agent.resumeAgentSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("resume agent %s for task %s: %w", agent.Name, task.ID, err)
	}
	if resumed {
		fmt.Fprintf(os.Stderr, "[autonomy] resumed agent %s (%s session %s) on task %s\n",
			agent.Name, agent.effectiveBackend(), agent.LLMAgentID, task.ID)
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
// handle and the session are exactly what a restart has to re-establish, and
// resumeAgentSession does.
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

// resumeAgentSession re-opens this agent's provider session after a restart, and
// reports whether the backend resumed the session the agent was recorded with. A
// false means a fresh session, which has to be told the agent's frame again (see
// Agent.needsLLMFrame) — so the frame is reset here for the callers that only look
// at the error.
func (a *Agent) resumeAgentSession(ctx context.Context) (bool, error) {
	if a == nil {
		return false, fmt.Errorf("nil agent")
	}
	switch a.effectiveBackend() {
	case AgentBackendCline:
		// The Cline bridge keeps its sessions inside its own process and has no way
		// to re-attach to one, so a restarted runtime continues this agent on a
		// fresh session. What carries over is the agent itself — its row, its task,
		// its workspace — and the conversation it already had is on the record
		// (llm_messages), which the next turn is written against.
		//
		// The session is opened in the mode this agent's turns run in (a planner's
		// cycles decide), because that is the one it will keep using.
		a.resetLLMFrame()
		mode := clineModeFor(ReasonModeAgent)
		if a.Role == AgentRolePlanner {
			mode = clineModeFor(ReasonModePlan)
		}
		return false, a.attachClineMode(ctx, mode)
	case AgentBackendLocal:
		// No provider session to open: a local agent's turns come from its host.
		return false, nil
	default:
		return a.resumeCursorSession(ctx)
	}
}

// resumeCursorSession re-attaches the Cursor agent this one was recorded with and
// — when the provider no longer has it — continues on a fresh session instead of
// failing: a session that expired while the runtime was down is a reason to start
// talking again, not a reason to give up the task it is here to continue.
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
