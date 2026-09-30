package contextcap_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability/contextcap"
	ctxsvc "github.com/kaulie/autonomy/src/context"
)

func newService(t *testing.T) ctxsvc.Service {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.md")
	body := "# Runtime\n## Heartbeat Protocol\nRuntime sends heartbeat to the control plane.\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	svc := ctxsvc.NewService(ctxsvc.NewMemoryRepository(), ctxsvc.FileSourceLoader{})
	r := ctxsvc.Resource{
		ID: "doc-1", ProjectID: "p1", Type: ctxsvc.ResourceDocument, Name: "runtime.md",
		Revision: "abc123",
		Source:   ctxsvc.ResourceSource{Type: "local_file", Location: path},
	}
	if err := svc.RegisterResource(context.Background(), r); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := svc.SyncResource(context.Background(), "doc-1"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	return svc
}

func TestSearchGetList(t *testing.T) {
	svc := newService(t)

	list, err := contextcap.List{Svc: svc}.Run(map[string]string{"project_id": "p1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listed struct {
		Resources []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"resources"`
	}
	if err := json.Unmarshal([]byte(list["resources"]), &listed); err != nil {
		t.Fatalf("list json: %v", err)
	}
	if len(listed.Resources) != 1 || listed.Resources[0].ID != "doc-1" {
		t.Fatalf("listed = %+v", listed.Resources)
	}

	search, err := contextcap.Search{Svc: svc}.Run(map[string]string{"project_id": "p1", "query": "heartbeat protocol"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var results struct {
		Results []struct {
			ResourceID string `json:"resource_id"`
			SectionID  string `json:"section_id"`
			Title      string `json:"title"`
			Heading    string `json:"heading"`
			Snippet    string `json:"snippet"`
			Revision   string `json:"revision"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(search["results"]), &results); err != nil {
		t.Fatalf("search json: %v", err)
	}
	if len(results.Results) == 0 {
		t.Fatal("search returned nothing")
	}
	top := results.Results[0]
	if top.ResourceID != "doc-1" || top.Heading != "Heartbeat Protocol" || top.Revision != "abc123" {
		t.Fatalf("top = %+v", top)
	}

	got, err := contextcap.Get{Svc: svc}.Run(map[string]string{"section_id": top.SectionID, "resource_id": "doc-1"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.Contains(got["content"], "heartbeat to the control plane") || got["revision"] != "abc123" {
		t.Fatalf("get = %+v", got)
	}
}

func TestTypedErrorsSurfaceThroughCapabilities(t *testing.T) {
	svc := newService(t)

	if _, err := (contextcap.Get{Svc: svc}).Run(map[string]string{"section_id": "ghost"}); !ctxsvc.IsCode(err, ctxsvc.ErrResourceNotFound) {
		t.Errorf("get ghost = %v, want RESOURCE_NOT_FOUND", err)
	}
	if _, err := (contextcap.Search{Svc: svc}).Run(map[string]string{"project_id": "p1"}); !ctxsvc.IsCode(err, ctxsvc.ErrInvalidQuery) {
		t.Errorf("search no query = %v, want INVALID_QUERY", err)
	}
	if _, err := (contextcap.List{Svc: svc}).Run(map[string]string{}); !ctxsvc.IsCode(err, ctxsvc.ErrProjectNotFound) {
		t.Errorf("list no project = %v, want PROJECT_NOT_FOUND", err)
	}
	if _, err := (contextcap.Search{}).Run(map[string]string{"project_id": "p1", "query": "x"}); err == nil {
		t.Error("search without a service should error")
	}
}
