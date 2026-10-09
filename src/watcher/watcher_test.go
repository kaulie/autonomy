package watcher

import (
	"sync"
	"testing"
)

type seqProbe struct {
	kind   string
	until  string
	states []Observation
	n      int
}

func (p *seqProbe) Kind() string         { return p.kind }
func (p *seqProbe) DefaultUntil() string { return p.until }
func (p *seqProbe) Observe(Spec) (Observation, error) {
	if p.n >= len(p.states) {
		return p.states[len(p.states)-1], nil
	}
	out := p.states[p.n]
	p.n++
	return out, nil
}
func (p *seqProbe) ID(spec Spec, obs Observation) string {
	return p.kind + ":" + firstNonEmpty(obs["id"], spec.Target)
}
func (p *seqProbe) Fingerprint(obs Observation) string { return obs["state"] }
func (p *seqProbe) Done(obs Observation, until string) bool {
	if until == "" {
		until = p.until
	}
	return obs["state"] == until
}
func (p *seqProbe) Change(spec Spec, obs Observation) Change {
	return Change{
		Source: p.kind, Type: p.kind + "." + obs["state"],
		TaskID: spec.TaskID, AssetID: p.kind + ":" + spec.Target,
		Payload: map[string]any{"state": obs["state"]}, IdempotencyKey: p.kind + ":" + spec.Target + ":" + obs["state"],
	}
}

func TestWatcherEmitsWhenFingerprintMovesThenStops(t *testing.T) {
	p := &seqProbe{
		kind: "thing", until: "ready",
		states: []Observation{
			{"exists": "true", "state": "pending", "id": "a"},
			{"exists": "true", "state": "ready", "id": "a"},
		},
	}
	var got []Change
	w := New([]Probe{p}, WithSink(func(ch Change) error {
		got = append(got, ch)
		return nil
	}), WithErrors(func(string, ...any) {}))
	t.Cleanup(w.Close)

	if err := w.Watch(Spec{Kind: "thing", Target: "a", TaskID: "task-1", Fields: p.states[0]}); err != nil {
		t.Fatal(err)
	}
	w.PollOnce()
	if len(got) != 0 {
		t.Fatalf("same fingerprint must not emit: %+v", got)
	}
	w.PollOnce()
	if len(got) != 1 || got[0].Type != "thing.ready" || got[0].TaskID != "task-1" {
		t.Fatalf("got=%+v", got)
	}
	if views := w.List(); len(views) != 0 {
		t.Fatalf("watch should drop after until: %+v", views)
	}
}

func TestWatcherRejectsUnknownKind(t *testing.T) {
	w := New(nil)
	if _, err := w.Observe(Spec{Kind: "nope", Target: "x"}); err == nil {
		t.Fatal("want unknown kind")
	}
}

func TestPullRequestAndDeploymentShareOneWatcher(t *testing.T) {
	var mu sync.Mutex
	prMerged := false
	deployState := "running"
	w := New([]Probe{
		PullRequestProbe{Snapshot: func(string) (Observation, error) {
			mu.Lock()
			defer mu.Unlock()
			merged := "false"
			state := "open"
			if prMerged {
				merged, state = "true", "closed"
			}
			return Observation{
				"exists": "true", "state": state, "merged": merged,
				"pr":     "https://github.com/kaulie/agent-watchdog/pull/9",
				"number": "9", "repo": "kaulie/agent-watchdog",
			}, nil
		}},
		DeploymentProbe{Snapshot: func(string) (Observation, error) {
			mu.Lock()
			defer mu.Unlock()
			return Observation{"exists": "true", "state": deployState, "deployment": "pipeline-1", "healthy": "false"}, nil
		}},
	}, WithErrors(func(string, ...any) {}))
	t.Cleanup(w.Close)

	var changes []Change
	w.SetSink(func(ch Change) error {
		changes = append(changes, ch)
		return nil
	})

	prSpec := Spec{Kind: KindPullRequest, Target: "https://github.com/kaulie/agent-watchdog/pull/9", TaskID: "task-pr",
		Fields: Observation{"exists": "true", "state": "open", "merged": "false", "repo": "kaulie/agent-watchdog", "number": "9"}}
	depSpec := Spec{Kind: KindDeployment, Target: "pipeline-1", TaskID: "task-dep",
		Fields: Observation{"exists": "true", "state": "running", "healthy": "false"}}
	if err := w.Watch(prSpec); err != nil {
		t.Fatal(err)
	}
	if err := w.Watch(depSpec); err != nil {
		t.Fatal(err)
	}
	if n := len(w.List()); n != 2 {
		t.Fatalf("list=%d want 2 kinds at once", n)
	}

	mu.Lock()
	prMerged = true
	deployState = "succeeded"
	mu.Unlock()
	w.PollOnce()

	if len(changes) != 2 {
		t.Fatalf("changes=%+v, want PR merge and deployment succeeded", changes)
	}
	types := map[string]string{}
	for _, ch := range changes {
		types[ch.Type] = ch.TaskID
	}
	if types[typePullRequestMerged] != "task-pr" || types[typeDeploymentSucceeded] != "task-dep" {
		t.Fatalf("types=%v", types)
	}
	if got := w.List(); len(got) != 0 {
		t.Fatalf("both watches should stop: %+v", got)
	}
}
