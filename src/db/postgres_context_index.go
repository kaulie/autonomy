package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	ctxsvc "github.com/kaulie/autonomy/src/context"
)

func (r *postgresContextRepository) ReplaceSections(ctx context.Context, resourceID string, sections []ctxsvc.Section) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ctxsvc.Wrap(ctxsvc.ErrIndexFailed, err, "begin index transaction for %q", resourceID)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM context_sections WHERE resource_id = $1`, resourceID); err != nil {
		return ctxsvc.Wrap(ctxsvc.ErrIndexFailed, err, "clear index for %q", resourceID)
	}
	insert := `INSERT INTO context_sections
		(id, resource_id, heading, heading_path, content, section_order, start_line, end_line, search_vector)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8, to_tsvector('` + contextTextSearchConfig + `', $9))`
	for _, s := range sections {
		document := strings.Join([]string{s.Heading, s.Path, s.Content}, " ")
		if _, err := tx.ExecContext(ctx, insert, s.ID, resourceID, s.Heading, s.Path,
			s.Content, s.Order, s.StartLine, s.EndLine, document); err != nil {
			return ctxsvc.Wrap(ctxsvc.ErrIndexFailed, err, "insert section %q", s.ID)
		}
	}
	if err := tx.Commit(); err != nil {
		return ctxsvc.Wrap(ctxsvc.ErrIndexFailed, err, "commit index for %q", resourceID)
	}
	return nil
}

func (r *postgresContextRepository) GetSection(ctx context.Context, id string) (*ctxsvc.Section, error) {
	row := r.db.QueryRowContext(ctx, `SELECT id, resource_id, coalesce(heading,''), coalesce(heading_path,''),
		content, coalesce(section_order,0), coalesce(start_line,0), coalesce(end_line,0)
		FROM context_sections WHERE id = $1`, id)
	var s ctxsvc.Section
	if err := row.Scan(&s.ID, &s.ResourceID, &s.Heading, &s.Path, &s.Content,
		&s.Order, &s.StartLine, &s.EndLine); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, ctxsvc.Wrap(ctxsvc.ErrIndexFailed, err, "read section %q", id)
	}
	return &s, nil
}

// Search runs full-text search with optional metadata filters, ranked by
// PostgreSQL's ts_rank and answered with a ts_headline snippet — candidates,
// never whole documents (spec 10 / 12).
func (r *postgresContextRepository) Search(ctx context.Context, req ctxsvc.SearchRequest) ([]ctxsvc.SearchResult, error) {
	config := contextTextSearchConfig
	args := []any{req.Query, req.ProjectID}
	where := []string{"r.project_id = $2", "s.search_vector @@ q.q"}
	if len(req.ResourceTypes) > 0 {
		placeholders := make([]string, 0, len(req.ResourceTypes))
		for _, t := range req.ResourceTypes {
			args = append(args, string(t))
			placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
		}
		where = append(where, "r.type IN ("+strings.Join(placeholders, ", ")+")")
	}
	for k, v := range req.Metadata {
		args = append(args, k, v)
		where = append(where, fmt.Sprintf("r.metadata ->> $%d = $%d", len(args)-1, len(args)))
	}
	limit := req.Limit
	if limit <= 0 {
		limit = ctxsvc.DefaultSearchLimit
	}
	args = append(args, limit)
	limitPlaceholder := fmt.Sprintf("$%d", len(args))

	query := "SELECT s.id, s.resource_id, r.name, coalesce(s.heading,''), coalesce(r.revision,''), " +
		"ts_rank(s.search_vector, q.q) AS rank, " +
		"ts_headline('" + config + "', s.content, q.q, 'MaxFragments=1, MinWords=5, MaxWords=20') AS snippet " +
		"FROM context_sections s JOIN context_resources r ON r.id = s.resource_id " +
		"CROSS JOIN plainto_tsquery('" + config + "', $1) q " +
		"WHERE " + strings.Join(where, " AND ") +
		" ORDER BY rank DESC, r.name ASC, s.id ASC LIMIT " + limitPlaceholder

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, ctxsvc.Wrap(ctxsvc.ErrIndexFailed, err, "search project %q", req.ProjectID)
	}
	defer rows.Close()
	out := []ctxsvc.SearchResult{}
	for rows.Next() {
		var (
			result ctxsvc.SearchResult
			rank   float64
		)
		if err := rows.Scan(&result.SectionID, &result.ResourceID, &result.ResourceName,
			&result.Heading, &result.Revision, &rank, &result.Snippet); err != nil {
			return nil, ctxsvc.Wrap(ctxsvc.ErrIndexFailed, err, "scan search row")
		}
		result.Score = rank
		out = append(out, result)
	}
	return out, rows.Err()
}

var _ ctxsvc.Repository = (*postgresContextRepository)(nil)
