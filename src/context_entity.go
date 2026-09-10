package autonomy

import (
	"fmt"
	"time"
)

// ContextContainer is a entity that is used to anchor/mount the context of the task in a real world context, which is
// essential for the agent to understand the task and the real world context.
// it can be a project, a team, a user.
// it greatly release burden for a task to enter into the automony.
type ContextContainer struct {
	ID                   string
	Name                 string
	Description          string
	DomainType           TaskDomain
	ContextContainerType ContextContainerType
	CreatedAt            time.Time
	UpdatedAt            time.Time

	ContextReferences []string
	EntityReferences  []string
	AssetReferences   []string
}

type ContextContainerType string

const (
	ContextContainerTypeProject ContextContainerType = "project"
	ContextContainerTypeTeam    ContextContainerType = "team"
)

type ContextDomainMapping struct {
	Doamin           TaskDomain
	ContextDomainID  int
	ContextEntityID  int
	DomainEntityID   int
	DomainEntityType DomainEntityType
}

func ConvertToContextContainerType(externalContainerType string) (ContextContainerType, error) {
	switch externalContainerType {
	case "project":
		return ContextContainerTypeProject, nil
	case "team":
		return ContextContainerTypeTeam, nil
	default:
		return ContextContainerTypeProject, fmt.Errorf("invalid external entity type: %s", externalContainerType)
	}
}

// ContextEntity is a concrete context implementation for a context entity.
// it is different from context_entity, which is a global context container for a task
type ContextEntity struct {
	ID                   string
	Name                 string
	Description          string
	DomainType           TaskDomain
	ContextContainerType ContextContainerType
	CreatedAt            time.Time
	UpdatedAt            time.Time
}
