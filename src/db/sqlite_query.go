package db

import . "github.com/kaulie/autonomy/src"

import (
	"database/sql"
	"fmt"
)

// TaskStore and AgentStore reads, plus the ConversationStore reads the HTTP API
// polls with, as the sqlite engine serves them (src/http_server.go). They live
// apart from sqlite_store.go because they are the read half of the ports rather
// than part of the schema/migration path.

// GetTask reads one task by id. A missing row returns (nil, nil).
//
// A task's goal type and context references are read back with the row, not only
// held for the request that wrote them: they are what the task *is* — the world it
// was accepted into — so an instruction that names neither (src/api_service.go's
// accept) continues the task with them instead of stripping it.
func (s *SQLiteStore) GetTask(taskID string) (*Task, error) {
	if taskID == "" {
		return nil, fmt.Errorf("empty task id")
	}
	task, err := scanTask(s.db.QueryRow(`
SELECT id, description, domain, goal_type, context_ref, status, error, agent_id, created_at, updated_at
FROM tasks WHERE id = ?`, taskID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}
	return task, nil
}

// ListTasks reads every task row, oldest first (created_at, then id): the walk a
// broadcast takes to resolve its scope (src/broadcast.go).
func (s *SQLiteStore) ListTasks() ([]*Task, error) {
	rows, err := s.db.Query(`
SELECT id, description, domain, goal_type, context_ref, status, error, agent_id, created_at, updated_at
FROM tasks ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	var tasks []*Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("list tasks: %w", err)
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	return tasks, nil
}

// taskRow is one row of the task columns every task read selects, in that order.
// Both readers take the same columns so a task is the same value whichever way it
// was read (GetTask, ListTasks).
type taskRow interface {
	Scan(dest ...any) error
}

// scanTask reads one task row: the columns the queries above select, in order.
// It reports sql.ErrNoRows unchanged, so a caller that asks for one task can tell
// "no such task" from "the read failed".
func scanTask(row taskRow) (*Task, error) {
	var (
		task       Task
		domain     string
		goalType   string
		contextRef string
		createdAt  string
		updatedAt  string
	)
	if err := row.Scan(
		&task.ID, &task.Description, &domain, &goalType, &contextRef, &task.Status, &task.Error,
		&task.AgentID, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	refs, err := parseTaskContextRef(contextRef)
	if err != nil {
		return nil, fmt.Errorf("task %s context_ref: %w", task.ID, err)
	}
	task.Domain = TaskDomain(domain)
	task.GoalType = GoalType(goalType)
	task.ContextRef = refs
	task.CreatedAt = parseTime(createdAt)
	task.UpdatedAt = parseTime(updatedAt)
	return &task, nil
}

// GetAgent reads one agent by id. A missing row returns (nil, nil).
func (s *SQLiteStore) GetAgent(agentID int64) (*Agent, error) {
	if agentID == 0 {
		return nil, fmt.Errorf("empty agent id")
	}
	var (
		a         Agent
		lifecycle string
		taskID    string
		provider  string
		createdAt string
		updatedAt string
		deletedAt sql.NullString
	)
	err := s.db.QueryRow(`
SELECT id, name, state, lifecycle, current_task_id, context, llm_agent_id, llm_provider, model,
       created_at, updated_at, deleted_at
FROM agents WHERE id = ?`, agentID).Scan(
		&a.ID, &a.Name, &a.State, &lifecycle, &taskID, &a.Context, &a.LLMAgentID, &provider, &a.Model,
		&createdAt, &updatedAt, &deletedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get agent: %w", err)
	}
	a.Lifecycle = AgentLifecycle(lifecycle)
	a.LLMProvider = LLMProvider(provider)
	if deletedAt.Valid {
		a.DeletedAt = parseTime(deletedAt.String)
	}
	if taskID != "" {
		a.CurrentTask = &Task{ID: taskID}
	}
	return &a, nil
}

// ActiveReasonTurn returns the newest in-flight reason turn for the agent on the
// task (status = running). A missing row returns (nil, nil).
func (s *SQLiteStore) ActiveReasonTurn(taskID string, agentID int64) (*ReasonTurn, error) {
	if taskID == "" || agentID == 0 {
		return nil, nil
	}
	var (
		turn      ReasonTurn
		mode      string
		provider  string
		startedAt sql.NullString
		endedAt   sql.NullString
		createdAt string
		costCents sql.NullFloat64
	)
	err := s.db.QueryRow(`
SELECT id, task_id, agent_id, cycle, mode, llm_provider, model, llm_agent_id,
       input, raw_output, normalized_output, run_id, status, error_code, error_message,
       duration_ms, event_count, input_tokens, output_tokens, cache_read_tokens,
       cache_write_tokens, reasoning_tokens, total_tokens, cost_cents,
       started_at, ended_at, created_at
FROM reason_turns
WHERE task_id = ? AND agent_id = ? AND status = ?
ORDER BY id DESC LIMIT 1`, taskID, agentID, string(LLMStatusRunning)).Scan(
		&turn.ID, &turn.TaskID, &turn.AgentID, &turn.Cycle, &mode, &provider, &turn.Model, &turn.LLMAgentID,
		&turn.Input, &turn.RawOutput, &turn.NormalizedOutput, &turn.RunID, &turn.Status, &turn.ErrorCode, &turn.ErrorMessage,
		&turn.DurationMS, &turn.EventCount, &turn.InputTokens, &turn.OutputTokens, &turn.CacheReadTokens,
		&turn.CacheWriteTokens, &turn.ReasoningTokens, &turn.TotalTokens, &costCents,
		&startedAt, &endedAt, &createdAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("active reason turn: %w", err)
	}
	turn.Mode = ReasonMode(mode)
	turn.LLMProvider = LLMProvider(provider)
	turn.CreatedAt = parseTime(createdAt)
	if startedAt.Valid {
		turn.StartedAt = parseTime(startedAt.String)
	}
	if endedAt.Valid {
		turn.EndedAt = parseTime(endedAt.String)
	}
	if costCents.Valid {
		v := costCents.Float64
		turn.CostCents = &v
	}
	return &turn, nil
}

// ListLLMMessagesAfter returns the agent's conversation stream across turns for
// rows with id > afterID, ascending. limit <= 0 defaults to 200.
func (s *SQLiteStore) ListLLMMessagesAfter(taskID string, agentID, afterID int64, limit int) ([]LLMMessage, error) {
	if taskID == "" || agentID == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(`
SELECT id, turn_id, task_id, agent_id, cycle, seq, role, parent_id,
content, normalized_content, llm_provider, model, run_id, status, created_at
FROM llm_messages
WHERE task_id = ? AND agent_id = ? AND id > ?
ORDER BY id ASC
LIMIT ?`, taskID, agentID, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list llm messages after: %w", err)
	}
	defer rows.Close()
	var out []LLMMessage
	for rows.Next() {
		var (
			m         LLMMessage
			role      string
			provider  string
			parentID  sql.NullInt64
			createdAt string
		)
		if err := rows.Scan(&m.ID, &m.TurnID, &m.TaskID, &m.AgentID, &m.Cycle, &m.Seq, &role,
			&parentID, &m.Content, &m.NormalizedContent, &provider, &m.Model, &m.RunID, &m.Status,
			&createdAt); err != nil {
			return nil, fmt.Errorf("scan llm message: %w", err)
		}
		m.Role = LLMMessageRole(role)
		m.LLMProvider = LLMProvider(provider)
		if parentID.Valid {
			m.ParentID = parentID.Int64
		}
		m.CreatedAt = parseTime(createdAt)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate llm messages: %w", err)
	}
	return out, nil
}
