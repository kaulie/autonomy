package context

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIntegrationMarkdownRegisterIndexSearchGet is the spec section 25
// acceptance flow: Markdown -> Register -> Index -> Search -> Get, then edit the
// source and re-sync, and see the index reflect the new content.
func TestIntegrationMarkdownRegisterIndexSearchGet(t *testing.T) {
	ctx := context.Background()
	docsDir := filepath.Join(t.TempDir(), "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	writeDoc := func(name, body string) string {
		path := filepath.Join(docsDir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	architecture := writeDoc("architecture.md", "# Architecture\n## Planner\nThe planner decides what to do next.\n")
	runtimePath := writeDoc("runtime.md", "# Runtime\n## Heartbeat Protocol\nRuntime periodically sends heartbeat to the control plane.\n")
	writeDoc("completion-contract.md", "# Completion Contract\n## Done\nA task is done when its contract is verified.\n")

	repo := NewMemoryRepository()
	svc := NewService(repo, FileSourceLoader{}, WithIDGenerator(seqIDs()))
	docs := []struct{ id, name, path string }{
		{"doc-arch", "architecture.md", architecture},
		{"doc-runtime", "runtime.md", runtimePath},
		{"doc-contract", "completion-contract.md", ""},
	}
	for _, d := range docs {
		path := d.path
		if path == "" {
			path = filepath.Join(docsDir, d.name)
		}
		r := Resource{
			ID: d.id, ProjectID: "autonomy", Type: ResourceDocument, Name: d.name,
			// A git-backed revision is carried through as provenance.
			Revision: "abc123",
			Source:   ResourceSource{Type: "local_file", Location: path, Path: d.name},
		}
		if err := svc.RegisterResource(ctx, r); err != nil {
			t.Fatalf("register %s: %v", d.name, err)
		}
		if err := svc.SyncResource(ctx, d.id); err != nil {
			t.Fatalf("sync %s: %v", d.name, err)
		}
	}

	// 1. Search finds the runtime document's Heartbeat Protocol section first.
	results, err := svc.Search(ctx, SearchRequest{ProjectID: "autonomy", Query: "Runtime 和 Planner 如何维持 heartbeat"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("search returned no results")
	}
	top := results[0]
	if top.ResourceID != "doc-runtime" || top.Heading != "Heartbeat Protocol" {
		t.Fatalf("top result = %s / %q (want doc-runtime / Heartbeat Protocol)", top.ResourceID, top.Heading)
	}
	if !strings.Contains(strings.ToLower(top.Snippet), "heartbeat") {
		t.Errorf("snippet does not show the match: %q", top.Snippet)
	}

	// 2. Get retrieves the full section with provenance.
	section, err := svc.GetSection(ctx, top.SectionID)
	if err != nil {
		t.Fatalf("get section: %v", err)
	}
	if section.ResourceID != "doc-runtime" || section.Revision != "abc123" {
		t.Errorf("section provenance = resource %q revision %q", section.ResourceID, section.Revision)
	}
	if section.Source.Path != "runtime.md" {
		t.Errorf("section source path = %q", section.Source.Path)
	}
	if !strings.Contains(section.Content, "sends heartbeat to the control plane") {
		t.Errorf("section content = %q", section.Content)
	}

	// 3. Change the source, re-sync: checksum changes and the index follows.
	before, _ := svc.GetResource(ctx, "doc-runtime")
	writeDoc("runtime.md", "# Runtime\n## Heartbeat Protocol\nHeartbeat interval changed to 30 seconds.\n")
	if err := svc.SyncResource(ctx, "doc-runtime"); err != nil {
		t.Fatalf("resync: %v", err)
	}
	after, _ := svc.GetResource(ctx, "doc-runtime")
	if after.Checksum == before.Checksum {
		t.Errorf("checksum did not change after edit (%q)", after.Checksum)
	}

	// 4. The new content is searchable, the old text is gone from the index.
	changed, err := svc.Search(ctx, SearchRequest{ProjectID: "autonomy", Query: "heartbeat interval 30 seconds"})
	if err != nil {
		t.Fatalf("search after change: %v", err)
	}
	if len(changed) == 0 {
		t.Fatal("no results for the changed content")
	}
	updated, err := svc.GetSection(ctx, changed[0].SectionID)
	if err != nil {
		t.Fatalf("get changed section: %v", err)
	}
	if !strings.Contains(updated.Content, "30 seconds") {
		t.Errorf("index not rebuilt: %q", updated.Content)
	}
}
