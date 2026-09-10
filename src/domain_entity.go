package autonomy

import (
	"encoding/json"
	"time"
)

type Entity struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Type        string    `json:"type"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
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
	Meta       Entity     `json:"-"`
	Repository Repository `json:"repository,omitempty"`
}

func (e SourceCodeEntity) Entity() Entity {
	return e.Meta
}

func (e SourceCodeEntity) Type() string {
	return "source_code"
}

func (e SourceCodeEntity) MarshalJSON() ([]byte, error) {
	type alias SourceCodeEntity
	base, err := json.Marshal(alias(e))
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(base, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	set := func(k string, v any) {
		b, _ := json.Marshal(v)
		m[k] = b
	}
	set("id", e.Meta.ID)
	set("name", e.Meta.Name)
	set("description", e.Meta.Description)
	set("type", e.Type())
	if !e.Meta.CreatedAt.IsZero() {
		set("created_at", e.Meta.CreatedAt)
	}
	if !e.Meta.UpdatedAt.IsZero() {
		set("updated_at", e.Meta.UpdatedAt)
	}
	return json.Marshal(m)
}

type Repository struct {
	URL        string `json:"url"`
	MainBranch string `json:"main_branch"`
}

func BindEntityToContextContainer(entity Entity, contextContainer ContextContainer) error {
	return updateContextContainer(contextContainer.ID, func(c *ContextContainer) {
		c.EntityReferences = append(c.EntityReferences, entity.ID)
	})
}

func BindAssetToContextContainer(asset Asset, contextContainer ContextContainer) error {
	return updateContextContainer(contextContainer.ID, func(c *ContextContainer) {
		c.AssetReferences = append(c.AssetReferences, asset.ID)
	})
}

func BindContextToContextContainer(contextEntity ContextEntity, contextContainer ContextContainer) error {
	return updateContextContainer(contextContainer.ID, func(c *ContextContainer) {
		c.ContextReferences = append(c.ContextReferences, contextEntity.ID)
	})
}
