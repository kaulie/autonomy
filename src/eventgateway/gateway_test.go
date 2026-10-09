package eventgateway

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestIngestAssignsIDAndDedupe(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	n := 0
	gw := New(
		WithClock(func() time.Time { return now }),
		WithIDGenerator(func() string { n++; return "evt-test-1" }),
	)
	first, res, err := gw.Ingest(context.Background(), Envelope{
		Source: "deployment", Type: "deployment.succeeded",
		Subject:        Subject{TaskID: "task-1", AssetID: "svc:autonomy"},
		Payload:        map[string]any{"pipeline_id": "p-1"},
		IdempotencyKey: "deployment:p-1:succeeded",
	})
	if err != nil || res.Duplicate || first.ID != "evt-test-1" {
		t.Fatalf("first=%+v res=%+v err=%v", first, res, err)
	}
	if first.Source != "deployment" || first.Type != "deployment.succeeded" || first.Subject.TaskID != "task-1" {
		t.Fatalf("event=%+v", first)
	}
	if !first.OccurredAt.Equal(now) || !first.ReceivedAt.Equal(now) {
		t.Fatalf("timestamps=%s %s", first.OccurredAt, first.ReceivedAt)
	}

	second, res, err := gw.Ingest(context.Background(), Envelope{
		Source: "deployment", Type: "deployment.succeeded",
		IdempotencyKey: "deployment:p-1:succeeded",
		Payload:        map[string]any{"pipeline_id": "ignored"},
	})
	if err != nil || !res.Duplicate {
		t.Fatalf("dup res=%+v err=%v", res, err)
	}
	if second.ID != first.ID || second.Payload["pipeline_id"] != "p-1" {
		t.Fatalf("duplicate must return the original, got %+v", second)
	}
}

func TestIngestRequiresSourceAndType(t *testing.T) {
	gw := New()
	_, _, err := gw.Ingest(context.Background(), Envelope{Type: "x"})
	if err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("want source required, got %v", err)
	}
	_, _, err = gw.Ingest(context.Background(), Envelope{Source: "github"})
	if err == nil || !strings.Contains(err.Error(), "type") {
		t.Fatalf("want type required, got %v", err)
	}
}

func TestListFiltersAndRecent(t *testing.T) {
	gw := New(WithIDGenerator(seqID()))
	mustIngest(t, gw, Envelope{Source: "github", Type: "pull_request.opened", Subject: Subject{TaskID: "t1"}})
	mustIngest(t, gw, Envelope{Source: "github", Type: "pull_request.merged", Subject: Subject{TaskID: "t1"}})
	mustIngest(t, gw, Envelope{Source: "deployment", Type: "deployment.succeeded", Subject: Subject{TaskID: "t2"}})

	got, err := gw.List(Filter{Source: "github", TaskID: "t1"})
	if err != nil || len(got) != 2 {
		t.Fatalf("github/t1=%v err=%v", got, err)
	}
	if got[0].Type != "pull_request.opened" || got[1].Type != "pull_request.merged" {
		t.Fatalf("order=%v", got)
	}

	after, err := gw.List(Filter{After: got[0].ID, Limit: 10})
	if err != nil || len(after) != 2 {
		t.Fatalf("after first=%v err=%v", after, err)
	}

	recent, err := gw.Recent(2)
	if err != nil || len(recent) != 2 || recent[0].Type != "pull_request.merged" || recent[1].Source != "deployment" {
		t.Fatalf("recent=%v err=%v", recent, err)
	}
}

func TestEmptyIdempotencyKeyDoesNotCollide(t *testing.T) {
	gw := New(WithIDGenerator(seqID()))
	a, _, err := gw.Ingest(context.Background(), Envelope{Source: "webhook", Type: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	b, res, err := gw.Ingest(context.Background(), Envelope{Source: "webhook", Type: "ping"})
	if err != nil || res.Duplicate || a.ID == b.ID {
		t.Fatalf("a=%s b=%s dup=%v err=%v", a.ID, b.ID, res.Duplicate, err)
	}
}

func mustIngest(t *testing.T, gw *Gateway, env Envelope) Event {
	t.Helper()
	ev, res, err := gw.Ingest(context.Background(), env)
	if err != nil || res.Duplicate {
		t.Fatalf("ingest %+v: dup=%v err=%v", env, res.Duplicate, err)
	}
	return ev
}

func seqID() func() string {
	n := 0
	return func() string {
		n++
		return "evt-" + itoa(n)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
