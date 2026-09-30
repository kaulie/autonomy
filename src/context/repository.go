package context

import "context"

// Repository is the persistence port behind the Context Service. Keeping it an
// interface is what lets a caller depend on ContextService without ever
// touching SQL, and lets the store be rebuilt (the Markdown source stays the
// Source of Truth, the index does not).
//
// Implementations: MemoryRepository (tests, embedding) and PostgresRepository
// (production, reusing the runtime's existing database connection).
type Repository interface {
	// SaveResource inserts or replaces one resource row.
	SaveResource(ctx context.Context, r Resource) error
	// GetResource returns nil, nil when the id is unknown.
	GetResource(ctx context.Context, id string) (*Resource, error)
	// ListResources returns a project's resources, ordered by name then id.
	ListResources(ctx context.Context, projectID string) ([]Resource, error)
	// ReplaceSections atomically replaces every section of one resource.
	ReplaceSections(ctx context.Context, resourceID string, sections []Section) error
	// GetSection returns nil, nil when the section id is unknown.
	GetSection(ctx context.Context, id string) (*Section, error)
	// Search returns ranked candidate sections (spec 12), never whole documents.
	Search(ctx context.Context, req SearchRequest) ([]SearchResult, error)
}
