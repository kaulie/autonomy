package main

import (
	"time"
)

// ExternalEntity is a entity that exists in real world
type ExternalEntity struct {
	ID           string
	Name         string
	Description  string
	EntityType   string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	EntityDomain string
}

type Project struct {
	ExternalEntity
}

type Team struct {
	ExternalEntity
}
