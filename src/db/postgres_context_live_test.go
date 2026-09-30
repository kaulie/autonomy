package db

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	ctxsvc "github.com/kaulie/autonomy/src/context"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// stubContextLoader serves canned content and revisions for the Context Service
// integration test.
type stubContextLoader struct {
	content  map[string]string
	revision map[string]string
}

func (s stubContextLoader) Load(_ context.Context, r ctxsvc.Resource) ([]byte, string, error) {
	body, ok := s.content[r.ID]
	if !ok {
		return nil, "", ctxsvc.Errorf(ctxsvc.ErrSourceUnavailable, "no stub content for %q", r.ID)
	}
	return []byte(body), s.revision[r.ID], nil
}

// TestPostgresContextRepositoryLive exercises the real PostgreSQL repository
// behind the Context Service: schema, registry, full-text index, search, get,
// and re-sync on change. It is skipped unless AUTONOMY_CONTEXT_TEST_DSN points
// at a PostgreSQL database the test may freely write to.
func TestPostgresContextRepositoryLive(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("AUTONOMY_CONTEXT_TEST_DSN"))
	if dsn == "" {
		t.Skip("set AUTONOMY_CONTEXT_TEST_DSN to run the PostgreSQL Context Service integration test")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	repo := &postgresContextRepository{db: db}
	if err := repo.ensureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	for _, table := range []string{"context_sections", "context_resources"} {
		if _, err := db.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clean %s: %v", table, err)
		}
	}

	loader := stubContextLoader{
		content:  map[string]string{"doc-pg": "# Runtime\n## Heartbeat Protocol\nRuntime periodically sends heartbeat to the control plane.\n"},
		revision: map[string]string{"doc-pg": "rev1"},
	}
	svc := ctxsvc.NewService(repo, loader)
	if err := svc.RegisterResource(ctx, ctxsvc.Resource{ID: "doc-pg", ProjectID: "autonomy", Type: ctxsvc.ResourceDocument, Name: "runtime.md"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := svc.SyncResource(ctx, "doc-pg"); err != nil {
		t.Fatalf("sync: %v", err)
	}

	results, err := svc.Search(ctx, ctxsvc.SearchRequest{ProjectID: "autonomy", Query: "heartbeat protocol"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("PostgreSQL search returned nothing")
	}
	if top := results[0]; top.ResourceID != "doc-pg" || top.Heading != "Heartbeat Protocol" {
		t.Fatalf("top result = %+v", top)
	} else if !strings.Contains(strings.ToLower(top.Snippet), "heartbeat") {
		t.Errorf("snippet = %q", top.Snippet)
	}

	section, err := svc.GetSection(ctx, results[0].SectionID)
	if err != nil {
		t.Fatalf("get section: %v", err)
	}
	if !strings.Contains(section.Content, "heartbeat to the control plane") || section.Revision != "rev1" {
		t.Fatalf("section = %+v", section)
	}

	loader.content["doc-pg"] = "# Runtime\n## Heartbeat Protocol\nHeartbeat interval changed to 30 seconds.\n"
	loader.revision["doc-pg"] = "rev2"
	if err := svc.SyncResource(ctx, "doc-pg"); err != nil {
		t.Fatalf("resync: %v", err)
	}
	changed, err := svc.Search(ctx, ctxsvc.SearchRequest{ProjectID: "autonomy", Query: "interval 30 seconds"})
	if err != nil {
		t.Fatalf("search after change: %v", err)
	}
	if len(changed) == 0 {
		t.Fatal("PostgreSQL index did not follow the change")
	}
	updated, _ := svc.GetSection(ctx, changed[0].SectionID)
	if !strings.Contains(updated.Content, "30 seconds") {
		t.Fatalf("index not rebuilt: %+v", updated)
	}
}
