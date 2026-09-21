package db

import . "github.com/kaulie/autonomy/src"

import (
	"database/sql"
	"fmt"
	"time"
)

// InboxStore, as the sqlite engine serves it: one row per message per agent.
//
// The queue order is the row id, so "the next message" is the oldest queued row
// for that agent — no separate sequence, no clock to trust. Claiming it is one
// guarded UPDATE, so two consumers can never take the same message: the second
// one sees status != queued and finds nothing.

// inboxDDL creates the inbox table and the indexes the queue reads it with.
const inboxDDL = `
CREATE TABLE IF NOT EXISTS agent_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  agent_id INTEGER NOT NULL,
  task_id TEXT NOT NULL DEFAULT '',
  sender TEXT NOT NULL DEFAULT '',
  sender_id TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'queued',
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  started_at TEXT,
  ended_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_agent_messages_queue ON agent_messages(agent_id, status, id);
CREATE INDEX IF NOT EXISTS idx_agent_messages_task ON agent_messages(task_id, id);
`

// EnqueueMessage appends one message to an agent's inbox and returns its id.
func (s *SQLiteStore) EnqueueMessage(msg AgentMessage) (int64, error) {
	if msg.AgentID == 0 {
		return 0, fmt.Errorf("enqueue message: no agent")
	}
	status := msg.Status
	if status == "" {
		status = MessageStatusQueued
	}
	created := msg.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	res, err := s.db.Exec(`
INSERT INTO agent_messages (agent_id, task_id, sender, sender_id, kind, content, status, error, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, '', ?)
`, msg.AgentID, msg.TaskID, string(msg.Sender), msg.SenderID, string(msg.Kind), msg.Content, string(status), formatTime(created))
	if err != nil {
		return 0, fmt.Errorf("enqueue message: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("enqueue message id: %w", err)
	}
	return id, nil
}

// ClaimNextMessage takes the oldest queued message for one agent and marks it
// running. found is false when the inbox is empty, and also when another consumer
// won the race for that message (the caller asks again — the queue is what it is).
func (s *SQLiteStore) ClaimNextMessage(agentID int64) (AgentMessage, bool, error) {
	if agentID == 0 {
		return AgentMessage{}, false, nil
	}
	var id int64
	err := s.db.QueryRow(`
SELECT id FROM agent_messages WHERE agent_id = ? AND status = ? ORDER BY id LIMIT 1
`, agentID, string(MessageStatusQueued)).Scan(&id)
	if err == sql.ErrNoRows {
		return AgentMessage{}, false, nil
	}
	if err != nil {
		return AgentMessage{}, false, fmt.Errorf("claim message: %w", err)
	}
	res, err := s.db.Exec(`
UPDATE agent_messages SET status = ?, started_at = ? WHERE id = ? AND status = ?
`, string(MessageStatusRunning), formatTime(time.Now()), id, string(MessageStatusQueued))
	if err != nil {
		return AgentMessage{}, false, fmt.Errorf("claim message %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return AgentMessage{}, false, fmt.Errorf("claim message %d: %w", id, err)
	}
	if n == 0 {
		return AgentMessage{}, false, nil
	}
	msg, err := s.agentMessage(id)
	if err != nil {
		return AgentMessage{}, false, err
	}
	return msg, true, nil
}

// agentMessage reads one row back as the message the queue works with.
func (s *SQLiteStore) agentMessage(id int64) (AgentMessage, error) {
	var (
		msg              AgentMessage
		sender, senderID string
		kind, status     string
		created          string
		started, ended   sql.NullString
	)
	err := s.db.QueryRow(`
SELECT id, agent_id, task_id, sender, sender_id, kind, content, status, error, created_at, started_at, ended_at
FROM agent_messages WHERE id = ?
`, id).Scan(&msg.ID, &msg.AgentID, &msg.TaskID, &sender, &senderID, &kind, &msg.Content, &status, &msg.Error,
		&created, &started, &ended)
	if err != nil {
		return AgentMessage{}, fmt.Errorf("read message %d: %w", id, err)
	}
	msg.Sender = MessageSender(sender)
	msg.SenderID = senderID
	msg.Kind = AgentMessageKind(kind)
	msg.Status = AgentMessageStatus(status)
	msg.CreatedAt = parseTime(created)
	if started.Valid {
		msg.StartedAt = parseTime(started.String)
	}
	if ended.Valid {
		msg.EndedAt = parseTime(ended.String)
	}
	return msg, nil
}

// FinishAgentMessage records how one message ended.
func (s *SQLiteStore) FinishAgentMessage(id int64, status AgentMessageStatus, errText string) error {
	if status == "" {
		status = MessageStatusDone
	}
	if _, err := s.db.Exec(`
UPDATE agent_messages SET status = ?, error = ?, ended_at = ? WHERE id = ?
`, string(status), errText, formatTime(time.Now()), id); err != nil {
		return fmt.Errorf("finish message %d: %w", id, err)
	}
	return nil
}

// RequeueRunningMessages puts an agent's running messages back in the queue: a
// message a process claimed and then died on is still the agent's to process.
func (s *SQLiteStore) RequeueRunningMessages(agentID int64) error {
	if agentID == 0 {
		return nil
	}
	if _, err := s.db.Exec(`
UPDATE agent_messages SET status = ?, started_at = NULL WHERE agent_id = ? AND status = ?
`, string(MessageStatusQueued), agentID, string(MessageStatusRunning)); err != nil {
		return fmt.Errorf("requeue messages of agent %d: %w", agentID, err)
	}
	return nil
}

// RequeueMessage puts one claimed message back in the queue, where it was: a run that
// must not start yet (a graceful restart is draining, src/graceful.go) is left for the
// consumer that starts next. Only the row that is running goes back — the id is the
// message's place in the queue, so it is claimed again before anything behind it.
func (s *SQLiteStore) RequeueMessage(id int64) error {
	if id == 0 {
		return nil
	}
	if _, err := s.db.Exec(`
UPDATE agent_messages SET status = ?, started_at = NULL WHERE id = ? AND status = ?
`, string(MessageStatusQueued), id, string(MessageStatusRunning)); err != nil {
		return fmt.Errorf("requeue message %d: %w", id, err)
	}
	return nil
}

// CountQueuedMessages counts the messages waiting for one agent — what the drain
// asks to know whether the message it just finished was the last one.
func (s *SQLiteStore) CountQueuedMessages(agentID int64) (int, error) {
	if agentID == 0 {
		return 0, nil
	}
	var n int
	if err := s.db.QueryRow(`
SELECT count(*) FROM agent_messages WHERE agent_id = ? AND status = ?
`, agentID, string(MessageStatusQueued)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count queued messages of agent %d: %w", agentID, err)
	}
	return n, nil
}

// CountMessagesAhead counts the messages an agent still has in front of one
// message: the ones that arrived before it and have not finished (queued, or being
// processed right now). The queue's order is the row id, so "before it" is `id <`.
func (s *SQLiteStore) CountMessagesAhead(agentID, messageID int64) (int, error) {
	if agentID == 0 || messageID == 0 {
		return 0, nil
	}
	var n int
	if err := s.db.QueryRow(`
SELECT count(*) FROM agent_messages
WHERE agent_id = ? AND id < ? AND status IN (?, ?)
`, agentID, messageID, string(MessageStatusQueued), string(MessageStatusRunning)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count messages ahead of %d for agent %d: %w", messageID, agentID, err)
	}
	return n, nil
}

// ListAgentMessages reads an agent's inbox in arrival order. limit <= 0 means all
// of it.
func (s *SQLiteStore) ListAgentMessages(agentID int64, limit int) ([]AgentMessage, error) {
	if agentID == 0 {
		return nil, nil
	}
	query := `
SELECT id FROM agent_messages WHERE agent_id = ? ORDER BY id`
	args := []any{agentID}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list messages of agent %d: %w", agentID, err)
	}
	defer rows.Close()
	var out []AgentMessage
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("list messages of agent %d: %w", agentID, err)
		}
		msg, err := s.agentMessage(id)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list messages of agent %d: %w", agentID, err)
	}
	return out, nil
}
