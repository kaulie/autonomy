package autonomy

import (
	"context"
	"fmt"
	"strings"
)

const (
	// UserMessageModeCommand is a follow-up that may replan and execute
	// (inbox kind = instruction). Omitted mode means this.
	UserMessageModeCommand = "command"
	// UserMessageModeChat talks to the planner only and must not change
	// an already written plan (inbox kind = chat).
	UserMessageModeChat = "chat"
	// UserMessageModeApprove confirms a plan the agent is waiting on; it is the
	// mode spelling of AcceptTaskRequest.Approve (inbox kind = approval).
	UserMessageModeApprove = "approve"
)

// parseUserMessageKind maps AcceptTaskRequest.Mode onto an inbox kind.
// Empty / "command" stay the old instruction; "chat" is planner conversation;
// "approve" is a confirmation of a pending plan; anything else is a caller error
// (HTTP 400).
func parseUserMessageKind(mode string) (AgentMessageKind, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", UserMessageModeCommand:
		return MessageKindInstruction, nil
	case UserMessageModeChat:
		return MessageKindChat, nil
	case UserMessageModeApprove:
		return MessageKindApproval, nil
	default:
		return "", fmt.Errorf(`mode must be "chat", "command" or "approve"`)
	}
}

// chatPlannerInput is what the planner sees for a chat message: the user's words plus
// the hard instruction that this turn must not touch the plan. That instruction is a **file**
// (src/agent_policy/CHAT_MODE.md, docs/prompt.md); unreadable means this turn cannot be run
// safely, so it reports that instead of answering without the guard.
func chatPlannerInput(user string) (string, error) {
	text := strings.TrimSpace(user)
	guard, err := loadPromptFile(chatModeRel)
	if err != nil {
		return "", fmt.Errorf("chat mode prompt: %w", err)
	}
	return strings.TrimSpace(guard) + "\n\nUser: " + text, nil
}

// processChat is one chat message: the planner may answer, but the runtime
// never writes a new execution plan, never executes steps, and never changes
// the task's status. The already produced plan is left exactly as it is.
func (r *Autonomy) processChat(ctx context.Context, cancel context.CancelFunc, agent *Agent, msg AgentMessage) (TurnResult, error) {
	task := r.taskForMessage(msg)
	if task == nil {
		return TurnResult{}, fmt.Errorf("chat for an unknown task: %q", msg.TaskID)
	}
	prevStatus, prevError := task.Status, task.Error
	defer func() {
		if ctx.Err() != nil {
			markStopped(task)
			return
		}
		task.Status = prevStatus
		task.Error = prevError
		persistTask(task)
	}()

	if agent.Session == nil || agent.Session.agent == nil || agent.Session.taskID != task.ID {
		agent.Session = NewLLMSession(r.Runtime, agent, SessionOpts{TaskID: task.ID})
	}
	if cancel != nil {
		inFlightTasks.Store(task.ID, cancel)
		defer inFlightTasks.Delete(task.ID)
		defer clearStopReason(task.ID)
	}
	agent.Start()
	persistAgent(agent)

	// One planner turn so the user can talk. Whatever it answers — including a
	// type:plan the model should not have returned — is dropped: no Execute,
	// no recordPlan, no status change.
	//
	// The planner's view of its task is the same here as in a decision cycle: it
	// answers with the task's own record in front of it (src/task_record.go), so
	// "what have you done so far?" is answered from the record rather than from a
	// session that a restart may have taken away.
	brief := r.taskBriefing(task.ID)
	input, err := chatPlannerInput(msg.Content)
	if err != nil {
		return TurnResult{}, err
	}
	_, err = agent.decide(ctx, 1, nil, input, brief)
	return TurnResult{}, err
}
