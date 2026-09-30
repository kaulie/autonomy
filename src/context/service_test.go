package context

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubLoader serves canned content and revisions, standing in for a git source.
type stubLoader struct {
	content  map[string]string
	revision map[string]string
}

func (s *stubLoader) Load(_ context.Context, r Resource) ([]byte, string, error) {
	c, ok := s.content[r.ID]
	if !ok {
		return nil, "", Errorf(ErrSourceUnavailable, "no stub content for %q", r.ID)
	}
	return []byte(c), s.revision[r.ID], nil
}

func steppingClock() func() time.Time {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	n := 0
	return func() time.Time { n++; return base.Add(time.Duration(n) * time.Second) }
}

func newTestService(repo Repository, loader SourceLoader) Service {
	return NewService(repo, loader, WithClock(steppingClock()), WithIDGenerator(seqIDs()))
}

func seqIDs() func() string {
	n := 0
	return func() string { n++; return "ctx-" + string(rune('a'+n-1)) }
}

func TestRegisterAndSyncValidation(t *testing.T) {
	svc := newTestService(NewMemoryRepository(), &stubLoader{})
	ctx := context.Background()

	if err := svc.RegisterResource(ctx, Resource{Name: "no project"}); !IsCode(err, ErrProjectNotFound) {
		t.Errorf("missing project = %v, want PROJECT_NOT_FOUND", err)
	}
	if err := svc.RegisterResource(ctx, Resource{ProjectID: "p", Type: "widget"}); !IsCode(err, ErrInvalidQuery) {
		t.Errorf("bad type = %v, want INVALID_QUERY", err)
	}
	if err := svc.SyncResource(ctx, "ghost"); !IsCode(err, ErrResourceNotFound) {
		t.Errorf("sync unknown = %v, want RESOURCE_NOT_FOUND", err)
	}
}

func TestSyncMissingSourceIsSourceUnavailable(t *testing.T) {
	svc := newTestService(NewMemoryRepository(), &stubLoader{})
	ctx := context.Background()
	r := Resource{ID: "doc-1", ProjectID: "p", Name: "d"}
	if err := svc.RegisterResource(ctx, r); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := svc.SyncResource(ctx, "doc-1"); !IsCode(err, ErrSourceUnavailable) {
		t.Fatalf("sync unreadable source = %v, want SOURCE_UNAVAILABLE", err)
	}
}

func TestSearchAndGetValidation(t *testing.T) {
	svc := newTestService(NewMemoryRepository(), &stubLoader{})
	ctx := context.Background()

	if _, err := svc.Search(ctx, SearchRequest{Query: "x"}); !IsCode(err, ErrProjectNotFound) {
		t.Errorf("search without project = %v, want PROJECT_NOT_FOUND", err)
	}
	if _, err := svc.Search(ctx, SearchRequest{ProjectID: "p"}); !IsCode(err, ErrInvalidQuery) {
		t.Errorf("search without query = %v, want INVALID_QUERY", err)
	}
	if _, err := svc.ListResources(ctx, ""); !IsCode(err, ErrProjectNotFound) {
		t.Errorf("list without project = %v, want PROJECT_NOT_FOUND", err)
	}
	if _, err := svc.GetResource(ctx, "ghost"); !IsCode(err, ErrResourceNotFound) {
		t.Errorf("get unknown resource = %v, want RESOURCE_NOT_FOUND", err)
	}
	if _, err := svc.GetSection(ctx, "ghost"); !IsCode(err, ErrResourceNotFound) {
		t.Errorf("get unknown section = %v, want RESOURCE_NOT_FOUND", err)
	}
}

func TestSyncIndexesAndDetectsChange(t *testing.T) {
	loader := &stubLoader{
		content:  map[string]string{"doc-1": sampleDoc},
		revision: map[string]string{"doc-1": "rev-1"},
	}
	repo := NewMemoryRepository()
	svc := newTestService(repo, loader)
	ctx := context.Background()

	if err := svc.RegisterResource(ctx, Resource{ID: "doc-1", ProjectID: "p", Name: "Runtime", Type: ResourceDocument}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := svc.SyncResource(ctx, "doc-1"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	first, _ := svc.GetResource(ctx, "doc-1")
	if first.Checksum == "" || first.Revision != "rev-1" {
		t.Fatalf("after sync checksum=%q revision=%q", first.Checksum, first.Revision)
	}

	// Unchanged source: not re-indexed, provenance untouched.
	if err := svc.SyncResource(ctx, "doc-1"); err != nil {
		t.Fatalf("resync: %v", err)
	}
	again, _ := svc.GetResource(ctx, "doc-1")
	if !again.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("unchanged sync re-indexed: updatedAt %v -> %v", first.UpdatedAt, again.UpdatedAt)
	}

	// Changed source: checksum and revision move together, index is replaced.
	loader.content["doc-1"] = strings.Replace(sampleDoc, "periodically sends heartbeat to the control plane", "sends a heartbeat every 5 seconds", 1)
	loader.revision["doc-1"] = "rev-2"
	if err := svc.SyncResource(ctx, "doc-1"); err != nil {
		t.Fatalf("resync after change: %v", err)
	}
	changed, _ := svc.GetResource(ctx, "doc-1")
	if changed.Checksum == first.Checksum || changed.Revision != "rev-2" {
		t.Errorf("change not detected: checksum %q -> %q, revision %q", first.Checksum, changed.Checksum, changed.Revision)
	}
}

func TestFileSourceLoaderReadsLocalFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte("# Notes\nbody\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	svc := newTestService(NewMemoryRepository(), FileSourceLoader{})
	ctx := context.Background()
	r := Resource{ID: "notes", ProjectID: "p", Name: "notes.md", Type: ResourceDocument,
		Source: ResourceSource{Type: "local_file", Location: path}}
	if err := svc.RegisterResource(ctx, r); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := svc.SyncResource(ctx, "notes"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	res, _ := svc.Search(ctx, SearchRequest{ProjectID: "p", Query: "body"})
	if len(res) != 1 || res[0].Heading != "Notes" {
		t.Fatalf("search = %+v", res)
	}
}
