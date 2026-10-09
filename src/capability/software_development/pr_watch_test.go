package software_development_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sd "github.com/kaulie/autonomy/src/capability/software_development"
)

type recordWatch struct {
	mu    sync.Mutex
	specs []sd.PRWatchSpec
}

func (r *recordWatch) WatchPullRequest(spec sd.PRWatchSpec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.specs = append(r.specs, spec)
	return nil
}

func (r *recordWatch) last() sd.PRWatchSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.specs) == 0 {
		return sd.PRWatchSpec{}
	}
	return r.specs[len(r.specs)-1]
}

func prJSON(merged bool) string {
	state := "open"
	if merged {
		state = "closed"
	}
	return `{"number":9,"html_url":"https://github.com/kaulie/agent-watchdog/pull/9","title":"watch","state":"` + state + `","draft":false,"merged":` + boolJSON(merged) + `}`
}

func boolJSON(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func TestPRWatchRegistersBackgroundUntilMerge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/pulls/9") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(prJSON(false)))
	}))
	t.Cleanup(srv.Close)

	reg := &recordWatch{}
	c := sd.PRWatch{APIURL: srv.URL, Token: "test-token", Watches: reg}
	out, err := c.Run(map[string]string{
		"pr":      "https://github.com/kaulie/agent-watchdog/pull/9",
		"task_id": "task-watchdog",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["merged"] != "false" || out["watching"] != "true" || out["terminal"] != "false" {
		t.Fatalf("out=%v, want an open PR with a background watch", out)
	}
	got := reg.last()
	if got.PR != "https://github.com/kaulie/agent-watchdog/pull/9" || got.TaskID != "task-watchdog" || got.Until != sd.WatchUntilMerged || got.Number != 9 {
		t.Fatalf("spec=%+v", got)
	}
}

func TestPRWatchAlreadyMergedDoesNotWatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(prJSON(true)))
	}))
	t.Cleanup(srv.Close)

	reg := &recordWatch{}
	c := sd.PRWatch{APIURL: srv.URL, Token: "test-token", Watches: reg}
	out, err := c.Run(map[string]string{"pr": "https://github.com/kaulie/agent-watchdog/pull/9"})
	if err != nil {
		t.Fatal(err)
	}
	if out["merged"] != "true" || out["watching"] != "false" || out["terminal"] != "true" {
		t.Fatalf("out=%v, want merged with no background watch", out)
	}
	if spec := reg.last(); spec.PR != "" {
		t.Fatalf("must not register a watch for an already-merged PR: %+v", spec)
	}
}

func TestPRWatchInCallWaitsUntilMerged(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(prJSON(n > 1)))
	}))
	t.Cleanup(srv.Close)

	c := sd.PRWatch{
		APIURL: srv.URL,
		Token:  "test-token",
		Sleep:  func(context.Context, time.Duration) error { return nil },
	}
	out, err := c.Run(map[string]string{
		"pr":       "https://github.com/kaulie/agent-watchdog/pull/9",
		"watch":    "true",
		"interval": "2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["merged"] != "true" || out["watching"] != "false" || n < 2 {
		t.Fatalf("out=%v polls=%d, want the in-call watch to see the merge", out, n)
	}
}
