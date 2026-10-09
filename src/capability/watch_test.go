package capability_test

import (
	"context"
	"testing"
	"time"

	"github.com/kaulie/autonomy/src/capability"
	"github.com/kaulie/autonomy/src/watcher"
)

func TestWatchRegistersBackgroundUntilMerge(t *testing.T) {
	obs := watcher.Observation{
		"exists": "true", "state": "open", "merged": "false",
		"pr":     "https://github.com/kaulie/agent-watchdog/pull/9",
		"number": "9", "repo": "kaulie/agent-watchdog",
	}
	w := watcher.New([]watcher.Probe{watcher.PullRequestProbe{
		Snapshot: func(string) (watcher.Observation, error) { return obs, nil },
	}}, watcher.WithErrors(func(string, ...any) {}))
	t.Cleanup(w.Close)

	c := capability.Watch{Watches: w}
	out, err := c.Run(map[string]string{
		"pr":      "https://github.com/kaulie/agent-watchdog/pull/9",
		"task_id": "task-watchdog",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["merged"] != "false" || out["watching"] != "true" || out["terminal"] != "false" || out["kind"] != watcher.KindPullRequest {
		t.Fatalf("out=%v, want an open PR with a background watch", out)
	}
	if n := len(w.List()); n != 1 {
		t.Fatalf("list=%d, want one watch registered from the pr alias", n)
	}
}

func TestWatchAlreadyMergedDoesNotWatch(t *testing.T) {
	w := watcher.New([]watcher.Probe{watcher.PullRequestProbe{
		Snapshot: func(string) (watcher.Observation, error) {
			return watcher.Observation{
				"exists": "true", "state": "closed", "merged": "true",
				"pr": "https://github.com/kaulie/agent-watchdog/pull/9",
			}, nil
		},
	}}, watcher.WithErrors(func(string, ...any) {}))
	t.Cleanup(w.Close)

	out, err := (capability.Watch{Watches: w}).Run(map[string]string{
		"pr": "https://github.com/kaulie/agent-watchdog/pull/9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["merged"] != "true" || out["watching"] != "false" || out["terminal"] != "true" {
		t.Fatalf("out=%v, want merged with no background watch", out)
	}
	if n := len(w.List()); n != 0 {
		t.Fatalf("must not register a watch for an already-merged PR: %d", n)
	}
}

func TestWatchInCallWaitsUntilMerged(t *testing.T) {
	var n int
	w := watcher.New([]watcher.Probe{watcher.PullRequestProbe{
		Snapshot: func(string) (watcher.Observation, error) {
			n++
			merged := n > 1
			state, flag := "open", "false"
			if merged {
				state, flag = "closed", "true"
			}
			return watcher.Observation{"exists": "true", "state": state, "merged": flag, "pr": "https://github.com/kaulie/agent-watchdog/pull/9"}, nil
		},
	}}, watcher.WithErrors(func(string, ...any) {}))
	t.Cleanup(w.Close)

	c := capability.Watch{
		Watches: w,
		Sleep:   func(context.Context, time.Duration) error { return nil },
	}
	out, err := c.Run(map[string]string{
		"kind":     watcher.KindPullRequest,
		"target":   "https://github.com/kaulie/agent-watchdog/pull/9",
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

func TestWatchDeploymentKindUsesSameCapability(t *testing.T) {
	w := watcher.New([]watcher.Probe{watcher.DeploymentProbe{
		Snapshot: func(string) (watcher.Observation, error) {
			return watcher.Observation{"exists": "true", "state": "running", "deployment": "pipeline-1", "healthy": "false"}, nil
		},
	}}, watcher.WithErrors(func(string, ...any) {}))
	t.Cleanup(w.Close)

	out, err := (capability.Watch{Watches: w}).Run(map[string]string{
		"deployment": "pipeline-1",
		"task_id":    "task-dep",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["kind"] != watcher.KindDeployment || out["watching"] != "true" || out["until"] != watcher.UntilSucceeded {
		t.Fatalf("out=%v, want one watch capability covering deployment", out)
	}
}
