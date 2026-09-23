package db

import . "github.com/kaulie/autonomy/src"

import (
	"database/sql"
	"fmt"
	"github.com/kaulie/autonomy/src/llmbackend"
	"sort"
	"strings"
	"time"
)

// The data API's read half, as the postgres engine serves it (TurnQueryStore in
// src/store.go, the endpoints in src/turn_api.go and docs/http-api.md「数据 API」).
//
// Every query here is a plain SELECT: filtering, ordering, paging, faceting and
// counting. Nothing writes, and nothing migrates. The statements are the postgres
// engine's own (numbered parameters, timestamptz columns), and they select the same
// logical columns as the sqlite engine's, so a TurnRecord — and therefore the JSON the
// data API answers with — is the same value whichever engine holds the log.

// pgTurnColumns is every column a TurnRecord read selects, in the order
// pgScanTurnRecord expects them: t is reason_turns, a is agents.
const pgTurnColumns = `t.id, t.task_id, t.cycle, t.mode,
       t.agent_id, COALESCE(a.name, ''),
       t.llm_provider, t.model, t.llm_agent_id,
       t.input, t.raw_output, t.normalized_output, t.run_id,
       t.status, t.error_code, t.error_message,
       t.duration_ms, t.event_count,
       t.input_tokens, t.output_tokens, t.cache_read_tokens, t.cache_write_tokens,
       t.reasoning_tokens, t.total_tokens, t.cost_cents,
       t.started_at, t.ended_at, t.created_at`

// pgTurnFrom is what a turn read is FROM: the log, joined to the agents it names. It is
// a LEFT JOIN because the agent of a turn is a name to show, not a precondition to
// read: a turn whose agent row is gone still reads, with an empty name.
const pgTurnFrom = `FROM reason_turns t LEFT JOIN agents a ON a.id = t.agent_id`

// QueryTurns reads one page of reason turns, and the number of rows the same filter
// matches: the pager counts what it pages, not the table it pages over.
func (s *PostgresStore) QueryTurns(q TurnQuery) ([]TurnRecord, int, error) {
	where, args := pgTurnWhere(q)
	var total int
	if err := s.readPool().QueryRow(`SELECT COUNT(*) `+pgTurnFrom+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count reason turns: %w", err)
	}
	// ORDER BY <order> <dir>, id DESC: equal sort keys keep a stable order, so a page
	// boundary that lands inside them neither skips a row nor shows it twice.
	page := append(append([]any{}, args...), turnPageLimit(q.Limit), turnPageOffset(q.Offset))
	query := `SELECT ` + pgTurnColumns + ` ` + pgTurnFrom + where +
		` ORDER BY ` + turnOrderColumn(q.Order) + ` ` + turnSortDirection(q.Dir) +
		`, t.id DESC LIMIT ` + pgParam(len(page)-1) + ` OFFSET ` + pgParam(len(page))
	rows, err := s.readPool().Query(query, page...)
	if err != nil {
		return nil, 0, fmt.Errorf("query reason turns: %w", err)
	}
	defer rows.Close()
	turns, err := pgScanTurnRecords(rows)
	if err != nil {
		return nil, 0, err
	}
	return turns, total, nil
}

// pgParam renders the $n placeholder of the nth (1-based) parameter of a statement.
func pgParam(n int) string { return fmt.Sprintf("$%d", n) }

// GetTurn reads one turn by id, in full. A missing row returns (nil, nil): "no such
// turn" is the caller's to describe (a 404), not a read failure.
func (s *PostgresStore) GetTurn(id int64) (*TurnRecord, error) {
	if id <= 0 {
		return nil, nil
	}
	rec, err := pgScanTurnRecord(s.readPool().QueryRow(`SELECT `+pgTurnColumns+` `+pgTurnFrom+` WHERE t.id = $1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get reason turn %d: %w", id, err)
	}
	return &rec, nil
}

// pgTurnWhere is the WHERE clause a TurnQuery asks for, and its arguments. Only the
// filters that were given appear, so an empty query is the whole table.
func pgTurnWhere(q TurnQuery) (string, []any) {
	var (
		conds []string
		args  []any
	)
	match := func(column, value string) {
		if value == "" {
			return
		}
		args = append(args, value)
		conds = append(conds, column+" = "+pgParam(len(args)))
	}
	match("t.task_id", q.TaskID)
	match("t.mode", string(q.Mode))
	match("t.model", q.Model)
	match("t.status", q.Status)
	match("a.name", q.Agent)
	if q.Search != "" {
		// The term is matched literally, so the escaping matters: searching for "100%"
		// must not answer with every row.
		//
		// ILIKE, not LIKE: the search is one feature of the data API, and the sqlite
		// engine's LIKE matches ASCII case-insensitively — so a postgres deployment
		// answering differently would be a second, accidental contract.
		args = append(args, turnSearchPattern(q.Search))
		first := pgParam(len(args))
		args = append(args, turnSearchPattern(q.Search))
		second := pgParam(len(args))
		conds = append(conds, `(t.input ILIKE `+first+` ESCAPE '\' OR t.raw_output ILIKE `+second+` ESCAPE '\')`)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// pgScanTurnRecord reads one row of pgTurnColumns. Timestamps come back as the
// contract's RFC3339 UTC text (pgTimeText): the log is evidence, and re-formatting
// evidence is how a reader ends up disagreeing with the database it read.
func pgScanTurnRecord(row interface{ Scan(dest ...any) error }) (TurnRecord, error) {
	var (
		rec       TurnRecord
		mode      string
		provider  string
		costCents sql.NullFloat64
		startedAt sql.NullTime
		endedAt   sql.NullTime
		createdAt time.Time
	)
	if err := row.Scan(
		&rec.ID, &rec.TaskID, &rec.Cycle, &mode,
		&rec.AgentID, &rec.Agent,
		&provider, &rec.Model, &rec.LLMAgentID,
		&rec.Input, &rec.Output, &rec.NormalizedOutput, &rec.RunID,
		&rec.Status, &rec.ErrorCode, &rec.ErrorMessage,
		&rec.DurationMS, &rec.EventCount,
		&rec.InputTokens, &rec.OutputTokens, &rec.CacheReadTokens, &rec.CacheWriteTokens,
		&rec.ReasoningTokens, &rec.TotalTokens, &costCents,
		&startedAt, &endedAt, &createdAt,
	); err != nil {
		return TurnRecord{}, err
	}
	rec.Mode = ReasonMode(mode)
	rec.Provider = llmbackend.Provider(provider)
	if costCents.Valid {
		cost := costCents.Float64
		rec.CostCents = &cost
	}
	// A NULL timestamp is an empty string: the contract says "the log has no time here"
	// rather than handing out a zero instant pretending to be one.
	rec.StartedAt = pgTimeText(pgScanTime(startedAt))
	rec.EndedAt = pgTimeText(pgScanTime(endedAt))
	rec.CreatedAt = pgTimeText(createdAt)
	return rec, nil
}

// pgScanTurnRecords reads every row of a turn query. A query that matched nothing is an
// empty slice, not nil: the contract answers with an empty array, and a nil Go slice
// would marshal as null.
func pgScanTurnRecords(rows *sql.Rows) ([]TurnRecord, error) {
	turns := []TurnRecord{}
	for rows.Next() {
		rec, err := pgScanTurnRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("scan reason turn: %w", err)
		}
		turns = append(turns, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reason turns: %w", err)
	}
	return turns, nil
}

// TurnFacets reads the filter bar's values: one aggregation per facet column, in the
// shape the bar renders (all of them, in one call).
func (s *PostgresStore) TurnFacets() (TurnFacets, error) {
	facets := TurnFacets{
		Tasks:     []TurnFacetValue{},
		Agents:    []TurnFacetValue{},
		Modes:     []TurnFacetValue{},
		Models:    []TurnFacetValue{},
		Providers: []TurnFacetValue{},
		Statuses:  []TurnFacetValue{},
	}
	// The agents facet is the *name*: a page filters by what it shows, and the join is
	// pgTurnFrom's, so a turn whose agent row is missing counts nowhere instead of
	// showing up as "".
	reads := []struct {
		into   *[]TurnFacetValue
		column string
	}{
		{&facets.Tasks, "t.task_id"},
		{&facets.Agents, "COALESCE(a.name, '')"},
		{&facets.Modes, "t.mode"},
		{&facets.Models, "t.model"},
		{&facets.Providers, "t.llm_provider"},
		{&facets.Statuses, "t.status"},
	}
	for _, read := range reads {
		values, err := s.pgTurnFacet(read.column)
		if err != nil {
			return TurnFacets{}, err
		}
		*read.into = values
	}
	return facets, nil
}

// pgTurnFacet aggregates one facet column: the values that are not empty, most-used
// first, value ascending when two are used equally often.
func (s *PostgresStore) pgTurnFacet(column string) ([]TurnFacetValue, error) {
	// column is an expression this file names, never caller input.
	query := `SELECT ` + column + ` AS value, COUNT(*) AS n ` + pgTurnFrom +
		` WHERE ` + column + ` IS NOT NULL AND ` + column + ` <> ''` +
		` GROUP BY ` + column + ` ORDER BY n DESC, value ASC`
	rows, err := s.readPool().Query(query)
	if err != nil {
		return nil, fmt.Errorf("facet %s: %w", column, err)
	}
	defer rows.Close()
	values := []TurnFacetValue{}
	for rows.Next() {
		var value TurnFacetValue
		if err := rows.Scan(&value.Value, &value.Count); err != nil {
			return nil, fmt.Errorf("scan facet %s: %w", column, err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate facet %s: %w", column, err)
	}
	return values, nil
}

// pgTaskOptionRow is one candidate of the task selector as a row: what the task row
// says it is, how many turns it has, and the newest of their times (zero when it has
// none). The time is kept beside the contract's text form because the selector is
// ordered by the instant, not by the characters of its rendering.
type pgTaskOptionRow struct {
	option TaskOption
	lastAt time.Time
}

// ListTaskOptions reads the task selector's candidates: the tasks that have a row,
// unioned with the task ids that only ever appear in the log. The second source is not
// noise — a task whose row was deleted, or one that ran before the row was written,
// still has turns to compare.
func (s *PostgresStore) ListTaskOptions() ([]TaskOption, error) {
	options := map[string]*pgTaskOptionRow{}
	known, err := s.readPool().Query(`
SELECT t.id, t.description, t.status, COUNT(r.id), MAX(r.created_at),
       t.context_ref, t.agent_id, t.updated_at
FROM tasks t LEFT JOIN reason_turns r ON r.task_id = t.id
GROUP BY t.id, t.description, t.status, t.context_ref, t.agent_id, t.updated_at`)
	if err != nil {
		return nil, fmt.Errorf("list task options: %w", err)
	}
	if err := pgScanTaskOptions(known, options); err != nil {
		return nil, err
	}
	// A task that only ever appears in the log has no row, so it has no world, no agent
	// and no update time: its project is "" and it is not one of any project's tasks.
	orphans, err := s.readPool().Query(`
SELECT r.task_id, ''::text AS description, ''::text AS status, COUNT(*), MAX(r.created_at),
       ''::text AS context_ref, 0::bigint AS agent_id, NULL::timestamptz AS updated_at
FROM reason_turns r
WHERE r.task_id <> '' AND NOT EXISTS (SELECT 1 FROM tasks t WHERE t.id = r.task_id)
GROUP BY r.task_id`)
	if err != nil {
		return nil, fmt.Errorf("list log-only task options: %w", err)
	}
	if err := pgScanTaskOptions(orphans, options); err != nil {
		return nil, err
	}
	return pgSortTaskOptions(options), nil
}

// pgScanTaskOptions folds one query's rows into the candidate set, keyed by task id so
// the two sources merge instead of repeating each other.
func pgScanTaskOptions(rows *sql.Rows, options map[string]*pgTaskOptionRow) error {
	defer rows.Close()
	for rows.Next() {
		var (
			row        pgTaskOptionRow
			lastAt     sql.NullTime
			updatedAt  sql.NullTime
			contextRef string
		)
		if err := rows.Scan(&row.option.ID, &row.option.Description, &row.option.Status,
			&row.option.Turns, &lastAt, &contextRef, &row.option.AgentID, &updatedAt); err != nil {
			return fmt.Errorf("scan task option: %w", err)
		}
		if row.option.ID == "" {
			continue
		}
		row.lastAt = pgScanTime(lastAt)
		row.option.LastAt = pgTimeText(row.lastAt)
		row.option.UpdatedAt = pgTimeText(pgScanTime(updatedAt))
		// The project is read out of the task's own world (tasks.context_ref): the column
		// is JSON text, and which key names the project is the contract's business
		// (src/store_row_text.go), not this query's.
		if refs, err := parseTaskContextRef(contextRef); err == nil {
			row.option.ProjectID = refs[ContextContainerTypeProject]
		}
		options[row.option.ID] = &row
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate task options: %w", err)
	}
	return nil
}

// pgSortTaskOptions orders the selector the way it is read: the most recently active
// task first, a task with no turns at all last (it has no activity to sort by), and
// ids ascending between two tasks that are equally recent, so the list is stable.
func pgSortTaskOptions(options map[string]*pgTaskOptionRow) []TaskOption {
	rows := make([]*pgTaskOptionRow, 0, len(options))
	for _, row := range options {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		if !left.lastAt.Equal(right.lastAt) {
			return left.lastAt.After(right.lastAt)
		}
		return left.option.ID < right.option.ID
	})
	sorted := make([]TaskOption, 0, len(rows))
	for _, row := range rows {
		sorted = append(sorted, row.option)
	}
	return sorted
}

// ListTurnsByTask reads one task's turns in the order they executed (created_at, then
// id — id alone is only the order the rows were written in), and how many turns the
// task has, which is what tells a capped page from a whole series.
func (s *PostgresStore) ListTurnsByTask(taskID string, limit int) ([]TurnRecord, int, error) {
	if strings.TrimSpace(taskID) == "" {
		return []TurnRecord{}, 0, nil
	}
	var total int
	if err := s.readPool().QueryRow(`SELECT COUNT(*) `+pgTurnFrom+` WHERE t.task_id = $1`, taskID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count task turns: %w", err)
	}
	rows, err := s.readPool().Query(`SELECT `+pgTurnColumns+` `+pgTurnFrom+
		` WHERE t.task_id = $1 ORDER BY t.created_at ASC, t.id ASC LIMIT $2`, taskID, taskTurnLimit(limit))
	if err != nil {
		return nil, 0, fmt.Errorf("list task turns: %w", err)
	}
	defer rows.Close()
	turns, err := pgScanTurnRecords(rows)
	if err != nil {
		return nil, 0, err
	}
	return turns, total, nil
}

// CountTurns is how many reason turns the log holds.
func (s *PostgresStore) CountTurns() (int, error) {
	var total int
	if err := s.readPool().QueryRow(`SELECT COUNT(*) FROM reason_turns`).Scan(&total); err != nil {
		return 0, fmt.Errorf("count reason turns: %w", err)
	}
	return total, nil
}
