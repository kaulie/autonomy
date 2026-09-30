package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	ctxsvc "github.com/kaulie/autonomy/src/context"
)

const selectContextResource = `SELECT id, project_id, type, name, uri, source_type, source_location,
	source_repository, source_path, revision, checksum, metadata, created_at, updated_at
	FROM context_resources`

// rowScanner is the one method *sql.Row and *sql.Rows share that scanning needs.
type rowScanner interface {
	Scan(dest ...any) error
}

func (r *postgresContextRepository) GetResource(ctx context.Context, id string) (*ctxsvc.Resource, error) {
	row := r.db.QueryRowContext(ctx, selectContextResource+` WHERE id = $1`, id)
	resource, err := scanContextResource(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, ctxsvc.Wrap(ctxsvc.ErrIndexFailed, err, "read resource %q", id)
	}
	return &resource, nil
}

func (r *postgresContextRepository) ListResources(ctx context.Context, projectID string) ([]ctxsvc.Resource, error) {
	rows, err := r.db.QueryContext(ctx,
		selectContextResource+` WHERE project_id = $1 ORDER BY name ASC, id ASC`, projectID)
	if err != nil {
		return nil, ctxsvc.Wrap(ctxsvc.ErrIndexFailed, err, "list project %q", projectID)
	}
	defer rows.Close()
	out := []ctxsvc.Resource{}
	for rows.Next() {
		resource, err := scanContextResource(rows)
		if err != nil {
			return nil, ctxsvc.Wrap(ctxsvc.ErrIndexFailed, err, "scan resource row")
		}
		out = append(out, resource)
	}
	return out, rows.Err()
}

func scanContextResource(row rowScanner) (ctxsvc.Resource, error) {
	var (
		resource   ctxsvc.Resource
		typ        string
		metadata   []byte
		createdAt  time.Time
		updatedAt  time.Time
		uri        sql.NullString
		sourceType sql.NullString
		location   sql.NullString
		repository sql.NullString
		path       sql.NullString
		revision   sql.NullString
		checksum   sql.NullString
	)
	if err := row.Scan(&resource.ID, &resource.ProjectID, &typ, &resource.Name, &uri, &sourceType,
		&location, &repository, &path, &revision, &checksum, &metadata, &createdAt, &updatedAt); err != nil {
		return ctxsvc.Resource{}, err
	}
	resource.Type = ctxsvc.ResourceType(typ)
	resource.URI = uri.String
	resource.Source = ctxsvc.ResourceSource{
		Type: sourceType.String, Location: location.String, Repository: repository.String, Path: path.String,
	}
	resource.Revision = revision.String
	resource.Checksum = checksum.String
	resource.CreatedAt = createdAt
	resource.UpdatedAt = updatedAt
	if len(metadata) > 0 && string(metadata) != "null" {
		_ = json.Unmarshal(metadata, &resource.Metadata)
	}
	return resource, nil
}
