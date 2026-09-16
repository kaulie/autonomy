package deployment_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaulie/autonomy/src/capability/deployment"
)

// fakeObserver is a scripted deployment source: it answers with whatever fn
// returns and counts how many times it was polled.
type fakeObserver struct {
	fn    func(req deployment.Request) (deployment.Snapshot, error)
	calls int
	last  deployment.Request
}

func (f *fakeObserver) Observe(_ context.Context, req deployment.Request) (deployment.Snapshot, error) {
	f.calls++
	f.last = req
	return f.fn(req)
}

func observeOnce(snap deployment.Snapshot) *fakeObserver {
	return &fakeObserver{fn: func(deployment.Request) (deployment.Snapshot, error) { return snap, nil }}
}

// TestMonitorReportsFailedDeploymentWithEvidence pins the point of the
// capability: a failed rollout comes back as an observation (not an error),
// with the signals that explain it and the log lines behind them.
func TestMonitorReportsFailedDeploymentWithEvidence(t *testing.T) {
	obs := observeOnce(deployment.Snapshot{
		ID:       "deployment-abc",
		State:    deployment.StateFailed,
		Phase:    "rollout",
		Progress: "2/3",
		Error:    "rollout failed",
		Logs: []string{
			"step 1: build ok",
			"step 2: pushing image",
			"step 3: container terminated: OOMKilled (limit 512Mi)",
		},
	})
	m := deployment.Monitor{Observer: obs}

	out, err := m.Run(map[string]string{"deployment": "deployment-abc", "endpoint": "http://deploy.internal"})
	if err != nil {
		t.Fatal(err)
	}
	if out["state"] != "failed" || out["terminal"] != "true" {
		t.Fatalf("state/terminal = %q/%q", out["state"], out["terminal"])
	}
	if out["problem"] != "true" {
		t.Fatalf("problem=%q, want a failed deployment to be a problem", out["problem"])
	}
	if !strings.Contains(out["signals"], "deployment_failed") || !strings.Contains(out["signals"], "oom") {
		t.Fatalf("signals=%q", out["signals"])
	}
	if !strings.Contains(out["evidence"], "OOMKilled") {
		t.Fatalf("evidence=%q", out["evidence"])
	}
	if !strings.Contains(out["diagnosis"], "failed") || !strings.Contains(out["diagnosis"], "rollout") {
		t.Fatalf("diagnosis=%q", out["diagnosis"])
	}
	if !strings.Contains(out["suggestions"], "memory") {
		t.Fatalf("suggestions=%q", out["suggestions"])
	}
	if out["phase"] != "rollout" || out["progress"] != "2/3" {
		t.Fatalf("phase/progress = %q/%q", out["phase"], out["progress"])
	}
	if out["provider"] != deployment.Provider || out["polls"] != "1" {
		t.Fatalf("provider/polls = %q/%q", out["provider"], out["polls"])
	}
	if obs.last.Deployment != "deployment-abc" || obs.last.Endpoint != "http://deploy.internal" {
		t.Fatalf("observer request=%+v", obs.last)
	}
}

// TestMonitorRunningDeploymentIsNotAProblem: being in flight is not a failure.
func TestMonitorRunningDeploymentIsNotAProblem(t *testing.T) {
	healthy := true
	obs := observeOnce(deployment.Snapshot{
		ID: "d1", State: deployment.StateRunning, Progress: "1/4",
		Healthy: &healthy, UpdatedAt: time.Now(), Logs: []string{"step 1: ok"},
	})
	out, err := (deployment.Monitor{Observer: obs}).Run(map[string]string{
		"deployment": "d1", "endpoint": "http://deploy.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["problem"] != "false" || out["terminal"] != "false" {
		t.Fatalf("problem/terminal = %q/%q", out["problem"], out["terminal"])
	}
	if out["healthy"] != "true" {
		t.Fatalf("healthy=%q", out["healthy"])
	}
	if !strings.Contains(out["suggestions"], "still in progress") {
		t.Fatalf("suggestions=%q", out["suggestions"])
	}
	if _, ok := out["signals"]; ok {
		t.Fatalf("running deployment reported signals: %q", out["signals"])
	}
}

// TestMonitorUnhealthyDeploymentIsAProblem: a deployment that is technically
// still running but reports itself unhealthy is exactly what monitoring is for.
func TestMonitorUnhealthyDeploymentIsAProblem(t *testing.T) {
	unhealthy := false
	obs := observeOnce(deployment.Snapshot{
		ID: "d2", State: deployment.StateRunning, Healthy: &unhealthy,
		UpdatedAt: time.Now(), Logs: []string{"readiness probe failed: 503"},
	})
	out, err := (deployment.Monitor{Observer: obs}).Run(map[string]string{
		"deployment": "d2", "endpoint": "http://deploy.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["problem"] != "true" || !strings.Contains(out["signals"], "unhealthy") {
		t.Fatalf("problem/signals = %q/%q", out["problem"], out["signals"])
	}
	if out["healthy"] != "false" {
		t.Fatalf("healthy=%q", out["healthy"])
	}
}

// TestMonitorStalledDeploymentIsAProblem: silence is a signal too — a rollout
// that stops reporting progress gets called out before the pipeline times out.
func TestMonitorStalledDeploymentIsAProblem(t *testing.T) {
	obs := observeOnce(deployment.Snapshot{
		ID: "d3", State: deployment.StateRunning,
		UpdatedAt: time.Now().Add(-30 * time.Minute),
		Logs:      []string{"waiting for rollout to finish"},
	})
	out, err := (deployment.Monitor{Observer: obs}).Run(map[string]string{
		"deployment": "d3", "endpoint": "http://deploy.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["problem"] != "true" || !strings.Contains(out["signals"], "stalled") {
		t.Fatalf("problem/signals = %q/%q", out["problem"], out["signals"])
	}
}

// TestMonitorDefaultObservesOnce: without watch the capability answers from one
// observation, because the decision loop is what re-runs it.
func TestMonitorDefaultObservesOnce(t *testing.T) {
	obs := observeOnce(deployment.Snapshot{ID: "d4", State: deployment.StateRunning})
	out, err := (deployment.Monitor{Observer: obs}).Run(map[string]string{
		"deployment": "d4", "endpoint": "http://deploy.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if obs.calls != 1 || out["polls"] != "1" {
		t.Fatalf("calls/polls = %d/%q, want a single observation", obs.calls, out["polls"])
	}
}

// TestMonitorWatchPollsUntilTerminal: watching is an opt-in, bounded way to
// follow a rollout inside one capability call.
func TestMonitorWatchPollsUntilTerminal(t *testing.T) {
	obs := &fakeObserver{}
	obs.fn = func(deployment.Request) (deployment.Snapshot, error) {
		if obs.calls < 3 {
			return deployment.Snapshot{ID: "d5", State: deployment.StateRunning, UpdatedAt: time.Now()}, nil
		}
		return deployment.Snapshot{ID: "d5", State: deployment.StateSucceeded, UpdatedAt: time.Now()}, nil
	}
	m := deployment.Monitor{Observer: obs, Sleep: func(context.Context, time.Duration) error { return nil }}

	out, err := m.Run(map[string]string{
		"deployment": "d5", "endpoint": "http://deploy.internal",
		"watch": "true", "interval": "1", "timeout": "30",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["state"] != "succeeded" || out["polls"] != "3" {
		t.Fatalf("state/polls = %q/%q, want the third observation", out["state"], out["polls"])
	}
	if out["problem"] != "false" {
		t.Fatalf("problem=%q, a clean rollout is not a problem", out["problem"])
	}
	if !strings.Contains(out["suggestions"], "no failure signal") {
		t.Fatalf("suggestions=%q", out["suggestions"])
	}
}

// TestMonitorWatchWindowIsBounded: a deployment that never reaches a terminal
// state still returns within the window, with what was last seen.
func TestMonitorWatchWindowIsBounded(t *testing.T) {
	obs := observeOnce(deployment.Snapshot{ID: "d6", State: deployment.StateRunning, UpdatedAt: time.Now()})
	m := deployment.Monitor{Observer: obs, Sleep: func(context.Context, time.Duration) error { return nil }}

	out, err := m.Run(map[string]string{
		"deployment": "d6", "endpoint": "http://deploy.internal",
		"watch": "true", "interval": "1", "timeout": "3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["state"] != "running" || out["polls"] != "4" { // 1 + timeout/interval
		t.Fatalf("state/polls = %q/%q", out["state"], out["polls"])
	}
}

// TestMonitorDoesNotBlameASucceededDeploymentForScaryLogLines: a rollout that
// finished fine is not reported as a problem just because a retried step logged
// a scary word.
func TestMonitorDoesNotBlameASucceededDeploymentForScaryLogLines(t *testing.T) {
	obs := observeOnce(deployment.Snapshot{
		ID: "d11", State: deployment.StateSucceeded,
		Logs: []string{"warn: connection refused, retrying", "step 2: ok"},
	})
	out, err := (deployment.Monitor{Observer: obs}).Run(map[string]string{
		"deployment": "d11", "endpoint": "http://deploy.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["problem"] != "false" || out["terminal"] != "true" {
		t.Fatalf("problem/terminal = %q/%q", out["problem"], out["terminal"])
	}
	if _, ok := out["signals"]; ok {
		t.Fatalf("succeeded deployment reported signals: %q", out["signals"])
	}
}

// TestMonitorUnknownStateIsNotInvented: when the source does not say, the
// monitor reports unknown instead of guessing a state.
func TestMonitorUnknownStateIsNotInvented(t *testing.T) {
	obs := observeOnce(deployment.Snapshot{ID: "d7"})
	out, err := (deployment.Monitor{Observer: obs}).Run(map[string]string{
		"deployment": "d7", "endpoint": "http://deploy.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["state"] != "unknown" || out["terminal"] != "false" || out["problem"] != "false" {
		t.Fatalf("state/terminal/problem = %q/%q/%q", out["state"], out["terminal"], out["problem"])
	}
}

// TestMonitorDefaultsToTheConfiguredDeploymentAPI: with no URL in the input the
// monitor uses $DEPLOYMENT_API_URL — the same variable service.deploy and the
// gateway use — falling back to the local control plane.
func TestMonitorDefaultsToTheConfiguredDeploymentAPI(t *testing.T) {
	t.Setenv(deployment.EnvAPIURL, "http://deploy.internal/")
	obs := observeOnce(deployment.Snapshot{ID: "d9", State: deployment.StateRunning})
	if _, err := (deployment.Monitor{Observer: obs}).Run(map[string]string{"deployment": "d9"}); err != nil {
		t.Fatal(err)
	}
	if obs.last.Endpoint != "http://deploy.internal/" {
		t.Fatalf("endpoint=%q", obs.last.Endpoint)
	}

	t.Setenv(deployment.EnvAPIURL, "")
	obs = observeOnce(deployment.Snapshot{ID: "d9", State: deployment.StateRunning})
	if _, err := (deployment.Monitor{Observer: obs}).Run(map[string]string{"deployment": "d9"}); err != nil {
		t.Fatal(err)
	}
	if obs.last.Endpoint != deployment.DefaultAPIURL {
		t.Fatalf("endpoint=%q, want the local control plane %q", obs.last.Endpoint, deployment.DefaultAPIURL)
	}
}

// TestMonitorAcceptsWhatServiceDeployReturns: the pipeline id (and the poll
// path) that service.deploy hands back name the thing to monitor.
func TestMonitorAcceptsWhatServiceDeployReturns(t *testing.T) {
	obs := observeOnce(deployment.Snapshot{ID: "p-1", State: deployment.StateRunning})
	out, err := (deployment.Monitor{Observer: obs}).Run(map[string]string{
		"pipeline_id": "p-1", "poll": "/api/pipelines/p-1", "endpoint": "http://deploy.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if obs.last.Deployment != "p-1" {
		t.Fatalf("deployment=%q", obs.last.Deployment)
	}
	if obs.last.Poll != "/api/pipelines/p-1" {
		t.Fatalf("poll=%q", obs.last.Poll)
	}
	if out["deployment"] != "p-1" {
		t.Fatalf("out deployment=%q", out["deployment"])
	}
}

// TestMonitorRequiresSomethingToFollow: no deployment and no poll path means
// nothing to observe.
func TestMonitorRequiresSomethingToFollow(t *testing.T) {
	_, err := (deployment.Monitor{Observer: observeOnce(deployment.Snapshot{})}).Run(map[string]string{"endpoint": "http://x"})
	if err == nil || !strings.Contains(err.Error(), "missing deployment") {
		t.Fatalf("err=%v", err)
	}
}

// TestMonitorObservationFailureIsAnError: not being able to observe at all is
// different from observing a failure.
func TestMonitorObservationFailureIsAnError(t *testing.T) {
	boom := errors.New("connection refused")
	obs := &fakeObserver{fn: func(deployment.Request) (deployment.Snapshot, error) { return deployment.Snapshot{}, boom }}
	_, err := (deployment.Monitor{Observer: obs}).Run(map[string]string{
		"deployment": "d10", "endpoint": "http://deploy.internal",
	})
	if err == nil || !strings.Contains(err.Error(), "connection refused") || !strings.Contains(err.Error(), deployment.Name) {
		t.Fatalf("err=%v", err)
	}
}

func TestMonitorMetadata(t *testing.T) {
	m := deployment.Monitor{}
	if m.Name() != "deployment.monitor" || m.Domain() != "deployment" || m.Provider() != deployment.Provider {
		t.Fatalf("meta name=%s domain=%s provider=%s", m.Name(), m.Domain(), m.Provider())
	}
	d := m.Description()
	for _, want := range []string{"deployment", "status_url", "watch", "evidence"} {
		if !strings.Contains(d, want) {
			t.Fatalf("description %q does not mention %q", d, want)
		}
	}
}

// TestParseRequestDefaultsAndBounds: every optional input has a documented
// default, and out-of-range values are clamped rather than trusted.
func TestParseRequestDefaultsAndBounds(t *testing.T) {
	t.Setenv(deployment.EnvAPIURL, "http://deploy.internal")

	req, err := deployment.ParseRequest(map[string]string{"deployment": "d11"})
	if err != nil {
		t.Fatal(err)
	}
	if req.Deployment != "d11" || req.Endpoint != "http://deploy.internal" {
		t.Fatalf("req=%+v", req)
	}
	if req.Tail != 40 || req.Watch || req.Interval != 5*time.Second || req.Timeout != 60*time.Second {
		t.Fatalf("defaults = tail:%d watch:%v interval:%s timeout:%s", req.Tail, req.Watch, req.Interval, req.Timeout)
	}

	req, err = deployment.ParseRequest(map[string]string{
		"deployment": "d11", "status_url": "http://deploy.internal/runs/9",
		"tail": "99999", "watch": "yes", "interval": "30", "timeout": "99999",
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Tail != 500 {
		t.Fatalf("tail=%d, want it clamped to 500", req.Tail)
	}
	if !req.Watch {
		t.Fatal("watch=yes should be true")
	}
	if req.Timeout != 10*time.Minute {
		t.Fatalf("timeout=%s, want it clamped to 10m", req.Timeout)
	}
	if req.Interval != 30*time.Second {
		t.Fatalf("interval=%s, want the requested 30s", req.Interval)
	}
	if req.StatusURL != "http://deploy.internal/runs/9" {
		t.Fatalf("status_url=%q", req.StatusURL)
	}

	// An interval longer than the window would make the first poll the only one.
	req, err = deployment.ParseRequest(map[string]string{
		"deployment": "d12", "interval": "600", "timeout": "60",
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Interval != time.Minute {
		t.Fatalf("interval=%s, want it clamped to the 60s timeout", req.Interval)
	}

	for _, bad := range []map[string]string{
		{"deployment": "d11", "tail": "-1"},
		{"deployment": "d11", "interval": "0"},
		{"deployment": "d11", "timeout": "abc"},
		{},
	} {
		if _, err := deployment.ParseRequest(bad); err == nil {
			t.Fatalf("ParseRequest(%v) = nil error", bad)
		}
	}
}

// TestDiagnoseClassifiesLogFingerprints exercises the exported diagnosis on its
// own, including the rules an agent will meet most often in a failed rollout.
func TestDiagnoseClassifiesLogFingerprints(t *testing.T) {
	cases := []struct {
		line   string
		signal string
	}{
		{"Error: ImagePullBackOff for image registry/app:v2", "image_pull"},
		{"context deadline exceeded while waiting for rollout", "timeout"},
		{"dial tcp 10.0.0.5:5432: connect: connection refused", "connection"},
		{"permission denied: cannot write to /var/lib/app", "permission"},
		{"panic: runtime error: index out of range", "crash"},
		{"Back-off restarting failed container", "crash_loop"},
	}
	for _, tc := range cases {
		got := deployment.Diagnose(deployment.Snapshot{ID: "x", State: deployment.StateRunning, Logs: []string{tc.line}}, time.Now(), 0)
		if !got.Problem || !strings.Contains(strings.Join(got.Signals, ","), tc.signal) {
			t.Fatalf("%q -> signals=%v, want %q", tc.line, got.Signals, tc.signal)
		}
	}
}

// TestDiagnoseEvidenceWindowIsBoundedToTheRequest: the evidence is a window, not
// the whole log — the decision prompt stays a sane size.
func TestDiagnoseEvidenceWindowIsBoundedToTheRequest(t *testing.T) {
	logs := make([]string, 200)
	for i := range logs {
		logs[i] = "line-" + string(rune('a'+i%26))
	}
	logs[10] = "fatal: the deployment failed"
	got := deployment.Diagnose(deployment.Snapshot{ID: "x", State: deployment.StateFailed, Logs: logs}, time.Now(), 5)
	if n := strings.Count(got.Evidence, "\n") + 1; n != 5 {
		t.Fatalf("evidence has %d lines, want 5:\n%s", n, got.Evidence)
	}
	if !strings.Contains(got.Evidence, "fatal: the deployment failed") {
		t.Fatalf("evidence lost the failing line:\n%s", got.Evidence)
	}
}
