package autonomy

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// The data API's read half, as the SQLite engine serves it (TurnQueryStore in
// src/store.go, the endpoints in src/turn_api.go and docs/http-api.md「数据 API」).
//
// They live in the engine because SQL, column names and the agent join are this
// engine's business, and they are plain SELECTs: filtering, ordering, paging,
// faceting and counting — the five things a list page, a comparison page and a
// filter bar ask of a log. Nothing here writes, and nothing here migrates.

// turnColumns is every column a TurnRecord read selects, in the order
// scanTurnRecord expects them: t is reason_turns, a is agents.
const turnColumns = `t.id, t.task_id, t.cycle, t.mode,
       t.agent_id, COALESCE(a.name, ''),
       t.llm_provider, t.model, t.llm_agent_id,
       t.input, t.raw_output, t.normalized_output, t.run_id,
       t.status, t.error_code, t.error_message,
       t.duration_ms, t.event_count,
       t.input_tokens, t.output_tokens, t.cache_read_tokens, t.cache_write_tokens,
       t.reasoning_tokens, t.total_tokens, t.cost_cents,
       t.started_at, t.ended_at, t.created_at`

// turnFrom is what a turn read is FROM: the log, joined to the agents it names. It
// is a LEFT JOIN because the agent of a turn is a name to show, not a precondition
// to read: a turn whose agent row is gone still reads, with an empty name.
const turnFrom = `FROM reason_turns t LEFT JOIN agents a ON a.id = t.agent_id`

// QueryTurns reads one page of reason turns, and the number of rows the same filter
// matches: the pager counts what it pages, not the table it pages over.
func (s *SQLiteStore) QueryTurns(q TurnQuery) ([]TurnRecord, int, error) {
	where, args := turnWhere(q)
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) `+turnFrom+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count reason turns: %w", err)
	}
	// ORDER BY <order> <dir>, id DESC: equal sort keys keep a stable order, so a
	// page boundary that lands inside them neither skips a row nor shows it twice.
	query := `SELECT ` + turnColumns + ` ` + turnFrom + where +
		` ORDER BY ` + turnOrderColumn(q.Order) + ` ` + turnSortDirection(q.Dir) +
		`, t.id DESC LIMIT ? OFFSET ?`
	rows, err := s.db.Query(query, append(args, turnPageLimit(q.Limit), turnPageOffset(q.Offset))...)
	if err != nil {
		return nil, 0, fmt.Errorf("query reason turns: %w", err)
	}
	defer rows.Close()
	turns, err := scanTurnRecords(rows)
	if err != nil {
		return nil, 0, err
	}
	return turns, total, nil
}

// GetTurn reads one turn by id, in full. A missing row returns (nil, nil): "no such
// turn" is the caller's to describe (a 404), not a read failure.
func (s *SQLiteStore) GetTurn(id int64) (*TurnRecord, error) {
	if id <= 0 {
		return nil, nil
	}
	rec, err := scanTurnRecord(s.db.QueryRow(`SELECT `+turnColumns+` `+turnFrom+` WHERE t.id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get reason turn %d: %w", id, err)
	}
	return &rec, nil
}

// turnWhere is the WHERE clause a TurnQuery asks for, and its arguments. Only the
// filters that were given appear, so an empty query is the whole table.
func turnWhere(q TurnQuery) (string, []any) {
	var (
		conds []string
		args  []any
	)
	match := func(column, value string) {
		if value == "" {
			return
		}
		conds = append(conds, column+" = ?")
		args = append(args, value)
	}
	match("t.task_id", q.TaskID)
	match("t.mode", string(q.Mode))
	match("t.model", q.Model)
	match("t.status", q.Status)
	match("a.name", q.Agent)
	if q.Search != "" {
		// The term is matched literally, so the escaping matters: searching for
		// "100%" must not answer with every row.
		conds = append(conds, `(t.input LIKE ? ESCAPE '\' OR t.raw_output LIKE ? ESCAPE '\')`)
		pattern := turnSearchPattern(q.Search)
		args = append(args, pattern, pattern)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// turnSearchPattern turns a search term into the LIKE pattern that matches it
// literally: the wildcards are characters the caller searched for, and so is the
// escape character itself.
func turnSearchPattern(term string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(term)
	return "%" + escaped + "%"
}

// turnOrderColumn is the whitelist behind the list's `order`: sorting is by one of
// four columns, and anything else reads as id. That is also why no caller string
// reaches the SQL text — an unknown order cannot become an injected one.
func turnOrderColumn(order string) string {
	switch strings.ToLower(strings.TrimSpace(order)) {
	case "created_at":
		return "t.created_at"
	case "duration_ms":
		return "t.duration_ms"
	case "total_tokens":
		return "t.total_tokens"
	default:
		return "t.id"
	}
}

// turnSortDirection is the direction the list is sorted in: ascending only when the
// caller says "asc", and descending otherwise — the reading order, newest first, is
// what an unqualified request means. Like turnOrderColumn, it answers with SQL this
// file names rather than with caller text.
func turnSortDirection(dir string) string {
	if strings.EqualFold(strings.TrimSpace(dir), "asc") {
		return "ASC"
	}
	return "DESC"
}

// turnPageLimit / turnPageOffset clamp a page request to a page that may exist: the
// bounds are the contract's (src/store.go), and clamping here as well as in the HTTP
// layer means a caller that clamped — and one that did not — get the same page.
func turnPageLimit(limit int) int {
	if limit <= 0 {
		return DefaultTurnPageLimit
	}
	if limit > MaxTurnPageLimit {
		return MaxTurnPageLimit
	}
	return limit
}

func turnPageOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}

// taskTurnLimit clamps one task's execution series the same way.
func taskTurnLimit(limit int) int {
	if limit <= 0 {
		return DefaultTaskTurnLimit
	}
	if limit > MaxTaskTurnLimit {
		return MaxTaskTurnLimit
	}
	return limit
}

// scanTurnRecord reads one row of turnColumns. agent_id is read as the driver's own
// value rather than straight into an int64 because this column was TEXT (an agents
// uuid) before agents had integer ids: a turn that old is still served — it simply
// joins no agent, and its agent name reads as empty — instead of failing the page it
// happens to sit on.
func scanTurnRecord(row interface{ Scan(dest ...any) error }) (TurnRecord, error) {
	var (
		rec       TurnRecord
		agentID   any
		mode      string
		provider  string
		costCents sql.NullFloat64
		startedAt sql.NullString
		endedAt   sql.NullString
	)
	if err := row.Scan(
		&rec.ID, &rec.TaskID, &rec.Cycle, &mode,
		&agentID, &rec.Agent,
		&provider, &rec.Model, &rec.LLMAgentID,
		&rec.Input, &rec.Output, &rec.NormalizedOutput, &rec.RunID,
		&rec.Status, &rec.ErrorCode, &rec.ErrorMessage,
		&rec.DurationMS, &rec.EventCount,
		&rec.InputTokens, &rec.OutputTokens, &rec.CacheReadTokens, &rec.CacheWriteTokens,
		&rec.ReasoningTokens, &rec.TotalTokens, &costCents,
		&startedAt, &endedAt, &rec.CreatedAt,
	); err != nil {
		return TurnRecord{}, err
	}
	rec.Mode = ReasonMode(mode)
	rec.Provider = LLMProvider(provider)
	rec.AgentID = turnAgentID(agentID)
	if costCents.Valid {
		cost := costCents.Float64
		rec.CostCents = &cost
	}
	// A NULL timestamp is an empty string: the contract says "the log has no time
	// here" rather than handing out a zero instant pretending to be one.
	rec.StartedAt = startedAt.String
	rec.EndedAt = endedAt.String
	return rec, nil
}

// scanTurnRecords reads every row of a turn query. A query that matched nothing is
// an empty slice, not nil: the contract answers with an empty array, and a nil Go
// slice would marshal as null.
func scanTurnRecords(rows *sql.Rows) ([]TurnRecord, error) {
	turns := []TurnRecord{}
	for rows.Next() {
		rec, err := scanTurnRecord(rows)
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

// turnAgentID reads a reason_turns.agent_id as the driver handed it over: an int64
// in today's schema, the text of a uuid in the schema this engine migrated — and
// anything else reads as 0, which is an agent no turn joins.
func turnAgentID(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case []byte:
		return turnAgentID(string(v))
	case string:
		id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0
		}
		return id
	default:
		return 0
	}
}

// TurnFacets reads the filter bar's values: one aggregation per facet column, in the
// shape the bar renders (all of them, in one call).
func (s *SQLiteStore) TurnFacets() (TurnFacets, error) {
	facets := TurnFacets{
		Tasks:     []TurnFacetValue{},
		Agents:    []TurnFacetValue{},
		Modes:     []TurnFacetValue{},
		Models:    []TurnFacetValue{},
		Providers: []TurnFacetValue{},
		Statuses:  []TurnFacetValue{},
	}
	// The agents facet is the *name*: a page filters by what it shows, and the join
	// is turnFrom's, so a turn whose agent row is missing counts nowhere instead of
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
		values, err := s.turnFacet(read.column)
		if err != nil {
			return TurnFacets{}, err
		}
		*read.into = values
	}
	return facets, nil
}

// turnFacet aggregates one facet column: the values that are not empty, most-used
// first, value ascending when two are used equally often.
func (s *SQLiteStore) turnFacet(column string) ([]TurnFacetValue, error) {
	// column is an expression this file names, never caller input.
	query := `SELECT ` + column + ` AS value, COUNT(*) AS n ` + turnFrom +
		` WHERE ` + column + ` IS NOT NULL AND ` + column + ` <> ''` +
		` GROUP BY ` + column + ` ORDER BY n DESC, value ASC`
	rows, err := s.db.Query(query)
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

// ListTaskOptions reads the task selector's candidates: the tasks that have a row,
// unioned with the task ids that only ever appear in the log. The second source is
// not noise — a task whose row was deleted, or one that ran before the row was
// written, still has turns to compare.
func (s *SQLiteStore) ListTaskOptions() ([]TaskOption, error) {
	options := map[string]*TaskOption{}
	// MAX(created_at) is the newest turn's time because every timestamp this engine
	// writes is UTC RFC3339 (formatTime): later in time is later in text.
	known, err := s.db.Query(`
SELECT t.id, t.description, t.status, COUNT(r.id), COALESCE(MAX(r.created_at), '')
FROM tasks t LEFT JOIN reason_turns r ON r.task_id = t.id
GROUP BY t.id, t.description, t.status`)
	if err != nil {
		return nil, fmt.Errorf("list task options: %w", err)
	}
	if err := scanTaskOptions(known, options); err != nil {
		return nil, err
	}
	orphans, err := s.db.Query(`
SELECT r.task_id, '' AS description, '' AS status, COUNT(*), COALESCE(MAX(r.created_at), '')
FROM reason_turns r
WHERE r.task_id <> '' AND NOT EXISTS (SELECT 1 FROM tasks t WHERE t.id = r.task_id)
GROUP BY r.task_id`)
	if err != nil {
		return nil, fmt.Errorf("list log-only task options: %w", err)
	}
	if err := scanTaskOptions(orphans, options); err != nil {
		return nil, err
	}
	return sortTaskOptions(options), nil
}

// scanTaskOptions folds one query's rows into the candidate set, keyed by task id so
// the two sources merge instead of repeating each other.
func scanTaskOptions(rows *sql.Rows, options map[string]*TaskOption) error {
	defer rows.Close()
	for rows.Next() {
		var option TaskOption
		if err := rows.Scan(&option.ID, &option.Description, &option.Status, &option.Turns, &option.LastAt); err != nil {
			return fmt.Errorf("scan task option: %w", err)
		}
		if option.ID == "" {
			continue
		}
		options[option.ID] = &option
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate task options: %w", err)
	}
	return nil
}

// sortTaskOptions orders the selector the way it is read: the most recently active
// task first, a task with no turns at all last (it has no activity to sort by), and
// ids ascending between two tasks that are equally recent, so the list is stable.
func sortTaskOptions(options map[string]*TaskOption) []TaskOption {
	sorted := make([]TaskOption, 0, len(options))
	for _, option := range options {
		sorted = append(sorted, *option)
	}
	sort.Slice(sorted, func(i, j int) bool {
		left, right := parseTime(sorted[i].LastAt), parseTime(sorted[j].LastAt)
		if !left.Equal(right) {
			return left.After(right)
		}
		return sorted[i].ID < sorted[j].ID
	})
	return sorted
}

// ListTurnsByTask reads one task's turns in the order they executed (created_at,
// then id — id alone is only the order the rows were written in), and how many turns
// the task has, which is what tells a capped page from a whole series.
func (s *SQLiteStore) ListTurnsByTask(taskID string, limit int) ([]TurnRecord, int, error) {
	if strings.TrimSpace(taskID) == "" {
		return []TurnRecord{}, 0, nil
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) `+turnFrom+` WHERE t.task_id = ?`, taskID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count task turns: %w", err)
	}
	rows, err := s.db.Query(`SELECT `+turnColumns+` `+turnFrom+
		` WHERE t.task_id = ? ORDER BY t.created_at ASC, t.id ASC LIMIT ?`, taskID, taskTurnLimit(limit))
	if err != nil {
		return nil, 0, fmt.Errorf("list task turns: %w", err)
	}
	defer rows.Close()
	turns, err := scanTurnRecords(rows)
	if err != nil {
		return nil, 0, err
	}
	return turns, total, nil
}

// CountTurns is how many reason turns the log holds.
func (s *SQLiteStore) CountTurns() (int, error) {
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM reason_turns`).Scan(&total); err != nil {
		return 0, fmt.Errorf("count reason turns: %w", err)
	}
	return total, nil
}
