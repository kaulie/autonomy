package autonomy

type ContextContainerManager struct {
	// project-001: context container
	// team-001: context container
	ContextContainers map[string]ContextContainer
	// project: project-001: context container
	// team: team-001: context container
	ContextContainersByType map[ContextContainerType]map[string]ContextContainer
}

func NewContextContainerManager() *ContextContainerManager {
	return &ContextContainerManager{
		ContextContainers:       make(map[string]ContextContainer),
		ContextContainersByType: make(map[ContextContainerType]map[string]ContextContainer),
	}
}

// ContextEntityManager is a manager for the context entities
type ContextEntityManager struct {
	ContextEntities map[string]ContextEntity
}

func NewContextEntityManager() *ContextEntityManager {
	return &ContextEntityManager{
		ContextEntities: make(map[string]ContextEntity),
	}
}

type DomainEntityManager struct {
	DomainEntities map[string]DomainEntity
}

func NewDomainEntityManager() *DomainEntityManager {
	return &DomainEntityManager{
		DomainEntities: make(map[string]DomainEntity),
	}
}

func RegisterContextContainer(container ContextContainer) error {
	GetAutonomy().ContextContainerManager.ContextContainers[container.ID] = container
	typeMap, ok := GetAutonomy().ContextContainerManager.ContextContainersByType[container.ContextContainerType]
	if !ok {
		typeMap = make(map[string]ContextContainer)
	}
	typeMap[container.ID] = container
	GetAutonomy().ContextContainerManager.ContextContainersByType[container.ContextContainerType] = typeMap
	return nil
}

// acts as a bridge between the real world entity and the context entity
func RegisterContextEntity(entity ContextEntity) error {
	GetAutonomy().ContextEntityManager.ContextEntities[entity.ID] = entity
	return nil
}

func RegisterDomainEntity(entity DomainEntity) error {
	GetAutonomy().DomainEntityManager.DomainEntities[entity.Entity().ID] = entity
	return nil
}

func BindTaskCtxRef(taskID string, contextEntityID string) error {
	GetAutonomy().TaskCtxManager.AddTaskCtxRef(taskID, contextEntityID)
	return nil
}
