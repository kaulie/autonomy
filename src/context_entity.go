package autonomy

import "time"

// ContextEntity is a entity that is used to anchor/mount the context of the task in a real world context, which is
// essential for the agent to understand the task and the real world context.
// it can be a project, a team, a user.
// it greatly release burden for a task to enter into the automony.
type ContextEntity struct {
	ID                string
	Name              string
	Description       string
	DomainType        TaskDomain
	ContextEntityType ContextEntityType
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type ContextEntityType string

const (
	ContextEntityTypeProject ContextEntityType = "project"
	ContextEntityTypeTeam    ContextEntityType = "team"
)

type ContextDomainMapping struct {
	Doamin           TaskDomain
	ContextDomainID  int
	ContextEntityID  int
	DomainEntityID   int
	DomainEntityType DomainEntityType
}
