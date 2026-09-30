package context

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

const (
	// DefaultSearchLimit is used when a request sets no limit.
	DefaultSearchLimit = 10
	// MaxSearchLimit caps a request's limit.
	MaxSearchLimit = 50
)

// Service is the Context Service's public interface (spec 11). Planner, Runtime
// and Agent callers depend on this and never on the database: a future split
// into an out-of-process HTTP/gRPC service replaces the implementation, not the
// callers.
type Service interface {
	RegisterResource(ctx context.Context, resource Resource) error
	SyncResource(ctx context.Context, resourceID string) error
	Search(ctx context.Context, req SearchRequest) ([]SearchResult, error)
	GetResource(ctx context.Context, resourceID string) (*Resource, error)
	GetSection(ctx context.Context, sectionID string) (*Section, error)
	ListResources(ctx context.Context, projectID string) ([]Resource, error)
}

// ServiceOption customises a Service (clock and id generation, for tests).
type ServiceOption func(*service)

// WithClock overrides the service's clock.
func WithClock(now func() time.Time) ServiceOption {
	return func(s *service) { s.now = now }
}

// WithIDGenerator overrides how a missing resource id is minted.
func WithIDGenerator(gen func() string) ServiceOption {
	return func(s *service) { s.newID = gen }
}

type service struct {
	repo   Repository
	loader SourceLoader
	now    func() time.Time
	newID  func() string
}

// NewService builds the Context Service over a repository. A nil loader means
// local-file sources (FileSourceLoader).
func NewService(repo Repository, loader SourceLoader, opts ...ServiceOption) Service {
	if loader == nil {
		loader = FileSourceLoader{}
	}
	s := &service{repo: repo, loader: loader, now: time.Now, newID: mintID}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func mintID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "ctx-" + time.Now().UTC().Format("20060102150405.000000000")
	}
	return "ctx-" + hex.EncodeToString(b[:])
}

func (s *service) RegisterResource(ctx context.Context, r Resource) error {
	if strings.TrimSpace(r.ProjectID) == "" {
		return Errorf(ErrProjectNotFound, "resource %q has no project id", r.Name)
	}
	if r.Type == "" {
		r.Type = ResourceDocument
	}
	if !r.Type.Valid() {
		return Errorf(ErrInvalidQuery, "unknown resource type %q", r.Type)
	}
	now := s.now()
	if r.ID == "" {
		r.ID = s.newID()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	r.UpdatedAt = now
	if err := s.repo.SaveResource(ctx, r); err != nil {
		return Wrap(ErrIndexFailed, err, "register resource %q", r.ID)
	}
	return nil
}

func (s *service) SyncResource(ctx context.Context, resourceID string) error {
	r, err := s.repo.GetResource(ctx, strings.TrimSpace(resourceID))
	if err != nil {
		return Wrap(ErrIndexFailed, err, "read resource %q", resourceID)
	}
	if r == nil {
		return Errorf(ErrResourceNotFound, "resource %q is not registered", resourceID)
	}
	if r.Type != ResourceDocument {
		// Repository and service resources are registered only in V1.
		return nil
	}
	content, revision, err := s.loader.Load(ctx, *r)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(content)
	checksum := hex.EncodeToString(sum[:])
	if r.Checksum == checksum && r.Revision == revision {
		// Source unchanged: do not re-index (spec 9 / 17).
		return nil
	}
	sections, err := ParseMarkdown(r.ID, content)
	if err != nil {
		return err
	}
	if err := s.repo.ReplaceSections(ctx, r.ID, sections); err != nil {
		return Wrap(ErrIndexFailed, err, "index %d section(s) for %q", len(sections), r.ID)
	}
	r.Checksum = checksum
	r.Revision = revision
	r.UpdatedAt = s.now()
	if err := s.repo.SaveResource(ctx, *r); err != nil {
		return Wrap(ErrIndexFailed, err, "record revision for %q", r.ID)
	}
	return nil
}

func (s *service) Search(ctx context.Context, req SearchRequest) ([]SearchResult, error) {
	if strings.TrimSpace(req.ProjectID) == "" {
		return nil, Errorf(ErrProjectNotFound, "search requires a project id")
	}
	if len(queryTerms(req.Query)) == 0 {
		return nil, Errorf(ErrInvalidQuery, "search query %q has no searchable terms", req.Query)
	}
	if req.Limit <= 0 {
		req.Limit = DefaultSearchLimit
	}
	if req.Limit > MaxSearchLimit {
		req.Limit = MaxSearchLimit
	}
	results, err := s.repo.Search(ctx, req)
	if err != nil {
		return nil, Wrap(ErrIndexFailed, err, "search project %q", req.ProjectID)
	}
	return results, nil
}

func (s *service) GetResource(ctx context.Context, resourceID string) (*Resource, error) {
	r, err := s.repo.GetResource(ctx, strings.TrimSpace(resourceID))
	if err != nil {
		return nil, Wrap(ErrIndexFailed, err, "read resource %q", resourceID)
	}
	if r == nil {
		return nil, Errorf(ErrResourceNotFound, "resource %q is not registered", resourceID)
	}
	return r, nil
}

func (s *service) GetSection(ctx context.Context, sectionID string) (*Section, error) {
	sec, err := s.repo.GetSection(ctx, strings.TrimSpace(sectionID))
	if err != nil {
		return nil, Wrap(ErrIndexFailed, err, "read section %q", sectionID)
	}
	if sec == nil {
		return nil, Errorf(ErrResourceNotFound, "section %q is not indexed", sectionID)
	}
	if r, err := s.repo.GetResource(ctx, sec.ResourceID); err == nil && r != nil {
		sec.Revision = r.Revision
		sec.Source = r.Source
	}
	return sec, nil
}

func (s *service) ListResources(ctx context.Context, projectID string) ([]Resource, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, Errorf(ErrProjectNotFound, "list requires a project id")
	}
	res, err := s.repo.ListResources(ctx, projectID)
	if err != nil {
		return nil, Wrap(ErrIndexFailed, err, "list project %q", projectID)
	}
	if res == nil {
		res = []Resource{}
	}
	return res, nil
}

var _ Service = (*service)(nil)
