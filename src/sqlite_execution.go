package autonomy

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ExecutionStore, as the sqlite engine serves it: the execution plan/step tables.
// Every method here inserts or reads: a plan is written before its steps run and
// never rewritten, its outcome is derived from the steps, and nothing is keyed on
// the cycle (see docs/execution-step.md).

// CreateExecutionPlan writes one plan. This is the only statement that ever touches
// the row: a plan is one-shot, and a re-plan is a new plan.
func (s *SQLiteStore) CreateExecutionPlan(plan ExecutionPlan) (int64, error) {
	if strings.TrimSpace(plan.TaskID) == "" {
		return 0, fmt.Errorf("create execution plan: missing task id")
	}
	created := plan.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	res, err := s.db.Exec(`
INSERT INTO execution_plan
  (task_id, agent_id, cycle, decision_type, reason, evidence, need, step_count, plan_hash,
   reply_message_id, input_message_id, task_input_message_id, reason_turn_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		plan.TaskID, plan.AgentID, plan.Cycle, plan.DecisionType, plan.Reason,
		orJSONArray(plan.Evidence), orJSONObject(plan.Need), plan.StepCount, plan.PlanHash,
		nullID(plan.ReplyMessageID), nullID(plan.InputMessageID),
		nullID(plan.TaskInputMessageID), nullID(plan.ReasonTurnID),
		formatTime(created))
	if err != nil {
		return 0, fmt.Errorf("create execution plan: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("execution plan id: %w", err)
	}
	return id, nil
}

// AppendExecutionStepPlans writes a plan's steps in one transaction: a plan is
// either written in full before its first step runs, or not at all.
func (s *SQLiteStore) AppendExecutionStepPlans(steps []ExecutionStepPlan) error {
	if len(steps) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin execution step plan tx: %w", err)
	}
	stmt, err := tx.Prepare(`
INSERT INTO execution_step_plan (plan_id, idx, name, capability, input, expected_effect, evidence_refs, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare execution step plan insert: %w", err)
	}
	defer stmt.Close()
	for _, step := range steps {
		created := step.CreatedAt
		if created.IsZero() {
			created = time.Now()
		}
		if _, err := stmt.Exec(step.PlanID, step.Idx, step.Name, step.Capability, orJSONObject(step.Input),
			step.ExpectedEffect, orJSONArray(step.EvidenceRefs), formatTime(created)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("append execution step plan: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit execution step plan: %w", err)
	}
	return nil
}

// AppendExecutionStep records one step that ran, linked to the planned step it
// carries out by id, so a re-plan's identical step can never be confused with it.
func (s *SQLiteStore) AppendExecutionStep(step ExecutionStep) (int64, error) {
	created := step.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	res, err := s.db.Exec(`
INSERT INTO execution_step
  (plan_id, plan_step_id, task_id, agent_id, cycle, idx, name, capability, provider, status,
   input, output, error, started_at, ended_at, duration_ms, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		step.PlanID, step.PlanStepID, step.TaskID, step.AgentID, step.Cycle, step.Idx,
		step.Name, step.Capability, step.Provider, step.Status,
		orJSONObject(step.Input), orJSONObject(step.Output), step.Error,
		formatTime(step.StartedAt), nullTimeArg(step.EndedAt), step.DurationMS, formatTime(created))
	if err != nil {
		return 0, fmt.Errorf("append execution step: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("execution step id: %w", err)
	}
	return id, nil
}

// AppendExecutionStepInteraction records one provider interaction of one step.
func (s *SQLiteStore) AppendExecutionStepInteraction(in ExecutionStepInteraction) (int64, error) {
	created := in.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	res, err := s.db.Exec(`
INSERT INTO execution_step_interaction (step_id, seq, kind, provider, reason_turn_id, created_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		in.StepID, in.Seq, in.Kind, in.Provider, nullID(in.ReasonTurnID), formatTime(created))
	if err != nil {
		return 0, fmt.Errorf("append execution step interaction: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("execution step interaction id: %w", err)
	}
	return id, nil
}

// AssistantMessageID is the row a run's reply was recorded in. The reply is the
// run's last message, so it is read back by role rather than assumed by seq (a run
// whose stream ended mid-flight still has its return as the assistant row).
func (s *SQLiteStore) AssistantMessageID(turnID int64) (int64, bool, error) {
	var id int64
	err := s.db.QueryRow(`
SELECT id FROM llm_messages
 WHERE turn_id = ? AND role = ? ORDER BY seq DESC LIMIT 1`,
		turnID, string(LLMMessageRoleAssistant)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read assistant message: %w", err)
	}
	return id, true, nil
}

// TaskInputMessageID is the task's own first user input, as recorded on its first
// plan. Every plan of a task points at it, so "which input asked for this" is one
// hop from any plan of any cycle.
func (s *SQLiteStore) TaskInputMessageID(taskID string) (int64, bool, error) {
	var id int64
	err := s.db.QueryRow(`
SELECT task_input_message_id FROM execution_plan
 WHERE task_id = ? AND task_input_message_id IS NOT NULL
 ORDER BY id LIMIT 1`, taskID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read task input message: %w", err)
	}
	return id, true, nil
}

// ListExecutionPlans reads a task's plans in creation order, oldest first.
func (s *SQLiteStore) ListExecutionPlans(taskID string) ([]ExecutionPlan, error) {
	rows, err := s.db.Query(`
SELECT id, task_id, agent_id, cycle, decision_type, reason, evidence, need, step_count,
       plan_hash, reply_message_id, input_message_id, task_input_message_id, reason_turn_id, created_at
  FROM execution_plan WHERE task_id = ? ORDER BY id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list execution plans: %w", err)
	}
	defer rows.Close()
	out := []ExecutionPlan{}
	for rows.Next() {
		var (
			plan                            ExecutionPlan
			reply, input, taskInput, turnID sql.NullInt64
			createdAt                       string
		)
		if err := rows.Scan(&plan.ID, &plan.TaskID, &plan.AgentID, &plan.Cycle, &plan.DecisionType,
			&plan.Reason, &plan.Evidence, &plan.Need, &plan.StepCount, &plan.PlanHash,
			&reply, &input, &taskInput, &turnID, &createdAt); err != nil {
			return nil, fmt.Errorf("scan execution plan: %w", err)
		}
		plan.ReplyMessageID = reply.Int64
		plan.InputMessageID = input.Int64
		plan.TaskInputMessageID = taskInput.Int64
		plan.ReasonTurnID = turnID.Int64
		plan.CreatedAt = parseTime(createdAt)
		out = append(out, plan)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list execution plans: %w", err)
	}
	return out, nil
}

// ListExecutionStepPlan reads a plan's planned steps in plan order.
func (s *SQLiteStore) ListExecutionStepPlan(planID int64) ([]ExecutionStepPlan, error) {
	rows, err := s.db.Query(`
SELECT id, plan_id, idx, name, capability, input, expected_effect, evidence_refs, created_at
  FROM execution_step_plan WHERE plan_id = ? ORDER BY idx`, planID)
	if err != nil {
		return nil, fmt.Errorf("list execution step plan: %w", err)
	}
	defer rows.Close()
	out := []ExecutionStepPlan{}
	for rows.Next() {
		var step ExecutionStepPlan
		var createdAt string
		if err := rows.Scan(&step.ID, &step.PlanID, &step.Idx, &step.Name, &step.Capability, &step.Input,
			&step.ExpectedEffect, &step.EvidenceRefs, &createdAt); err != nil {
			return nil, fmt.Errorf("scan execution step plan: %w", err)
		}
		step.CreatedAt = parseTime(createdAt)
		out = append(out, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list execution step plan: %w", err)
	}
	return out, nil
}

// ListExecutionSteps reads a plan's executed steps in execution order.
func (s *SQLiteStore) ListExecutionSteps(planID int64) ([]ExecutionStep, error) {
	rows, err := s.db.Query(`
SELECT id, plan_id, plan_step_id, task_id, agent_id, cycle, idx, name, capability, provider, status,
       input, output, error, started_at, ended_at, duration_ms, created_at
  FROM execution_step WHERE plan_id = ? ORDER BY idx, id`, planID)
	if err != nil {
		return nil, fmt.Errorf("list execution steps: %w", err)
	}
	defer rows.Close()
	out := []ExecutionStep{}
	for rows.Next() {
		var (
			step      ExecutionStep
			startedAt string
			endedAt   sql.NullString
			createdAt string
		)
		if err := rows.Scan(&step.ID, &step.PlanID, &step.PlanStepID, &step.TaskID, &step.AgentID,
			&step.Cycle, &step.Idx, &step.Name, &step.Capability, &step.Provider, &step.Status,
			&step.Input, &step.Output, &step.Error, &startedAt, &endedAt, &step.DurationMS,
			&createdAt); err != nil {
			return nil, fmt.Errorf("scan execution step: %w", err)
		}
		step.StartedAt = parseTime(startedAt)
		if endedAt.Valid {
			step.EndedAt = parseTime(endedAt.String)
		}
		step.CreatedAt = parseTime(createdAt)
		out = append(out, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list execution steps: %w", err)
	}
	return out, nil
}

// ListExecutionStepInteractions reads one step's interactions in Seq order.
func (s *SQLiteStore) ListExecutionStepInteractions(stepID int64) ([]ExecutionStepInteraction, error) {
	rows, err := s.db.Query(`
SELECT id, step_id, seq, kind, provider, reason_turn_id, created_at
  FROM execution_step_interaction WHERE step_id = ? ORDER BY seq`, stepID)
	if err != nil {
		return nil, fmt.Errorf("list execution step interactions: %w", err)
	}
	defer rows.Close()
	out := []ExecutionStepInteraction{}
	for rows.Next() {
		var (
			in        ExecutionStepInteraction
			turnID    sql.NullInt64
			createdAt string
		)
		if err := rows.Scan(&in.ID, &in.StepID, &in.Seq, &in.Kind, &in.Provider, &turnID, &createdAt); err != nil {
			return nil, fmt.Errorf("scan execution step interaction: %w", err)
		}
		in.ReasonTurnID = turnID.Int64
		in.CreatedAt = parseTime(createdAt)
		out = append(out, in)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list execution step interactions: %w", err)
	}
	return out, nil
}

// ExecutionPlanOutcome derives a plan's result from its steps: how much of the plan
// ran and how the last step of it ended. A plan whose steps never ran has no
// outcome — its planned steps are still there, which is the whole record of it.
func (s *SQLiteStore) ExecutionPlanOutcome(planID int64) (ExecutionPlanOutcome, bool, error) {
	out := ExecutionPlanOutcome{PlanID: planID}
	if err := s.db.QueryRow(`SELECT count(*) FROM execution_step_plan WHERE plan_id = ?`, planID).
		Scan(&out.Planned); err != nil {
		return out, false, fmt.Errorf("count planned steps: %w", err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM execution_step WHERE plan_id = ?`, planID).
		Scan(&out.Executed); err != nil {
		return out, false, fmt.Errorf("count executed steps: %w", err)
	}
	err := s.db.QueryRow(`
SELECT id, status, error FROM execution_step
 WHERE plan_id = ? ORDER BY idx DESC, id DESC LIMIT 1`, planID).
		Scan(&out.StepID, &out.Status, &out.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, fmt.Errorf("read last execution step: %w", err)
	}
	return out, true, nil
}

// nullID writes 0 as SQL NULL: "no such message" and "message 0" are not the same
// thing, and the traceability columns are empty when there was nothing to point at.
func nullID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

// orJSONArray / orJSONObject keep a JSON column valid even when the caller left it
// empty, so a reader never has to guess what an empty string means.
func orJSONArray(text string) string {
	if strings.TrimSpace(text) == "" {
		return "[]"
	}
	return text
}

func orJSONObject(text string) string {
	if strings.TrimSpace(text) == "" {
		return "{}"
	}
	return text
}
