package autonomy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaulie/autonomy/src/eventcenter"
	"github.com/kaulie/autonomy/src/eventgateway"
	"github.com/kaulie/autonomy/src/watcher"
)

func TestEventCenterFeedIngestsMergedPRAndBindsWatchTask(t *testing.T) {
	center := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/streams":
			_, _ = w.Write([]byte(`{"streams":{"github":1},"global_seq":1}`))
		default:
			_, _ = w.Write([]byte(`{"events":[` + mergedPREventBody() + `],"next_cursor":1}`))
		}
	}))
	t.Cleanup(center.Close)

	w := watcher.New([]watcher.Probe{watcher.PullRequestProbe{
		Snapshot: func(string) (watcher.Observation, error) {
			return watcher.Observation{
				"exists": "true", "state": "open", "merged": "false",
				"pr":   "https://github.com/kaulie/agent-watchdog/pull/9",
				"repo": "kaulie/agent-watchdog", "number": "9",
			}, nil
		},
	}}, watcher.WithErrors(func(string, ...any) {}))
	t.Cleanup(w.Close)
	if err := w.Watch(watcher.Spec{
		Kind: watcher.KindPullRequest, Target: "https://github.com/kaulie/agent-watchdog/pull/9",
		TaskID: "task-wd", Until: watcher.UntilMerged,
	}); err != nil {
		t.Fatal(err)
	}

	ec := &eventcenter.Client{URL: center.URL, HTTP: center.Client()}
	auto := &Autonomy{Watcher: w, EventGateway: eventgateway.New(), EventCenter: ec}
	auto.eventCenter = newEventCenterFeed(ec)
	auto.eventCenter.errf = func(string, ...any) {}

	n, err := auto.eventCenter.consume(auto, context.Background(), eventcenter.StreamGitHub, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("ingested=%d", n)
	}
	events, err := auto.ListWorldEvents(eventgateway.Filter{Type: "github.pull_request.merged"})
	if err != nil || events.Count != 1 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if events.Events[0].Subject.TaskID != "task-wd" {
		t.Fatalf("event=%+v, want the watch's task_id", events.Events[0])
	}
	if events.Events[0].Source != "github" {
		t.Fatalf("source=%q, want github (the producer), not event-center", events.Events[0].Source)
	}
}

func TestEventCenterFeedTimesOutWaitWithoutBusyLoop(t *testing.T) {
	center := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/streams" {
			_, _ = w.Write([]byte(`{"streams":{"github":0},"global_seq":0}`))
			return
		}
		_, _ = w.Write([]byte(`{"events":[],"next_cursor":0}`))
	}))
	t.Cleanup(center.Close)

	ec := &eventcenter.Client{URL: center.URL, HTTP: center.Client()}
	auto := &Autonomy{EventGateway: eventgateway.New(), EventCenter: ec}
	auto.eventCenter = newEventCenterFeed(ec)
	auto.eventCenter.errf = func(string, ...any) {}

	start := time.Now()
	n, err := auto.eventCenter.consume(auto, context.Background(), eventcenter.StreamGitHub, 0)
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("empty pull with wait=0 should return immediately")
	}
}

func openPREventBody() string {
	return eventCenterPREventJSON("github.pull_request.opened", false)
}

func mergedPREventBody() string {
	return eventCenterPREventJSON("github.pull_request.closed", true)
}

func eventCenterPREventJSON(typ string, merged bool) string {
	state, flag := "open", "false"
	if merged {
		state, flag = "closed", "true"
	}
	data := `{"action":"` + map[bool]string{false: "opened", true: "closed"}[merged] + `","number":9,"pull_request":{"number":9,"html_url":"https://github.com/kaulie/agent-watchdog/pull/9","title":"watch","state":"` + state + `","draft":false,"merged":` + flag + `},"repository":{"full_name":"kaulie/agent-watchdog"}}`
	ev := eventcenter.Event{
		ID:        "evt-pr-9",
		StreamSeq: 1,
		Stream:    "github",
		Provider:  "github",
		Type:      typ,
		Subject:   "repo:kaulie/agent-watchdog",
		Data:      json.RawMessage(data),
	}
	b, _ := json.Marshal(ev)
	return string(b)
}
