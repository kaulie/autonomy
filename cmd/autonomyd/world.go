package main

import (
	"time"

	autonomy "github.com/kaulie/autonomy/src"
)

// ExternalEntity is an entity as the world outside names it — a project, a team —
// before it becomes the runtime's Context.
type ExternalEntity struct {
	ID           string
	Name         string
	Description  string
	EntityType   string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	EntityDomain string
}

// Project and Team are the external kinds the demo maps onto a context container.
type Project struct{ ExternalEntity }

type Team struct{ ExternalEntity }

// seedDemoWorld registers the world the documented demo runs in: the project
// context (project-2) and, inside it, the repository the agent works on and the
// service it deploys. It is the world `go run ./cmd/autonomy` submits its default
// instruction against — that command used to register it and then run the task
// in-process; now the runtime runs the task, so the world it runs in is the
// runtime's own wiring. A caller can only name it: context_ref {"project":
// "project-2"}. See docs/context.md and docs/http-api.md.
func seedDemoWorld() error {
	project, err := contextContainer(ExternalEntity{
		ID:           "project-2",
		Name:         "Project 2",
		Description:  "Project 2 description",
		EntityType:   "project",
		EntityDomain: "software_development",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	})
	if err != nil {
		return err
	}
	if err := autonomy.RegisterContextContainer(project); err != nil {
		return err
	}

	sourceCode := autonomy.SourceCodeEntity{
		Meta: autonomy.Entity{
			ID:          "src-1",
			Name:        "source code",
			Description: "deployment project",
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		},
		Repository: autonomy.Repository{
			URL:        "https://github.com/kaulie/agent-control-plane-deployment",
			MainBranch: "main",
		},
	}
	service := autonomy.ServiceEntity{
		Meta: autonomy.Entity{
			ID:          "service-1",
			Name:        "service",
			Description: "service description",
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		},
		ServiceId:   "agent-control-plane-deployment",
		ServiceName: "agent-control-plane-deployment",
	}

	for _, entity := range []autonomy.DomainEntity{service, sourceCode} {
		if err := autonomy.RegisterDomainEntity(entity); err != nil {
			return err
		}
		if err := autonomy.BindEntityToContextContainer(entity.Entity(), project); err != nil {
			return err
		}
	}
	return nil
}

// contextContainer is the bridge between the two names for the same thing: what
// the world calls a project, the runtime names a project context container.
func contextContainer(entity ExternalEntity) (autonomy.ContextContainer, error) {
	domain, err := autonomy.ConvertToTaskDomain(entity.EntityDomain)
	if err != nil {
		return autonomy.ContextContainer{}, err
	}
	kind, err := autonomy.ConvertToContextContainerType(entity.EntityType)
	if err != nil {
		return autonomy.ContextContainer{}, err
	}
	return autonomy.ContextContainer{
		ID:                   entity.ID,
		Name:                 entity.Name,
		Description:          entity.Description,
		CreatedAt:            entity.CreatedAt,
		UpdatedAt:            entity.UpdatedAt,
		DomainType:           domain,
		ContextContainerType: kind,
	}, nil
}
