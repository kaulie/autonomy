package main

import (
	"time"

	autonomy "github.com/kaulie/autonomy/src"
)

// ExternalEntity is a entity that exists in real world
type ExternalEntity struct {
	ID          string
	Name        string
	Description string
	EntityType  autonomy.ContextEntityType
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Project struct {
	ExternalEntity
}

type Team struct {
	ExternalEntity
}
