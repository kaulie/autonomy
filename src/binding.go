package autonomy

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
