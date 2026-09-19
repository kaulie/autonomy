package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The request and response shapes of the runtime's HTTP API, as a caller sees
// them (docs/http-api.md). They are declared here on purpose: this command links
// none of the runtime in, so the JSON in the docs *is* the contract — a field
// renamed here is a bug the API catches, not the compiler.

// acceptRequest is the body of POST /api/tasks.
type acceptRequest struct {
	TaskID      string            `json:"task_id,omitempty"`
	Description string            `json:"description"`
	Domain      string            `json:"domain,omitempty"`
	GoalType    string            `json:"goal_type,omitempty"`
	ContextRef  map[string]string `json:"context_ref,omitempty"`
}

// acceptResponse is what the runtime answers an accepted instruction with: the
// task it is about, the agent that will do it, and where the instruction stands
// in that agent's queue (message_id, queued).
type acceptResponse struct {
	TaskID    string `json:"task_id"`
	AgentID   int64  `json:"agent_id"`
	Status    string `json:"status"`
	MessageID int64  `json:"message_id,omitempty"`
	Queued    int    `json:"queued"`
}

// stopResponse is what POST /api/tasks/{id}/stop answers.
type stopResponse struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}

// taskProgress is GET /api/tasks/{id}: the task's status and every plan it has
// run, each with the steps it planned and what executing them did.
type taskProgress struct {
	TaskID      string         `json:"task_id"`
	Description string         `json:"description"`
	Domain      string         `json:"domain"`
	Status      string         `json:"status"`
	Error       string         `json:"error,omitempty"`
	AgentID     int64          `json:"agent_id"`
	Plans       []planProgress `json:"plans"`
}

type planProgress struct {
	PlanID       int64          `json:"plan_id"`
	Cycle        int            `json:"cycle"`
	DecisionType string         `json:"decision_type"`
	Reason       string         `json:"reason,omitempty"`
	Need         string         `json:"need,omitempty"`
	StepCount    int            `json:"step_count"`
	Executed     int            `json:"executed"`
	Outcome      string         `json:"outcome,omitempty"`
	Steps        []stepProgress `json:"steps"`
}

type stepProgress struct {
	Idx        int    `json:"idx"`
	Name       string `json:"name,omitempty"`
	Capability string `json:"capability,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

// streamEvent is one conversation item from
// GET /api/tasks/{id}/agents/{id}/events (llm_messages: thinking / tool /
// assistant). MessageSeq is the cursor to poll after.
type streamEvent struct {
	MessageSeq int64  `json:"message_seq"`
	TurnID     int64  `json:"turn_id"`
	TurnSeq    int    `json:"turn_seq"`
	Cycle      int    `json:"cycle"`
	Role       string `json:"role"`
	Content    string `json:"content"`
	Status     string `json:"status,omitempty"`
}

type streamResponse struct {
	TaskID           string        `json:"task_id"`
	AgentID          int64         `json:"agent_id"`
	Events           []streamEvent `json:"events"`
	LastMessageSeq   int64         `json:"last_message_seq"`
	NextPollAfterSeq int64         `json:"next_poll_after_seq"`
}

// client is the runtime's HTTP API, one request at a time. The runtime's own
// words come back on refusal — the API answers every error as {"error": "…"}.
type client struct {
	base string
	http *http.Client
}

func newClient(base string) (*client, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil, fmt.Errorf("no server: set -server (or AUTONOMY_API_URL), e.g. http://127.0.0.1:4230")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid server %q: want http://host:port", base)
	}
	// http.Client{} keeps the default transport, so HTTP(S)_PROXY / NO_PROXY in
	// the environment are honoured the same way curl would.
	return &client{base: base, http: &http.Client{Timeout: 60 * time.Second}}, nil
}

func (c *client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *client) post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("%s %s: read: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, apiError(raw))
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, path, err)
	}
	return nil
}

// apiError is the runtime's own reason for refusing: the API answers errors as
// {"error": "…"}; anything else is passed through as it came.
func apiError(raw []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &e); err == nil && strings.TrimSpace(e.Error) != "" {
		return e.Error
	}
	if text := strings.TrimSpace(string(raw)); text != "" {
		return oneLine(text)
	}
	return "no body"
}

func progressPath(taskID string) string {
	return "/api/tasks/" + url.PathEscape(taskID)
}

func eventsPath(taskID string, agentID, after int64) string {
	return progressPath(taskID) + "/agents/" + strconv.FormatInt(agentID, 10) +
		"/events?last_synced_message_seq=" + strconv.FormatInt(after, 10)
}

func stopPath(taskID string) string {
	return progressPath(taskID) + "/stop"
}
