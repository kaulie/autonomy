package autonomy

import (
	"fmt"
	"time"
)

// Task statuses. `running` while a run is going, and afterwards whatever concluded it:
// `completed` when the decision was `done` (the goal satisfied, with its evidence),
// `blocked` / `need_input` when the concluding decision said the task cannot go on
// without something, `unverified` when the run ended with a `done` that never held up
// (verification refused it, docs/verification.md), and `error` when the run failed —
// with the reason in Error.
const (
	TaskStatusRunning    = "running"
	TaskStatusPending    = "pending"
	TaskStatusCompleted  = "completed"
	TaskStatusUnverified = "unverified"
	TaskStatusBlocked    = "blocked"
	TaskStatusNeedInput  = "need_input"
	TaskStatusError      = "error"
	// TaskStatusAwaitingApproval is a run paused on a plan the agent must get the
	// user to confirm before implementing (Agent.RequirePlanApproval): the plan is
	// on record, nothing has run, and a confirmation (AcceptTaskRequest.Approve)
	// releases it. It is a run waiting, not a task that ended.
	TaskStatusAwaitingApproval = "awaiting_approval"
	// TaskStatusStopped is a run cancelled by POST /api/tasks/{id}/stop. It is not
	// a planner answer (done / blocked / need_input) and not a failed action.
	TaskStatusStopped = "stopped"
)

// Task is the work contract: what to achieve and how completion is judged.
// It defines goal, not path.
type Task struct {
	ID          string
	Description string
	Domain      TaskDomain
	GoalType    GoalType
	Status      string
	// Error is why this task ended in status "error" or "unverified": the runtime's
	// own reason — a decide that failed, the last cycle's failing action, or the verdict
	// that kept the last `done` from holding up. It is recorded so the reason outlives
	// the process that printed it (`tasks.error`): a row that says "error" and nothing
	// else cannot be diagnosed later.
	Error      string
	AgentID    int64 // current agent responsible for executing this task
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ContextRef map[ContextContainerType]string // context references
}

type TaskDomain string

const (
	TaskDomainSoftwareDevelopment TaskDomain = "software_development"
	TaskDomainAI                  TaskDomain = "ai"
	TaskDomainWeb                 TaskDomain = "web"
	TaskDomainMobile              TaskDomain = "mobile"
	TaskDomainDesktop             TaskDomain = "desktop"
	TaskDomainServer              TaskDomain = "server"
	TaskDomainDatabase            TaskDomain = "database"
)

func ConvertToTaskDomain(externalDomain string) (TaskDomain, error) {
	switch externalDomain {
	case "software_development":
		return TaskDomainSoftwareDevelopment, nil
	case "ai":
		return TaskDomainAI, nil
	case "web":
		return TaskDomainWeb, nil
	case "mobile":
		return TaskDomainMobile, nil
	case "desktop":
		return TaskDomainDesktop, nil
	case "server":
		return TaskDomainServer, nil
	default:
		return TaskDomainSoftwareDevelopment, fmt.Errorf("invalid external domain: %s", externalDomain)
	}
}

// TaskResult is the outcome of running a task through Autonomy.
type TaskResult struct {
	Task    Task
	Err     error
	History []Result
}

type TaskType string

const (
	TaskTypeIssue   TaskType = "issue"
	TaskTypeFeature TaskType = "feature"
	TaskTypeDebug   TaskType = "debug"
	TaskTypeStory   TaskType = "story"
	TaskTypeTest    TaskType = "test"
)
