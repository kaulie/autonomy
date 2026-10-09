package autonomy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sd "github.com/kaulie/autonomy/src/capability/software_development"
	"github.com/kaulie/autonomy/src/eventgateway"
)

func TestPRWatcherEmitsMergedEvent(t *testing.T) {
	states := []map[string]string{{
		"exists": "true", "state": "open", "merged": "false",
		"pr":     "https://github.com/kaulie/agent-watchdog/pull/9",
		"number": "9", "repo": "kaulie/agent-watchdog", "title": "watch",
	}, {
		"exists": "true", "state": "closed", "merged": "true",
		"pr":     "https://github.com/kaulie/agent-watchdog/pull/9",
		"number": "9", "repo": "kaulie/agent-watchdog", "title": "watch",
	}}
	var n int
	w := newPRWatcher()
	t.Cleanup(w.Close)
	w.SetSnapshot(func(string) (map[string]string, error) {
		if n >= len(states) {
			return states[len(states)-1], nil
		}
		out := states[n]
		n++
		return out, nil
	})
	var ingested []IngestEventRequest
	w.SetIngest(func(_ context.Context, req IngestEventRequest) (*IngestEventResponse, error) {
		ingested = append(ingested, req)
		return &IngestEventResponse{Event: eventgateway.Event{ID: "evt-1", Type: req.Type}}, nil
	})

	spec := sd.PRWatchSpec{
		PR: "https://github.com/kaulie/agent-watchdog/pull/9", Repo: "kaulie/agent-watchdog",
		Number: 9, TaskID: "task-wd", Until: sd.WatchUntilMerged, State: "open", Merged: "false",
	}
	if err := w.WatchPullRequest(spec); err != nil {
		t.Fatal(err)
	}
	w.PollOnce()
	if len(ingested) != 0 {
		t.Fatalf("open snapshot must not re-emit: %+v", ingested)
	}
	w.PollOnce()
	if len(ingested) != 1 {
		t.Fatalf("ingested=%+v, want the merge", ingested)
	}
	ev := ingested[0]
	if ev.Type != typePullRequestMerged || ev.Source != sourceGitHub {
		t.Fatalf("event=%+v", ev)
	}
	if ev.Subject.TaskID != "task-wd" || ev.IdempotencyKey != "github:kaulie/agent-watchdog#9:merged" {
		t.Fatalf("subject/key=%+v", ev)
	}
	if got := w.List(); len(got) != 0 {
		t.Fatalf("watch should stop after merge: %+v", got)
	}
}

func TestPRWatchHTTPRegistersAndLists(t *testing.T) {
	var mu sync.Mutex
	merged := false
	git := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		state, flag := "open", "false"
		if merged {
			state, flag = "closed", "true"
		}
		_, _ = w.Write([]byte(`{"number":9,"html_url":"https://github.com/kaulie/agent-watchdog/pull/9","title":"watch","state":"` + state + `","draft":false,"merged":` + flag + `}`))
	}))
	t.Cleanup(git.Close)

	watcher := newPRWatcher()
	t.Cleanup(watcher.Close)
	check := sd.PRCheck{APIURL: git.URL, Token: "test-token"}
	watcher.SetSnapshot(func(pr string) (map[string]string, error) {
		return check.Run(map[string]string{"pr": pr})
	})
	auto := &Autonomy{PRWatcher: watcher, EventGateway: eventgateway.New()}
	watcher.SetIngest(auto.IngestWorldEvent)
	httpSrv := NewHTTPServer(auto)

	body := `{"pr":"https://github.com/kaulie/agent-watchdog/pull/9","task_id":"task-wd","until":"merged"}`
	rec := httptest.NewRecorder()
	httpSrv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/watches", strings.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", rec.Code, rec.Body.String())
	}
	var view PRWatchView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Watching || view.Number != 9 {
		t.Fatalf("view=%+v", view)
	}

	list := httptest.NewRecorder()
	httpSrv.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/watches", nil))
	var listed ListWatchesResponse
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Count != 1 {
		t.Fatalf("listed=%+v", listed)
	}

	mu.Lock()
	merged = true
	mu.Unlock()
	watcher.PollOnce()

	events, err := auto.ListWorldEvents(eventgateway.Filter{Type: typePullRequestMerged})
	if err != nil || events.Count != 1 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if events.Events[0].Subject.TaskID != "task-wd" {
		t.Fatalf("event=%+v", events.Events[0])
	}
}
