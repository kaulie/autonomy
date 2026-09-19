package autonomy

import (
	"fmt"
	"strings"
	"time"
)

// VerificationStore, as the sqlite engine serves it: a task's pinned Completion
// Contract (written once) and its verdicts (appended, never updated). See
// docs/verification.md.

// AppendCompletionContract pins one criterion. The first row for a (task_id, idx) is the
// one kept: INSERT OR IGNORE is what makes "the contract cannot be rewritten" a fact of
// the schema rather than a check somebody has to remember to make.
func (s *SQLiteStore) AppendCompletionContract(criterion ContractCriterion) error {
	if strings.TrimSpace(criterion.TaskID) == "" {
		return fmt.Errorf("append completion contract: missing task id")
	}
	created := criterion.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	if _, err := s.db.Exec(`
INSERT OR IGNORE INTO completion_contract (task_id, idx, plan_id, name, criterion, created_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		criterion.TaskID, criterion.Idx, criterion.PlanID, criterion.Name,
		orJSONObjectText(criterion.Criterion), formatTime(created)); err != nil {
		return fmt.Errorf("append completion contract: %w", err)
	}
	return nil
}

// ListCompletionContract reads a task's pinned contract in criterion order.
func (s *SQLiteStore) ListCompletionContract(taskID string) ([]ContractCriterion, error) {
	rows, err := s.db.Query(`
SELECT id, task_id, idx, plan_id, name, criterion, created_at
FROM completion_contract WHERE task_id = ? ORDER BY idx`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list completion contract: %w", err)
	}
	defer rows.Close()
	out := []ContractCriterion{}
	for rows.Next() {
		var (
			id      int64
			row     ContractCriterion
			created string
		)
		if err := rows.Scan(&id, &row.TaskID, &row.Idx, &row.PlanID, &row.Name, &row.Criterion, &created); err != nil {
			return nil, fmt.Errorf("scan completion contract: %w", err)
		}
		row.CreatedAt = parseTime(created)
		out = append(out, row)
	}
	return out, rows.Err()
}

// AppendVerification records one verdict. A verdict is never rewritten: the question
// "what did the runtime believe at cycle 3" has one answer.
func (s *SQLiteStore) AppendVerification(v Verification) (int64, error) {
	created := v.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	res, err := s.db.Exec(`
INSERT INTO verification
  (task_id, plan_id, cycle, criterion, requirement, method, evidence, expected, observed, result, reason, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.TaskID, v.PlanID, v.Cycle, v.Criterion, v.Requirement, v.Method,
		orJSONObjectText(v.Evidence), v.Expected, v.Observed, v.Result, v.Reason, formatTime(created))
	if err != nil {
		return 0, fmt.Errorf("append verification: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("verification id: %w", err)
	}
	return id, nil
}

// ListVerifications reads a task's verdicts in creation order, oldest first.
func (s *SQLiteStore) ListVerifications(taskID string) ([]Verification, error) {
	rows, err := s.db.Query(`
SELECT id, task_id, plan_id, cycle, criterion, requirement, method, evidence,
       expected, observed, result, reason, created_at
FROM verification WHERE task_id = ? ORDER BY id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list verifications: %w", err)
	}
	defer rows.Close()
	out := []Verification{}
	for rows.Next() {
		var (
			row     Verification
			created string
		)
		if err := rows.Scan(&row.ID, &row.TaskID, &row.PlanID, &row.Cycle, &row.Criterion,
			&row.Requirement, &row.Method, &row.Evidence, &row.Expected, &row.Observed,
			&row.Result, &row.Reason, &created); err != nil {
			return nil, fmt.Errorf("scan verification: %w", err)
		}
		row.CreatedAt = parseTime(created)
		out = append(out, row)
	}
	return out, rows.Err()
}

// orJSONObjectText keeps a JSON object column as the JSON it is: the text if it parses
// as an object, an empty object otherwise (a column nobody can read is not a value).
func orJSONObjectText(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return "{}"
	}
	return trimmed
}
