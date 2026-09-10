package autonomy

import "time"

type Entity struct {
	ID          string
	Name        string
	Description string
	Type        string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type DomainEntity interface {
	Entity() Entity
	Type() string
}

type DomainEntityType string

const (
	DomainEntityTypeSourceCode  DomainEntityType = "source_code"
	DomainEntityTypeIntegration DomainEntityType = "integration"
	DomainEntityTypeArtifact    DomainEntityType = "artifact"
	DomainEntityTypeDeployment  DomainEntityType = "deployment"
	DomainEntityTypeService     DomainEntityType = "service"
)

type SourceCodeEntity struct {
	Meta       Entity
	Repository Repository
}

func (e SourceCodeEntity) Entity() Entity {
	return e.Meta
}

func (e SourceCodeEntity) Type() string {
	return "source_code"
}

type Repository struct {
	URL        string `json:"url"`
	MainBranch string `json:"main_branch"`
}

func BindEntityToContextContainer(entity Entity, contextEntity ContextContainer) error {
	contextEntity.EntityReferences = append(contextEntity.EntityReferences, entity.ID)
	return nil
}

func BindAssetToContextContainer(asset Asset, contextEntity ContextContainer) error {
	contextEntity.AssetReferences = append(contextEntity.AssetReferences, asset.ID)
	return nil
}

func BindContextToContextContainer(contextEntity ContextEntity, contextContainer ContextContainer) error {
	contextContainer.ContextReferences = append(contextContainer.ContextReferences, contextEntity.ID)
	return nil
}
