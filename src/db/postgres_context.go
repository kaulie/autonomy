package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"

	ctxsvc "github.com/kaulie/autonomy/src/context"
)

// This file is the PostgreSQL engine's implementation of the Context Service's
// persistence port (github.com/kaulie/autonomy/src/context). It lives in the
// engine layer on purpose: only a storage engine may import database/sql
// (store_ports_test.go), and the Context Service opens no connection of its own
// — it is handed this store's writer.
//
// The schema (context_resources / context_sections + a GIN index on the search
// vector) is this repository's own and is created idempotently. The Markdown
// source stays the Source of Truth; these tables are the rebuildable index.

// contextTextSearchConfig is the PostgreSQL text-search configuration for the
// index and queries. "simple" tokenises every language without stemming, which
// suits a repo whose docs mix English and Chinese.
const contextTextSearchConfig = "simple"

type postgresContextRepository struct {
	db *sql.DB
}

// ContextRepository returns the Context Service repository backed by this
// store's writer, or nil when the schema could not be created. It is built once
// and reused.
func (s *PostgresStore) ContextRepository() ctxsvc.Repository {
	if s == nil || s.db == nil {
		return nil
	}
	s.ctxRepoOnce.Do(func() {
		repo := &postgresContextRepository{db: s.db}
		if err := repo.ensureSchema(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "[context] schema not created, Context Service disabled: %v\n", err)
			return
		}
		s.ctxRepo = repo
	})
	return s.ctxRepo
}

func (r *postgresContextRepository) ensureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS context_resources (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			type TEXT NOT NULL,
			name TEXT NOT NULL,
			uri TEXT,
			source_type TEXT,
			source_location TEXT,
			source_repository TEXT,
			source_path TEXT,
			revision TEXT,
			checksum TEXT,
			metadata JSONB,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_context_resources_project ON context_resources (project_id)`,
		`CREATE TABLE IF NOT EXISTS context_sections (
			id TEXT PRIMARY KEY,
			resource_id TEXT NOT NULL,
			heading TEXT,
			heading_path TEXT,
			content TEXT NOT NULL,
			section_order INTEGER,
			start_line INTEGER,
			end_line INTEGER,
			search_vector TSVECTOR
		)`,
		`CREATE INDEX IF NOT EXISTS idx_context_sections_resource ON context_sections (resource_id, section_order)`,
		`CREATE INDEX IF NOT EXISTS idx_context_sections_search ON context_sections USING GIN (search_vector)`,
	}
	for _, stmt := range stmts {
		if _, err := r.db.ExecContext(ctx, stmt); err != nil {
			return ctxsvc.Wrap(ctxsvc.ErrIndexFailed, err, "ensure context schema")
		}
	}
	return nil
}

func (r *postgresContextRepository) SaveResource(ctx context.Context, resource ctxsvc.Resource) error {
	metadata, err := json.Marshal(resource.Metadata)
	if err != nil {
		return ctxsvc.Wrap(ctxsvc.ErrIndexFailed, err, "encode metadata for %q", resource.ID)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO context_resources
			(id, project_id, type, name, uri, source_type, source_location, source_repository,
			 source_path, revision, checksum, metadata, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb,$13,$14)
		ON CONFLICT (id) DO UPDATE SET
			project_id=EXCLUDED.project_id, type=EXCLUDED.type, name=EXCLUDED.name, uri=EXCLUDED.uri,
			source_type=EXCLUDED.source_type, source_location=EXCLUDED.source_location,
			source_repository=EXCLUDED.source_repository, source_path=EXCLUDED.source_path,
			revision=EXCLUDED.revision, checksum=EXCLUDED.checksum, metadata=EXCLUDED.metadata,
			created_at=EXCLUDED.created_at, updated_at=EXCLUDED.updated_at`,
		resource.ID, resource.ProjectID, string(resource.Type), resource.Name, resource.URI,
		resource.Source.Type, resource.Source.Location,
		resource.Source.Repository, resource.Source.Path, resource.Revision, resource.Checksum,
		string(metadata), resource.CreatedAt, resource.UpdatedAt)
	if err != nil {
		return ctxsvc.Wrap(ctxsvc.ErrIndexFailed, err, "save resource %q", resource.ID)
	}
	return nil
}
