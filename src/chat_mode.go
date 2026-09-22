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
)

// parseUserMessageKind maps AcceptTaskRequest.Mode onto an inbox kind.
// Empty / "command" stay the old instruction; "chat" is planner conversation;
// anything else is a caller error (HTTP 400).
func parseUserMessageKind(mode string) (AgentMessageKind, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", UserMessageModeCommand:
		return MessageKindInstruction, nil
	case UserMessageModeChat:
		return MessageKindChat, nil
	default:
		return "", fmt.Errorf(`mode must be "chat" or "command"`)
	}
}

// chatPlannerInput is what the planner sees for a chat message: the user's
// words plus a hard instruction that this turn must not touch the plan.
func chatPlannerInput(user string) string {
	text := strings.TrimSpace(user)
	return "CHAT MODE — planner conversation only. Reply to the user in reason/need. " +
		"Do not create, revise, replace, or execute any plan. The existing plan must stay exactly as it is. " +
		"Never return type plan or done.\n\nUser: " + text
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
	}
	agent.Start()
	persistAgent(agent)

	// One planner turn so the user can talk. Whatever it answers — including a
	// type:plan the model should not have returned — is dropped: no Execute,
	// no recordPlan, no status change.
	_, err := agent.decide(ctx, 1, nil, chatPlannerInput(msg.Content))
	return TurnResult{}, err
}
