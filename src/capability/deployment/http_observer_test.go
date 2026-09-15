package deployment_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability/deployment"
)

// recordingServer serves the deployment API routes a test needs and remembers
// which paths were asked for.
type recordingServer struct {
	*httptest.Server
	handler func(w http.ResponseWriter, r *http.Request)
	paths   []string
}

func newRecordingServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *recordingServer {
	t.Helper()
	rs := &recordingServer{handler: handler}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.paths = append(rs.paths, r.URL.Path)
		rs.handler(w, r)
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *recordingServer) observer() *deployment.HTTPObserver {
	// The server's own client keeps the tests off any ambient HTTP proxy.
	return &deployment.HTTPObserver{Client: rs.Client()}
}

func (rs *recordingServer) asked(path string) bool {
	for _, p := range rs.paths {
		if p == path {
			return true
		}
	}
	return false
}

const statusJSON = `{"id":"deployment-abc","state":"running","phase":"rollout","progress":"2/5","healthy":true,"updated_at":"2026-09-15T10:00:00Z","logs":["step 1: ok","step 2: pushing"]}`

// TestHTTPObserverReadsStatusAndInlineLogs: one status call carries the state
// and, when the source includes them, the logs.
func TestHTTPObserverReadsStatusAndInlineLogs(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, statusJSON)
	})
	snap, err := srv.observer().Observe(context.Background(), deployment.Request{
		Deployment: "deployment-abc", Endpoint: srv.URL, Tail: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !srv.asked("/deployments/deployment-abc") {
		t.Fatalf("paths=%v", srv.paths)
	}
	if srv.asked("/deployments/deployment-abc/logs") {
		t.Fatalf("fetched logs even though the status carried them: %v", srv.paths)
	}
	if snap.ID != "deployment-abc" || snap.State != deployment.StateRunning || snap.Phase != "rollout" {
		t.Fatalf("snap=%+v", snap)
	}
	if snap.Progress != "2/5" || snap.Healthy == nil || !*snap.Healthy {
		t.Fatalf("snap=%+v", snap)
	}
	if snap.UpdatedAt.IsZero() {
		t.Fatalf("updated_at not parsed: %+v", snap)
	}
	if len(snap.Logs) != 2 || snap.Logs[1] != "step 2: pushing" {
		t.Fatalf("logs=%v", snap.Logs)
	}
}

// TestHTTPObserverNormalizesStateVocabulary: the monitor speaks one state
// vocabulary no matter which words the deployment system uses.
func TestHTTPObserverNormalizesStateVocabulary(t *testing.T) {
	cases := map[string]deployment.State{
		"succeeded":   deployment.StateSucceeded,
		"SUCCESS":     deployment.StateSucceeded,
		"failed":      deployment.StateFailed,
		"Timed Out":   deployment.StateFailed,
		"in-progress": deployment.StateRunning,
		"queued":      deployment.StatePending,
		"wat":         deployment.StateUnknown,
	}
	for raw, want := range cases {
		got := normalizeViaServer(t, raw)
		if got != want {
			t.Fatalf("state %q -> %q, want %q", raw, got, want)
		}
	}
}

func normalizeViaServer(t *testing.T, raw string) deployment.State {
	t.Helper()
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"`+raw+`"}`)
	})
	snap, err := srv.observer().Observe(context.Background(), deployment.Request{
		Deployment: "dep-1", Endpoint: srv.URL, Tail: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap.State
}

// TestHTTPObserverFetchesSeparateLogsEndpoint: when the status carries no logs
// the monitor asks the logs endpoint for them.
func TestHTTPObserverFetchesSeparateLogsEndpoint(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/logs") {
			_, _ = io.WriteString(w, `{"lines":["step 1: ok","container terminated: OOMKilled"]}`)
			return
		}
		_, _ = io.WriteString(w, `{"state":"failed"}`)
	})
	snap, err := srv.observer().Observe(context.Background(), deployment.Request{
		Deployment: "dep-2", Endpoint: srv.URL, Tail: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !srv.asked("/deployments/dep-2/logs") {
		t.Fatalf("paths=%v", srv.paths)
	}
	if len(snap.Logs) != 2 || !strings.Contains(strings.Join(snap.Logs, "\n"), "OOMKilled") {
		t.Fatalf("logs=%v", snap.Logs)
	}
}

// TestHTTPObserverAcceptsPlainTextLogs: not every deployment service speaks JSON.
func TestHTTPObserverAcceptsPlainTextLogs(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/logs") {
			_, _ = io.WriteString(w, "line one\nline two\n")
			return
		}
		_, _ = io.WriteString(w, `{"state":"running"}`)
	})
	snap, err := srv.observer().Observe(context.Background(), deployment.Request{
		Deployment: "dep-3", Endpoint: srv.URL, Tail: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Logs) != 2 || snap.Logs[0] != "line one" || snap.Logs[1] != "line two" {
		t.Fatalf("logs=%v", snap.Logs)
	}
}

// TestHTTPObserverReportsHTTPErrors: a failing status call travels as an error,
// with the status code and a body snippet — which is often the diagnosis.
func TestHTTPObserverReportsHTTPErrors(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "deployment service is down\nsecond line\n")
	})
	_, err := srv.observer().Observe(context.Background(), deployment.Request{
		Deployment: "dep-4", Endpoint: srv.URL, Tail: 5,
	})
	if err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "deployment service is down") {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(err.Error(), "second line") {
		t.Fatalf("error kept more than a snippet: %v", err)
	}
}

// TestHTTPObserverRejectsUnreadableStatus: a body that is neither the expected
// JSON nor anything else usable is reported, not guessed at.
func TestHTTPObserverRejectsUnreadableStatus(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "not json at all")
	})
	_, err := srv.observer().Observe(context.Background(), deployment.Request{
		Deployment: "dep-5", Endpoint: srv.URL, Tail: 5,
	})
	if err == nil || !strings.Contains(err.Error(), "decode status") {
		t.Fatalf("err=%v", err)
	}
}

// TestHTTPObserverTailKeepsTheEnd: evidence is the end of the log, where a
// failure lands.
func TestHTTPObserverTailKeepsTheEnd(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/logs") {
			_, _ = io.WriteString(w, `{"lines":["l1","l2","l3","l4","l5"]}`)
			return
		}
		_, _ = io.WriteString(w, `{"state":"running"}`)
	})
	snap, err := srv.observer().Observe(context.Background(), deployment.Request{
		Deployment: "dep-6", Endpoint: srv.URL, Tail: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Logs) != 2 || snap.Logs[0] != "l4" || snap.Logs[1] != "l5" {
		t.Fatalf("logs=%v", snap.Logs)
	}
}

// TestHTTPObserverStatusURLEscapesTheDeploymentID and honours status_url.
func TestHTTPObserverUsesExplicitStatusURL(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"state":"succeeded"}`)
	})
	snap, err := srv.observer().Observe(context.Background(), deployment.Request{
		Deployment: "dep-7", StatusURL: srv.URL + "/runs/7", Tail: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != deployment.StateSucceeded || !srv.asked("/runs/7") {
		t.Fatalf("state=%q paths=%v", snap.State, srv.paths)
	}
	if snap.ID != "dep-7" {
		t.Fatalf("id=%q, want it to fall back to the request's deployment", snap.ID)
	}
}

// TestMonitorOverHTTPAgainstAFakeDeploymentService is the end-to-end shape: the
// capability with no injected observer builds its own HTTP one, follows the run
// the pipeline started, and comes back with the failure and the reason.
func TestMonitorOverHTTPAgainstAFakeDeploymentService(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/logs") {
			_, _ = io.WriteString(w, `{"lines":["apply: ok","waiting for rollout","error: ImagePullBackOff (registry/app:v9)"]}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"deployment-9f2","status":"failed","phase":"rollout","updated_at":"2026-09-15T10:00:00Z"}`)
	})
	out, err := (deployment.Monitor{}).Run(map[string]string{
		"deployment": "deployment-9f2", "endpoint": srv.URL, "tail": "10",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["state"] != "failed" || out["problem"] != "true" {
		t.Fatalf("state/problem = %q/%q", out["state"], out["problem"])
	}
	if !strings.Contains(out["signals"], "image_pull") {
		t.Fatalf("signals=%q", out["signals"])
	}
	if !strings.Contains(out["evidence"], "ImagePullBackOff") {
		t.Fatalf("evidence=%q", out["evidence"])
	}
	if !strings.Contains(out["suggestions"], "registry credentials") {
		t.Fatalf("suggestions=%q", out["suggestions"])
	}
	if !srv.asked("/deployments/deployment-9f2") || !srv.asked("/deployments/deployment-9f2/logs") {
		t.Fatalf("paths=%v", srv.paths)
	}
}
