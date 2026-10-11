package context

import (
	"context"
	"testing"
)

func TestMemoryRepositoryDeleteResource(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	_ = repo.SaveResource(ctx, Resource{ID: "doc-1", ProjectID: "p", Name: "Runtime"})
	_ = repo.ReplaceSections(ctx, "doc-1", []Section{{ID: "sec-1", ResourceID: "doc-1", Heading: "Heartbeat", Content: "heartbeat"}})

	if err := repo.DeleteResource(ctx, "doc-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if r, _ := repo.GetResource(ctx, "doc-1"); r != nil {
		t.Errorf("resource still there: %+v", r)
	}
	if s, _ := repo.GetSection(ctx, "sec-1"); s != nil {
		t.Errorf("section still there: %+v", s)
	}
	if err := repo.DeleteResource(ctx, "doc-1"); err != nil {
		t.Errorf("deleting an unknown id: %v", err)
	}
}

func TestServiceDeleteResource(t *testing.T) {
	loader := &stubLoader{
		content:  map[string]string{"doc-1": sampleDoc, "doc-2": sampleDoc},
		revision: map[string]string{"doc-1": "rev-1", "doc-2": "rev-1"},
	}
	svc := newTestService(NewMemoryRepository(), loader)
	ctx := context.Background()
	for _, id := range []string{"doc-1", "doc-2"} {
		if err := svc.RegisterResource(ctx, Resource{ID: id, ProjectID: "p", Name: id, Type: ResourceDocument}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
		if err := svc.SyncResource(ctx, id); err != nil {
			t.Fatalf("sync %s: %v", id, err)
		}
	}
	before, err := svc.Search(ctx, SearchRequest{ProjectID: "p", Query: "heartbeat"})
	if err != nil || len(before) == 0 {
		t.Fatalf("search before delete: %v %+v", err, before)
	}
	var deletedSection string
	for _, r := range before {
		if r.ResourceID == "doc-1" {
			deletedSection = r.SectionID
		}
	}

	if err := svc.DeleteResource(ctx, "doc-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.GetResource(ctx, "doc-1"); CodeOf(err) != ErrResourceNotFound {
		t.Errorf("get after delete: %v", err)
	}
	if _, err := svc.GetSection(ctx, deletedSection); CodeOf(err) != ErrResourceNotFound {
		t.Errorf("section after delete: %v", err)
	}
	after, _ := svc.Search(ctx, SearchRequest{ProjectID: "p", Query: "heartbeat"})
	for _, r := range after {
		if r.ResourceID == "doc-1" {
			t.Errorf("search still returns the deleted resource: %+v", r)
		}
	}
	if len(after) == 0 {
		t.Error("deleting doc-1 also removed doc-2's sections")
	}
	list, _ := svc.ListResources(ctx, "p")
	if len(list) != 1 || list[0].ID != "doc-2" {
		t.Errorf("list after delete = %+v", list)
	}

	if err := svc.DeleteResource(ctx, "doc-1"); CodeOf(err) != ErrResourceNotFound {
		t.Errorf("deleting an unknown id = %v, want RESOURCE_NOT_FOUND", err)
	}
}
