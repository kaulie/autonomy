package db

import . "github.com/kaulie/autonomy/src"

import (
	"database/sql"
	"fmt"
	"github.com/kaulie/autonomy/src/llmbackend"
	"time"
)

// TaskStore and AgentStore reads, plus the ConversationStore reads the HTTP API polls
// with, as the postgres engine serves them (src/http_server.go). They live apart from
// postgres_store.go for the same reason the sqlite engine's do: they are the read half
// of the ports rather than part of the schema.

// GetTask reads one task by id. A missing row returns (nil, nil).
//
// A task's goal type and context references are read back with the row, not only held
// for the request that wrote them: they are what the task *is* — the world it was
// accepted into — so an instruction that names neither (src/api_service.go's accept)
// continues the task with them instead of stripping it.
func (s *PostgresStore) GetTask(taskID string) (*Task, error) {
	if taskID == "" {
		return nil, fmt.Errorf("empty task id")
	}
	task, err := pgScanTask(s.readPool().QueryRow(`
SELECT id, description, domain, goal_type, context_ref, status, error, agent_id, created_at, updated_at
FROM tasks WHERE id = $1`, taskID))
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
func (s *PostgresStore) ListTasks() ([]*Task, error) {
	rows, err := s.readPool().Query(`
SELECT id, description, domain, goal_type, context_ref, status, error, agent_id, created_at, updated_at
FROM tasks ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	var tasks []*Task
	for rows.Next() {
		task, err := pgScanTask(rows)
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

// pgTaskRow is one row of the task columns every task read selects, in that order.
// Both readers take the same columns so a task is the same value whichever way it was
// read (GetTask, ListTasks).
type pgTaskRow interface {
	Scan(dest ...any) error
}

// pgScanTask reads one task row: the columns the queries above select, in order. It
// reports sql.ErrNoRows unchanged, so a caller that asks for one task can tell "no such
// task" from "the read failed".
func pgScanTask(row pgTaskRow) (*Task, error) {
	var (
		task       Task
		domain     string
		goalType   string
		contextRef string
		createdAt  time.Time
		updatedAt  time.Time
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
	task.CreatedAt = createdAt.UTC()
	task.UpdatedAt = updatedAt.UTC()
	return &task, nil
}

// pgAgentColumns is the agent columns every agent read selects, in the order
// pgScanAgent expects. Both readers take the same columns so an agent is the same
// value whichever way it was read (GetAgent, ListAgents).
const pgAgentColumns = `id, name, state, lifecycle, current_task_id, context, llm_agent_id, llm_provider, model, account_id,
       created_at, updated_at, deleted_at`

// pgAgentRow is one row of the agent columns above.
type pgAgentRow interface {
	Scan(dest ...any) error
}

// pgScanAgent reads one agent row: the columns pgAgentColumns selects, in order.
// It reports sql.ErrNoRows unchanged, so a caller that asks for one agent can tell
// "no such agent" from "the read failed".
func pgScanAgent(row pgAgentRow) (*Agent, error) {
	var (
		a                    Agent
		lifecycle            string
		taskID               string
		provider             string
		createdAt, updatedAt time.Time
		deletedAt            sql.NullTime
	)
	if err := row.Scan(
		&a.ID, &a.Name, &a.State, &lifecycle, &taskID, &a.Context, &a.LLMAgentID, &provider, &a.Model, &a.AccountID,
		&createdAt, &updatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	a.Lifecycle = AgentLifecycle(lifecycle)
	a.LLMProvider = llmbackend.Provider(provider)
	a.DeletedAt = pgScanTime(deletedAt)
	if taskID != "" {
		a.CurrentTask = &Task{ID: taskID}
	}
	return &a, nil
}

// GetAgent reads one agent by id. A missing row returns (nil, nil).
func (s *PostgresStore) GetAgent(agentID int64) (*Agent, error) {
	if agentID == 0 {
		return nil, fmt.Errorf("empty agent id")
	}
	a, err := pgScanAgent(s.readPool().QueryRow(`SELECT `+pgAgentColumns+` FROM agents WHERE id = $1`, agentID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get agent: %w", err)
	}
	return a, nil
}

// ListAgents reads every agent row, oldest first (created_at, then id): the walk
// the status dashboard takes to enumerate the agents it shows (src/agent_dashboard.go).
func (s *PostgresStore) ListAgents() ([]*Agent, error) {
	rows, err := s.readPool().Query(`SELECT ` + pgAgentColumns + ` FROM agents ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()
	var agents []*Agent
	for rows.Next() {
		a, err := pgScanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("list agents: %w", err)
		}
		agents = append(agents, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	return agents, nil
}

// ActiveReasonTurn returns the newest in-flight reason turn for the agent on the task
// (status = running). A missing row returns (nil, nil).
func (s *PostgresStore) ActiveReasonTurn(taskID string, agentID int64) (*ReasonTurn, error) {
	if taskID == "" || agentID == 0 {
		return nil, nil
	}
	var (
		turn               ReasonTurn
		mode               string
		provider           string
		startedAt, endedAt sql.NullTime
		createdAt          time.Time
		costCents          sql.NullFloat64
	)
	err := s.readPool().QueryRow(`
SELECT id, task_id, agent_id, cycle, mode, llm_provider, model, llm_agent_id,
       input, raw_output, normalized_output, run_id, status, error_code, error_message,
       duration_ms, event_count, input_tokens, output_tokens, cache_read_tokens,
       cache_write_tokens, reasoning_tokens, total_tokens, cost_cents,
       started_at, ended_at, created_at
FROM reason_turns
WHERE task_id = $1 AND agent_id = $2 AND status = $3
ORDER BY id DESC LIMIT 1`, taskID, agentID, string(llmbackend.StatusRunning)).Scan(
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
	turn.LLMProvider = llmbackend.Provider(provider)
	turn.CreatedAt = createdAt.UTC()
	turn.StartedAt = pgScanTime(startedAt)
	turn.EndedAt = pgScanTime(endedAt)
	if costCents.Valid {
		v := costCents.Float64
		turn.CostCents = &v
	}
	return &turn, nil
}

// ListLLMMessagesAfter returns the agent's conversation stream across turns for rows
// with id > afterID, ascending. limit <= 0 defaults to 200.
func (s *PostgresStore) ListLLMMessagesAfter(taskID string, agentID, afterID int64, limit int) ([]LLMMessage, error) {
	if taskID == "" || agentID == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.readPool().Query(`
SELECT id, turn_id, task_id, agent_id, cycle, seq, role, parent_id,
content, normalized_content, llm_provider, model, run_id, status, created_at
FROM llm_messages
WHERE task_id = $1 AND agent_id = $2 AND id > $3
ORDER BY id ASC
LIMIT $4`, taskID, agentID, afterID, limit)
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
			createdAt time.Time
		)
		if err := rows.Scan(&m.ID, &m.TurnID, &m.TaskID, &m.AgentID, &m.Cycle, &m.Seq, &role,
			&parentID, &m.Content, &m.NormalizedContent, &provider, &m.Model, &m.RunID, &m.Status,
			&createdAt); err != nil {
			return nil, fmt.Errorf("scan llm message: %w", err)
		}
		m.Role = LLMMessageRole(role)
		m.LLMProvider = llmbackend.Provider(provider)
		if parentID.Valid {
			m.ParentID = parentID.Int64
		}
		m.CreatedAt = createdAt.UTC()
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate llm messages: %w", err)
	}
	return out, nil
}
