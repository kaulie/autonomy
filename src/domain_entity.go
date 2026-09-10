package autonomy

import "time"

type Entity struct {
	ID          string
	Name        string
	Description string
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
	Meta Entity
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
