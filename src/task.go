package autonomy

import "time"

// Task is the work contract: what to achieve and how completion is judged.
// It defines goal, not path.
type Task struct {
	ID          string
	Description string
	Domain      TaskDomain
	Context     string
	Target      string
	Goal        string
	Contract    Contract //completion contract for verification
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type TaskDomain string

const (
	TaskDomainAI       TaskDomain = "ai"
	TaskDomainWeb      TaskDomain = "web"
	TaskDomainMobile   TaskDomain = "mobile"
	TaskDomainDesktop  TaskDomain = "desktop"
	TaskDomainServer   TaskDomain = "server"
	TaskDomainDatabase TaskDomain = "database"
)

// Contract is the completion anchor. Agents may choose any path; they may not rewrite this.
type Contract struct {
	ExpectedState string
}

// TaskResult is the outcome of running a task through Autonomy.
type TaskResult struct {
	Task    Task
	Err     error
	History []Result
}
