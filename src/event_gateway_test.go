package autonomy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaulie/autonomy/src/eventgateway"
)

func TestEventGatewayHTTPIngestAndList(t *testing.T) {
	auto := &Autonomy{EventGateway: eventgateway.New()}
	srv := NewHTTPServer(auto)

	body := `{"source":"deployment","type":"deployment.succeeded","subject":{"task_id":"task-1","asset_id":"svc:autonomy"},"payload":{"pipeline_id":"p-1"},"idempotency_key":"deployment:p-1:succeeded"}`
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/events", strings.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("ingest status=%d body=%s", rec.Code, rec.Body.String())
	}
	var accepted IngestEventResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.Duplicate || accepted.Event.ID == "" || accepted.Event.Source != "deployment" {
		t.Fatalf("accepted=%+v", accepted)
	}
	if accepted.Delivered {
		t.Fatalf("no agent for task-1, want delivered=false: %+v", accepted)
	}

	dup := httptest.NewRecorder()
	srv.Handler().ServeHTTP(dup, httptest.NewRequest(http.MethodPost, "/api/events", strings.NewReader(body)))
	if dup.Code != http.StatusOK {
		t.Fatalf("duplicate status=%d body=%s", dup.Code, dup.Body.String())
	}
	var again IngestEventResponse
	if err := json.Unmarshal(dup.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if !again.Duplicate || again.Event.ID != accepted.Event.ID {
		t.Fatalf("duplicate=%+v want id %s", again, accepted.Event.ID)
	}

	list := httptest.NewRecorder()
	srv.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/events?source=deployment", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var listed ListEventsResponse
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Count != 1 || listed.Events[0].ID != accepted.Event.ID {
		t.Fatalf("listed=%+v", listed)
	}
}

func TestEventGatewayHTTPRejectsInvalidAndOff(t *testing.T) {
	srv := NewHTTPServer(&Autonomy{EventGateway: eventgateway.New()})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/events", strings.NewReader(`{"type":"x"}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "source") {
		t.Fatalf("missing source: status=%d body=%s", rec.Code, rec.Body.String())
	}

	off := NewHTTPServer(&Autonomy{})
	rec = httptest.NewRecorder()
	off.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/events", strings.NewReader(`{"source":"github","type":"ping"}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("gateway off: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEventGatewayInjectsEventsIntoWorldPrompt(t *testing.T) {
	prevAuto, prevWorld := _autonomy, _world
	t.Cleanup(func() { _autonomy, _world = prevAuto, prevWorld })
	gw := eventgateway.New()
	_autonomy = &Autonomy{EventGateway: gw}
	_world = nil
	if _, _, err := gw.Ingest(context.Background(), eventgateway.Envelope{
		Source: "github", Type: "pull_request.opened",
		Subject: eventgateway.Subject{TaskID: "task-pr"},
		Payload: map[string]any{"url": "https://example/pr/1"},
	}); err != nil {
		t.Fatal(err)
	}

	raw := formatWorldJSON(DecisionContext{})
	var world map[string]any
	if err := json.Unmarshal(raw, &world); err != nil {
		t.Fatal(err)
	}
	if _, ok := world["assets"]; !ok {
		t.Fatalf("world missing assets: %s", raw)
	}
	events, _ := world["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("events=%v", world["events"])
	}
	ev := events[0].(map[string]any)
	if ev["source"] != "github" || ev["type"] != "pull_request.opened" {
		t.Fatalf("event=%v", ev)
	}
}

func TestEventGatewayWakesTaskAgent(t *testing.T) {
	store, err := openStore(t, filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	prevStore := _store
	_store = store
	t.Cleanup(func() { _store = prevStore })

	probe := &inboxProbe{started: make(chan string, 1)}
	factory := NewAgentFactory()
	auto := &Autonomy{
		AgentFactory: factory,
		Store:        store,
		EventGateway: eventgateway.New(),
		Inbox:        NewInbox(store, probe.handle, nil),
		World:        &World{events: []Event{}},
	}
	task := &Task{ID: "task-watch", Description: "watch deploy", Status: TaskStatusPending, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	agent := factory.Create(task)
	if err := store.UpsertTask(task); err != nil {
		t.Fatal(err)
	}

	resp, err := auto.IngestWorldEvent(context.Background(), IngestEventRequest{
		Source:         "deployment",
		Type:           "deployment.succeeded",
		Subject:        eventgateway.Subject{TaskID: task.ID},
		IdempotencyKey: "deployment:p-9:succeeded",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Delivered || resp.AgentID != agent.ID || resp.MessageID == 0 {
		t.Fatalf("want observation queued, got %+v", resp)
	}

	msg := waitForMessageStatus(t, store, agent.ID, resp.MessageID, MessageStatusDone)
	if msg.Kind != MessageKindObservation || msg.Sender != MessageSenderSystem {
		t.Fatalf("message=%+v", msg)
	}
	if !strings.Contains(msg.Content, `"type":"deployment.succeeded"`) {
		t.Fatalf("content=%s", msg.Content)
	}
	if got := probe.processed(); len(got) != 1 {
		t.Fatalf("probe=%v", got)
	}

	listed, err := auto.ListWorldEvents(eventgateway.Filter{TaskID: task.ID})
	if err != nil || listed.Count != 1 {
		t.Fatalf("list=%+v err=%v", listed, err)
	}
}

func TestEventGatewayDoesNotInventAnAgent(t *testing.T) {
	store, err := openStore(t, filepath.Join(t.TempDir(), "events-none.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	auto := &Autonomy{
		AgentFactory: NewAgentFactory(),
		Store:        store,
		EventGateway: eventgateway.New(),
	}
	resp, err := auto.IngestWorldEvent(context.Background(), IngestEventRequest{
		Source:  "github",
		Type:    "ping",
		Subject: eventgateway.Subject{TaskID: "task-nobody"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Delivered {
		t.Fatalf("unknown task must not be woken: %+v", resp)
	}
	agents, err := store.ListAgents()
	if err != nil {
		t.Fatal(err)
	}
	// seedTestAccounts does not create agents; an event must not either.
	for _, a := range agents {
		if a != nil {
			t.Fatalf("event invented agent %+v", a)
		}
	}
}
