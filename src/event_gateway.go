package autonomy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/eventgateway"
)

// Event Gateway is how a fact that happened outside this process becomes a
// World observation the agent can see (docs/event-gateway.md). The module
// itself (src/eventgateway) is independent: ingest, normalize, log. This file
// is the runtime wiring — bootstrap, prompt injection, optional inbox wake,
// HTTP DTOs.
//
// A restart forgets the in-process log (MemoryLog). Persistence is the same
// seam Context Service used: a Store-backed Log behind the module's port, not
// an eighth Store union port.

const (
	// EnvEventGateway switches the gateway off ("0", "off", "false", "no").
	EnvEventGateway = "AUTONOMY_EVENT_GATEWAY"
	// EnvEventGatewayPromptLimit is how many recent events the World block of
	// a decision-cycle prompt carries (default 20).
	EnvEventGatewayPromptLimit = "AUTONOMY_EVENT_GATEWAY_PROMPT_LIMIT"

	defaultEventPromptLimit = 20
	maxEventPromptLimit     = 100
)

// IngestEventRequest is POST /api/events: a canonical world-event envelope.
type IngestEventRequest struct {
	Source         string               `json:"source"`
	Type           string               `json:"type"`
	Subject        eventgateway.Subject `json:"subject"`
	Payload        map[string]any       `json:"payload"`
	OccurredAt     time.Time            `json:"occurred_at"`
	IdempotencyKey string               `json:"idempotency_key"`
}

// IngestEventResponse is what ingest answers: the stored event, whether it was
// a duplicate, and — when a subject.task_id named a live task with an agent —
// whether an observation was queued for that agent.
type IngestEventResponse struct {
	Event     eventgateway.Event `json:"event"`
	Duplicate bool               `json:"duplicate"`
	Delivered bool               `json:"delivered"`
	TaskID    string             `json:"task_id,omitempty"`
	AgentID   int64              `json:"agent_id,omitempty"`
	MessageID int64              `json:"message_id,omitempty"`
}

// ListEventsResponse is GET /api/events.
type ListEventsResponse struct {
	Events []eventgateway.Event `json:"events"`
	Count  int                  `json:"count"`
}

func eventGatewayEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvEventGateway))) {
	case "0", "off", "false", "no":
		return false
	}
	return true
}

func newEventGateway() *eventgateway.Gateway {
	if !eventGatewayEnabled() {
		return nil
	}
	return eventgateway.New()
}

func eventPromptLimit() int {
	raw := strings.TrimSpace(os.Getenv(EnvEventGatewayPromptLimit))
	if raw == "" {
		return defaultEventPromptLimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultEventPromptLimit
	}
	if n > maxEventPromptLimit {
		return maxEventPromptLimit
	}
	return n
}

// IngestWorldEvent is the runtime door onto the gateway: the envelope is
// stored, copied onto World so the next prompt's World block can show it, and
// — when subject.task_id names a task that already has an agent — an
// observation is queued so that agent re-observes without waiting for the next
// user instruction. A duplicate does not re-notify. The gateway being off is
// an error, not a silent drop: a caller posting events must know they were not
// taken.
func (r *Autonomy) IngestWorldEvent(ctx context.Context, req IngestEventRequest) (*IngestEventResponse, error) {
	if r == nil || r.EventGateway == nil {
		return nil, fmt.Errorf("event gateway is off")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ev, result, err := r.EventGateway.Ingest(ctx, eventgateway.Envelope{
		Source:         req.Source,
		Type:           req.Type,
		Subject:        req.Subject,
		Payload:        req.Payload,
		OccurredAt:     req.OccurredAt,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		return nil, err
	}
	resp := &IngestEventResponse{Event: ev, Duplicate: result.Duplicate}
	if result.Duplicate {
		return resp, nil
	}
	r.recordWorldEvent(ev)
	if delivered := r.notifyWorldEvent(ev); delivered != nil {
		resp.Delivered = true
		resp.TaskID = delivered.taskID
		resp.AgentID = delivered.agentID
		resp.MessageID = delivered.messageID
	}
	return resp, nil
}

// ListWorldEvents is GET /api/events.
func (r *Autonomy) ListWorldEvents(f eventgateway.Filter) (*ListEventsResponse, error) {
	if r == nil || r.EventGateway == nil {
		return &ListEventsResponse{Events: []eventgateway.Event{}}, nil
	}
	events, err := r.EventGateway.List(f)
	if err != nil {
		return nil, err
	}
	if events == nil {
		events = []eventgateway.Event{}
	}
	return &ListEventsResponse{Events: events, Count: len(events)}, nil
}

func (r *Autonomy) recordWorldEvent(ev eventgateway.Event) {
	world := r.World
	if world == nil {
		world = _world
	}
	if world == nil {
		return
	}
	world.RecordEvent(Event{
		ID:        ev.ID,
		Timestamp: ev.OccurredAt,
		Type:      ev.Type,
		Message:   ev.Source + " " + ev.Type,
		Data: map[string]interface{}{
			"source":          ev.Source,
			"subject":         ev.Subject,
			"payload":         ev.Payload,
			"idempotency_key": ev.IdempotencyKey,
		},
	})
}

type worldEventDelivery struct {
	taskID    string
	agentID   int64
	messageID int64
}

// notifyWorldEvent wakes the task's own agent when the event names a task_id.
// It does not create a task or an agent: an event for a world nobody is working
// in stays in the log for the next cycle that reads World. Duplicate suppression
// is the caller's (IngestWorldEvent skips this on duplicate).
func (r *Autonomy) notifyWorldEvent(ev eventgateway.Event) *worldEventDelivery {
	taskID := strings.TrimSpace(ev.Subject.TaskID)
	if taskID == "" || r == nil {
		return nil
	}
	task := r.taskForMessage(AgentMessage{TaskID: taskID})
	if task == nil || strings.TrimSpace(task.ID) == "" {
		return nil
	}
	// A task that has never had an agent is not woken: creating one here would
	// make an event invent a planner. The fact still sits in World.
	if task.AgentID == 0 && (r.AgentFactory == nil || r.AgentFactory.ForTask(task.ID) == nil) {
		return nil
	}
	agent, err := r.resumeAgentForTask(task)
	if err != nil || agent == nil || agent.ID == 0 {
		fmt.Fprintf(os.Stderr, "[autonomy] event-gateway: wake task %s: %v\n", taskID, err)
		return nil
	}
	body, err := json.Marshal(ev)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] event-gateway: marshal %s: %v\n", ev.ID, err)
		return nil
	}
	id, err := r.agentInbox().Enqueue(agent, AgentMessage{
		TaskID:   task.ID,
		Sender:   MessageSenderSystem,
		SenderID: "event-gateway:" + ev.Source,
		Kind:     MessageKindObservation,
		Content:  string(body),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] event-gateway: enqueue task %s: %v\n", taskID, err)
		return nil
	}
	return &worldEventDelivery{taskID: task.ID, agentID: agent.ID, messageID: id}
}

// recentWorldEventsJSON is the `events` array of the prompt's World block:
// recent observations, oldest of the window first. An empty array when the
// gateway is off or has seen nothing — the shape stays stable.
func recentWorldEventsJSON() []map[string]any {
	limit := eventPromptLimit()
	if _autonomy != nil && _autonomy.EventGateway != nil {
		events, err := _autonomy.EventGateway.Recent(limit)
		if err == nil {
			return worldEventViews(events)
		}
	}
	if _world != nil {
		return loopEventViews(_world.RecentEvents(limit))
	}
	return []map[string]any{}
}

func worldEventViews(events []eventgateway.Event) []map[string]any {
	out := make([]map[string]any, 0, len(events))
	for _, ev := range events {
		item := map[string]any{
			"id":          ev.ID,
			"source":      ev.Source,
			"type":        ev.Type,
			"occurred_at": ev.OccurredAt.UTC().Format(time.RFC3339Nano),
		}
		if !ev.Subject.Empty() {
			item["subject"] = ev.Subject
		}
		if len(ev.Payload) > 0 {
			item["payload"] = ev.Payload
		}
		out = append(out, item)
	}
	return out
}

func loopEventViews(events []Event) []map[string]any {
	out := make([]map[string]any, 0, len(events))
	for _, ev := range events {
		item := map[string]any{
			"id":   ev.ID,
			"type": ev.Type,
		}
		if ev.Message != "" {
			item["message"] = ev.Message
		}
		if !ev.Timestamp.IsZero() {
			item["occurred_at"] = ev.Timestamp.UTC().Format(time.RFC3339Nano)
		}
		if len(ev.Data) > 0 {
			item["payload"] = ev.Data
		}
		out = append(out, item)
	}
	return out
}
