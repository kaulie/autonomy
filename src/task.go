package autonomy

import (
	"fmt"
	"time"
)

// Task is the work contract: what to achieve and how completion is judged.
// It defines goal, not path.
type Task struct {
	ID          string
	Description string
	Domain      TaskDomain
	Context     string
	GoalType    GoalType
	Status      string
	AgentID     int64 // current agent responsible for executing this task
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ContextRef  map[ContextEntityType]string // context references
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
