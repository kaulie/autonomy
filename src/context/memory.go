package context

import (
	"context"
	"sort"
	"sync"
)

// MemoryRepository is an in-process Repository. It is the reference
// implementation used by unit and integration tests (and by embedders that do
// not want SQLite/PostgreSQL); the PostgreSQL repository is the production one.
type MemoryRepository struct {
	mu        sync.RWMutex
	resources map[string]Resource
	sections  map[string][]Section
}

// NewMemoryRepository returns an empty in-memory repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		resources: make(map[string]Resource),
		sections:  make(map[string][]Section),
	}
}

func (m *MemoryRepository) SaveResource(_ context.Context, r Resource) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resources[r.ID] = cloneResource(r)
	return nil
}

func (m *MemoryRepository) GetResource(_ context.Context, id string) (*Resource, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.resources[id]
	if !ok {
		return nil, nil
	}
	c := cloneResource(r)
	return &c, nil
}

func (m *MemoryRepository) ListResources(_ context.Context, projectID string) ([]Resource, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Resource, 0, len(m.resources))
	for _, r := range m.resources {
		if r.ProjectID == projectID {
			out = append(out, cloneResource(r))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (m *MemoryRepository) ReplaceSections(_ context.Context, resourceID string, sections []Section) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(sections) == 0 {
		delete(m.sections, resourceID)
		return nil
	}
	cp := make([]Section, len(sections))
	copy(cp, sections)
	m.sections[resourceID] = cp
	return nil
}

func (m *MemoryRepository) GetSection(_ context.Context, id string) (*Section, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, secs := range m.sections {
		for _, s := range secs {
			if s.ID == id {
				c := s
				return &c, nil
			}
		}
	}
	return nil, nil
}

func (m *MemoryRepository) Search(_ context.Context, req SearchRequest) ([]SearchResult, error) {
	terms := queryTerms(req.Query)
	m.mu.RLock()
	defer m.mu.RUnlock()
	var results []SearchResult
	for id, r := range m.resources {
		if r.ProjectID != req.ProjectID || !matchesFilters(r, req) {
			continue
		}
		for _, s := range m.sections[id] {
			score := sectionScore(s, terms)
			if score <= 0 {
				continue
			}
			results = append(results, SearchResult{
				ResourceID:   r.ID,
				SectionID:    s.ID,
				ResourceName: r.Name,
				Heading:      s.Heading,
				Snippet:      snippet(s.Content, s.Heading, terms, 160),
				Score:        score,
				Revision:     r.Revision,
			})
		}
	}
	return rankResults(results, req.Limit), nil
}

func cloneResource(r Resource) Resource {
	c := r
	if r.Metadata != nil {
		c.Metadata = make(map[string]string, len(r.Metadata))
		for k, v := range r.Metadata {
			c.Metadata[k] = v
		}
	}
	return c
}
