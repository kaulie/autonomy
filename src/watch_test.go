package autonomy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sd "github.com/kaulie/autonomy/src/capability/software_development"
	"github.com/kaulie/autonomy/src/eventgateway"
	"github.com/kaulie/autonomy/src/watcher"
)

func TestWatcherEmitsMergedEvent(t *testing.T) {
	states := []watcher.Observation{{
		"exists": "true", "state": "open", "merged": "false",
		"pr":     "https://github.com/kaulie/agent-watchdog/pull/9",
		"number": "9", "repo": "kaulie/agent-watchdog", "title": "watch",
	}, {
		"exists": "true", "state": "closed", "merged": "true",
		"pr":     "https://github.com/kaulie/agent-watchdog/pull/9",
		"number": "9", "repo": "kaulie/agent-watchdog", "title": "watch",
	}}
	var n int
	var ingested []IngestEventRequest
	w := watcher.New([]watcher.Probe{watcher.PullRequestProbe{
		Snapshot: func(string) (watcher.Observation, error) {
			if n >= len(states) {
				return states[len(states)-1], nil
			}
			out := states[n]
			n++
			return out, nil
		},
	}}, watcher.WithErrors(func(string, ...any) {}), watcher.WithSink(func(ch watcher.Change) error {
		ingested = append(ingested, IngestEventRequest{
			Source: ch.Source, Type: ch.Type,
			Subject:        eventgateway.Subject{TaskID: ch.TaskID, AssetID: ch.AssetID},
			IdempotencyKey: ch.IdempotencyKey,
		})
		return nil
	}))
	t.Cleanup(w.Close)

	spec := watcher.Spec{
		Kind: watcher.KindPullRequest, Target: "https://github.com/kaulie/agent-watchdog/pull/9",
		TaskID: "task-wd", Until: watcher.UntilMerged,
		Fields: states[0],
	}
	if err := w.Watch(spec); err != nil {
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
	if ev.Type != "github.pull_request.merged" || ev.Source != "github" {
		t.Fatalf("event=%+v", ev)
	}
	if ev.Subject.TaskID != "task-wd" || ev.IdempotencyKey != "github:kaulie/agent-watchdog#9:merged" {
		t.Fatalf("subject/key=%+v", ev)
	}
	if got := w.List(); len(got) != 0 {
		t.Fatalf("watch should stop after merge: %+v", got)
	}
}

func TestWatchHTTPRegistersAndLists(t *testing.T) {
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

	check := sd.PRCheck{APIURL: git.URL, Token: "test-token"}
	w := watcher.New([]watcher.Probe{watcher.PullRequestProbe{
		Snapshot: func(pr string) (watcher.Observation, error) {
			out, err := check.Run(map[string]string{"pr": pr})
			return watcher.Observation(out), err
		},
	}}, watcher.WithErrors(func(string, ...any) {}))
	t.Cleanup(w.Close)

	auto := &Autonomy{Watcher: w, EventGateway: eventgateway.New()}
	w.SetSink(auto.watchSink)
	httpSrv := NewHTTPServer(auto)

	body := `{"pr":"https://github.com/kaulie/agent-watchdog/pull/9","task_id":"task-wd","until":"merged"}`
	rec := httptest.NewRecorder()
	httpSrv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/watches", strings.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", rec.Code, rec.Body.String())
	}
	var view WatchView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Watching || view.Kind != watcher.KindPullRequest {
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
	w.PollOnce()

	events, err := auto.ListWorldEvents(eventgateway.Filter{Type: "github.pull_request.merged"})
	if err != nil || events.Count != 1 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if events.Events[0].Subject.TaskID != "task-wd" {
		t.Fatalf("event=%+v", events.Events[0])
	}
}

func TestWatchHTTPDeploymentKind(t *testing.T) {
	w := watcher.New([]watcher.Probe{watcher.DeploymentProbe{
		Snapshot: func(string) (watcher.Observation, error) {
			return watcher.Observation{"exists": "true", "state": "running", "deployment": "pipeline-1"}, nil
		},
	}}, watcher.WithErrors(func(string, ...any) {}))
	t.Cleanup(w.Close)
	auto := &Autonomy{Watcher: w}
	httpSrv := NewHTTPServer(auto)

	body := `{"kind":"deployment","target":"pipeline-1","task_id":"task-dep"}`
	rec := httptest.NewRecorder()
	httpSrv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/watches", strings.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var view WatchView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Kind != watcher.KindDeployment || !view.Watching || view.Until != watcher.UntilSucceeded {
		t.Fatalf("view=%+v", view)
	}
}
