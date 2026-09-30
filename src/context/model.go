// Package context implements the Context Service described in
// /Users/gaolei/agent-policies/context_service.md (V1 scope, sections 21-22).
//
// It gives a Project a discoverable, addressable, versioned Context Space and
// exposes discovery through a single ContextService interface, so Planner,
// Runtime and Agent callers never touch SQL. Persistence hides behind a
// Repository; the PostgreSQL implementation reuses the runtime's existing
// database connection (see postgres.go) rather than opening a second one.
//
// V1 deliberately stops at Markdown -> Register -> Index -> Search -> Get:
// no vector DB, embeddings, LLM summarization, knowledge graph or planner logic.
package context

import "time"

// ResourceType is what a ProjectResource is. V1 fully parses documents
// (Markdown); repository and service resources are only registered.
type ResourceType string

const (
	ResourceDocument   ResourceType = "document"
	ResourceRepository ResourceType = "repository"
	ResourceService    ResourceType = "service"
)

// Valid reports whether t is one of the V1 resource types.
func (t ResourceType) Valid() bool {
	switch t {
	case ResourceDocument, ResourceRepository, ResourceService:
		return true
	default:
		return false
	}
}

// ResourceSource is where a Resource actually lives — the fact the index must
// be able to be rebuilt from (Source of Truth != Search Index).
type ResourceSource struct {
	// Type is the source kind: local_file, git, external.
	Type string `json:"type,omitempty"`
	// Location is the concrete place: a filesystem path or a remote URL.
	Location string `json:"location,omitempty"`
	// Repository and Path name a resource inside a repository, when relevant.
	Repository string `json:"repository,omitempty"`
	Path       string `json:"path,omitempty"`
}

// Resource is the unified abstraction every Project Context entry is
// normalised into (spec 7.1).
type Resource struct {
	ID        string            `json:"id"`
	ProjectID string            `json:"project_id"`
	Type      ResourceType      `json:"type"`
	Name      string            `json:"name"`
	URI       string            `json:"uri,omitempty"`
	Source    ResourceSource    `json:"source"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	// Revision is a git commit SHA for repository-backed sources, else empty.
	Revision  string    `json:"revision,omitempty"`
	Checksum  string    `json:"checksum,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Section is one heading-delimited slice of a parsed document (spec 8).
type Section struct {
	ID         string `json:"id"`
	ResourceID string `json:"resource_id"`
	Heading    string `json:"heading"`
	// Path is the heading ancestry, e.g. "Runtime Architecture / Heartbeat Protocol".
	Path      string `json:"path,omitempty"`
	Content   string `json:"content"`
	Order     int    `json:"order"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`

	// Revision and Source are provenance, filled in on retrieval from the
	// owning Resource (they are not columns of context_sections).
	Revision string         `json:"revision,omitempty"`
	Source   ResourceSource `json:"source,omitempty"`
}

// SearchRequest asks for candidate resources inside one project (spec 12).
type SearchRequest struct {
	ProjectID     string
	Query         string
	ResourceTypes []ResourceType
	Limit         int
	// Metadata filters candidates to resources whose metadata contains every
	// listed key=value pair. Optional.
	Metadata map[string]string
}

// SearchResult is a candidate section: enough to decide whether to fetch it,
// not the whole document (spec 12).
type SearchResult struct {
	ResourceID   string  `json:"resource_id"`
	SectionID    string  `json:"section_id"`
	ResourceName string  `json:"resource_name"`
	Heading      string  `json:"heading"`
	Snippet      string  `json:"snippet"`
	Score        float64 `json:"score"`
	Revision     string  `json:"revision,omitempty"`
}
