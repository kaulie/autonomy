package deployment_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaulie/autonomy/src/capability/broker"
	"github.com/kaulie/autonomy/src/capability/deployment"
)

// fakeSession is one acquired monitoring agent.
type fakeSession struct {
	id       string
	answer   string
	err      error
	prompt   string   // the last prompt
	prompts  []string // every prompt, in order: one per observation
	released bool
}

func (s *fakeSession) ID() string { return s.id }

func (s *fakeSession) Workspace() string { return "/sandbox/agent-deployment.monitor-1/" }

func (s *fakeSession) Prompt(_ context.Context, prompt string) (string, error) {
	s.prompt = prompt
	s.prompts = append(s.prompts, prompt)
	return s.answer, s.err
}

func (s *fakeSession) Release(context.Context) error {
	s.released = true
	return nil
}

// fakeBroker records what the capability asked for.
type fakeBroker struct {
	lastOpts broker.AcquireAgentOpts
	sess     broker.AgentSession
	err      error
	calls    int
}

func (b *fakeBroker) AcquireAgent(_ context.Context, opts broker.AcquireAgentOpts) (broker.AgentSession, error) {
	b.calls++
	b.lastOpts = opts
	if b.err != nil {
		return nil, b.err
	}
	return b.sess, nil
}

// useRepoPrompt points PROJECT_ROOT at this repository, so the monitoring prompt
// is read from its real file ($PROJECT_ROOT/src/agent_policy/DEPLOYMENT_MONITOR.md).
func useRepoPrompt(t *testing.T) {
	t.Helper()
	t.Setenv("PROJECT_ROOT", filepath.Join("..", "..", ".."))
}

const agentAnswerJSON = "```json\n" +
	`{"state":"failed","phase":"deploy","progress":"2/3","healthy":false,` +
	`"message":"rollout stopped","error":"container terminated: OOMKilled",` +
	`"problem":true,"signals":["oom"],` +
	`"diagnosis":"the new revision was killed for exceeding its memory limit",` +
	`"suggestions":["raise the memory limit and redeploy"],` +
	`"logs":["apply: ok","container terminated: OOMKilled (limit 512Mi)"]}` +
	"\n```\n"

// TestAgentObserverGivesTheAgentTheRawObservationAndTakesItsVerdict pins the
// agent-backed provider: the monitoring agent receives the deployment's
// coordinates plus the raw observation, and its own verdict comes back as the
// snapshot.
func TestAgentObserverGivesTheAgentTheRawObservationAndTakesItsVerdict(t *testing.T) {
	useRepoPrompt(t)
	sess := &fakeSession{id: "agent-deployment.monitor-1", answer: "here you go:\n" + agentAnswerJSON}
	b := &fakeBroker{sess: sess}
	base := observeOnce(deployment.Snapshot{
		ID: "req-77", State: deployment.StateRunning, Phase: "rollout", Progress: "2/5",
		Logs: []string{"apply: ok", "waiting for rollout to finish"},
	})
	obs := &deployment.AgentObserver{Agents: b, Base: base}

	snap, err := obs.Observe(context.Background(), deployment.Request{
		Deployment: "req-77", Endpoint: "http://deploy.internal", Tail: 10, TaskID: "task-9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != deployment.StateFailed || snap.Phase != "deploy" || snap.Progress != "2/3" {
		t.Fatalf("snap=%+v", snap)
	}
	if snap.AgentProblem == nil || !*snap.AgentProblem {
		t.Fatalf("agent verdict lost: %+v", snap)
	}
	if len(snap.AgentSignals) != 1 || snap.AgentSignals[0] != "oom" {
		t.Fatalf("signals=%v", snap.AgentSignals)
	}
	if !strings.Contains(snap.AgentDiagnosis, "memory limit") {
		t.Fatalf("diagnosis=%q", snap.AgentDiagnosis)
	}
	if len(snap.Logs) != 2 || !strings.Contains(snap.Logs[1], "OOMKilled") {
		t.Fatalf("logs=%v", snap.Logs)
	}
	if snap.Healthy == nil || *snap.Healthy {
		t.Fatalf("healthy=%v", snap.Healthy)
	}

	// The prompt names the deployment, the exact URLs the agent could fetch and
	// the raw observation it was handed.
	for _, want := range []string{
		"req-77",
		"http://deploy.internal/api/pipelines/req-77",
		"http://deploy.internal/api/pipelines/req-77/logs",
		"waiting for rollout to finish",
		"deployment: req-77",
	} {
		if !strings.Contains(sess.prompt, want) {
			t.Fatalf("prompt does not mention %q:\n%s", want, sess.prompt)
		}
	}
	if strings.Contains(sess.prompt, "{{") {
		t.Fatalf("prompt still has an unrendered placeholder:\n%s", sess.prompt)
	}
	if b.calls != 1 || b.lastOpts.Purpose != deployment.Name || b.lastOpts.TaskID != "task-9" {
		t.Fatalf("acquire opts=%+v calls=%d", b.lastOpts, b.calls)
	}
	if !sess.released {
		t.Fatal("the monitoring agent was not released")
	}
}

// TestAgentObserverKeepsWhatTheAgentDidNotSay: a partial answer overrides only
// what it speaks to — the raw observation fills the rest.
func TestAgentObserverKeepsWhatTheAgentDidNotSay(t *testing.T) {
	useRepoPrompt(t)
	sess := &fakeSession{answer: `{"message":"still rolling out"}`}
	base := observeOnce(deployment.Snapshot{ID: "p-1", State: deployment.StateRunning, Phase: "rollout", Progress: "2/5"})
	obs := &deployment.AgentObserver{Agents: &fakeBroker{sess: sess}, Base: base}

	snap, err := obs.Observe(context.Background(), deployment.Request{Deployment: "p-1", Endpoint: "http://deploy.internal", Tail: 10})
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != deployment.StateRunning || snap.Phase != "rollout" || snap.Progress != "2/5" {
		t.Fatalf("raw observation lost: %+v", snap)
	}
	if snap.Message != "still rolling out" {
		t.Fatalf("message=%q", snap.Message)
	}
	if snap.AgentProblem != nil || len(snap.AgentSignals) != 0 {
		t.Fatalf("agent invented a verdict: %+v", snap)
	}
}

// TestAgentObserverAcceptsTheAnswerShapesAgentsActuallySend: a fenced block or a
// bare object with prose around it both count.
func TestAgentObserverAcceptsTheAnswerShapesAgentsActuallySend(t *testing.T) {
	useRepoPrompt(t)
	cases := []struct {
		name   string
		answer string
		state  deployment.State
	}{
		{"fenced", "```json\n{\"state\":\"failed\"}\n```", deployment.StateFailed},
		{"bare with prose", "I checked it.\n{\"state\":\"succeeded\"}\nThat is all.", deployment.StateSucceeded},
		{"plain json", `{"state":"running"}`, deployment.StateRunning},
		{"nested braces", `{"state":"failed","logs":["a"],"extra":{"b":[1,2]}}`, deployment.StateFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := &fakeSession{answer: tc.answer}
			base := observeOnce(deployment.Snapshot{ID: "p-8", State: deployment.StatePending})
			obs := &deployment.AgentObserver{Agents: &fakeBroker{sess: sess}, Base: base}
			snap, err := obs.Observe(context.Background(), deployment.Request{Deployment: "p-8", Endpoint: "http://x", Tail: 5})
			if err != nil {
				t.Fatal(err)
			}
			if snap.State != tc.state {
				t.Fatalf("state=%q, want %q", snap.State, tc.state)
			}
			if snap.AgentNote != "" {
				t.Fatalf("note=%q, want the answer parsed", snap.AgentNote)
			}
		})
	}
}

// TestAgentObserverFallsBackToTheRawObservationInsteadOfGuessing: an answer the
// schema cannot be read out of does not become an invented state — the raw
// observation stands and the miss is reported.
func TestAgentObserverFallsBackToTheRawObservationInsteadOfGuessing(t *testing.T) {
	useRepoPrompt(t)
	for _, answer := range []string{"I could not reach the deployment API.", "{}", "```json\n{}\n```"} {
		sess := &fakeSession{answer: answer}
		base := observeOnce(deployment.Snapshot{ID: "p-8", State: deployment.StateRunning})
		obs := &deployment.AgentObserver{Agents: &fakeBroker{sess: sess}, Base: base}
		snap, err := obs.Observe(context.Background(), deployment.Request{Deployment: "p-8", Endpoint: "http://x", Tail: 5})
		if err != nil {
			t.Fatal(err)
		}
		if snap.State != deployment.StateRunning || snap.AgentNote == "" {
			t.Fatalf("answer %q: snap=%+v, want the raw observation plus a note", answer, snap)
		}
	}
}

// TestAgentObserverReportsARealFailure: when neither the reader nor the agent
// produced anything, the observation failed — it is not reported as "unknown".
func TestAgentObserverReportsARealFailure(t *testing.T) {
	useRepoPrompt(t)
	boom := errors.New("connection refused")
	sess := &fakeSession{answer: "no idea"}
	obs := &deployment.AgentObserver{
		Agents: &fakeBroker{sess: sess},
		Base: &fakeObserver{fn: func(deployment.Request) (deployment.Snapshot, error) {
			return deployment.Snapshot{}, boom
		}},
	}
	_, err := obs.Observe(context.Background(), deployment.Request{Deployment: "p-2", Endpoint: "http://x", Tail: 5})
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err=%v", err)
	}
}

// TestAgentObserverFailsWithoutThePromptTemplate: no prompt, no agent — and no
// agent is acquired just to be left unprompted.
func TestAgentObserverFailsWithoutThePromptTemplate(t *testing.T) {
	t.Setenv("PROJECT_ROOT", t.TempDir())
	b := &fakeBroker{sess: &fakeSession{answer: agentAnswerJSON}}
	obs := &deployment.AgentObserver{Agents: b, Base: observeOnce(deployment.Snapshot{ID: "p-3"})}
	_, err := obs.Observe(context.Background(), deployment.Request{Deployment: "p-3", Endpoint: "http://x", Tail: 5})
	if err == nil || !strings.Contains(err.Error(), deployment.DefaultPromptRel) {
		t.Fatalf("err=%v, want a missing-template error naming %s", err, deployment.DefaultPromptRel)
	}
	if b.calls != 0 {
		t.Fatalf("acquired an agent without a prompt: %+v", b.lastOpts)
	}
}

// TestAgentObserverRequiresABroker.
func TestAgentObserverRequiresABroker(t *testing.T) {
	useRepoPrompt(t)
	_, err := (&deployment.AgentObserver{}).Observe(context.Background(), deployment.Request{Deployment: "p-4", Endpoint: "http://x"})
	if err == nil || !strings.Contains(err.Error(), "agent broker not configured") {
		t.Fatalf("err=%v", err)
	}
}

// TestAgentObserverSurfacesAnAcquireFailure.
func TestAgentObserverSurfacesAnAcquireFailure(t *testing.T) {
	useRepoPrompt(t)
	obs := &deployment.AgentObserver{
		Agents: &fakeBroker{err: errors.New("no agent available")},
		Base:   observeOnce(deployment.Snapshot{ID: "p-5"}),
	}
	_, err := obs.Observe(context.Background(), deployment.Request{Deployment: "p-5", Endpoint: "http://x", Tail: 5})
	if err == nil || !strings.Contains(err.Error(), "no agent available") {
		t.Fatalf("err=%v", err)
	}
}

// TestAgentObserverSurfacesAPromptFailure.
func TestAgentObserverSurfacesAPromptFailure(t *testing.T) {
	useRepoPrompt(t)
	obs := &deployment.AgentObserver{
		Agents: &fakeBroker{sess: &fakeSession{err: errors.New("agent crashed")}},
		Base:   observeOnce(deployment.Snapshot{ID: "p-6"}),
	}
	_, err := obs.Observe(context.Background(), deployment.Request{Deployment: "p-6", Endpoint: "http://x", Tail: 5})
	if err == nil || !strings.Contains(err.Error(), "agent crashed") {
		t.Fatalf("err=%v", err)
	}
}

// TestMonitorMonitorsThroughAnAgentWhenItHasABroker: registering the capability
// with the runtime's agent broker is what makes the observation agent-backed —
// and the agent's verdict is what the report carries.
func TestMonitorMonitorsThroughAnAgentWhenItHasABroker(t *testing.T) {
	useRepoPrompt(t)
	// The deterministic reader is pointed at a service that answers nothing, so
	// this also pins that the agent's verdict does not depend on it.
	srv := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	sess := &fakeSession{id: "agent-deployment.monitor-1", answer: agentAnswerJSON}
	b := &fakeBroker{sess: sess}

	out, err := (deployment.Monitor{Agents: b}).Run(map[string]string{
		"pipeline_id": "req-77", "endpoint": srv.URL, "task_id": "task-9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["source"]; ok {
		t.Errorf("out=%v, want no source: who looked is the capability's wiring, not the observation", out)
	}
	if out["state"] != "failed" || out["problem"] != "true" {
		t.Fatalf("state/problem = %q/%q", out["state"], out["problem"])
	}
	if !strings.Contains(out["signals"], "oom") {
		t.Fatalf("signals=%q, want the agent's own signal", out["signals"])
	}
	// The structural fact about the observed state stays on top of the agent's
	// judgement: the agent says why, the state says what happened.
	if !strings.Contains(out["signals"], "deployment_failed") {
		t.Fatalf("signals=%q, want the observed state's own signal too", out["signals"])
	}
	if !strings.Contains(out["diagnosis"], "memory limit") {
		t.Fatalf("diagnosis=%q", out["diagnosis"])
	}
	if !strings.Contains(out["suggestions"], "raise the memory limit") {
		t.Fatalf("suggestions=%q", out["suggestions"])
	}
	if !strings.Contains(out["evidence"], "OOMKilled") {
		t.Fatalf("evidence=%q", out["evidence"])
	}
	if b.calls != 1 || b.lastOpts.TaskID != "task-9" || b.lastOpts.Purpose != deployment.Name {
		t.Fatalf("acquire opts=%+v calls=%d", b.lastOpts, b.calls)
	}
}

// TestMonitorObservesThroughTheReaderItHas: which provider monitors is the
// capability's own wiring, and the report says what was observed either way —
// without a broker the HTTP reader answers, and an injected observer answers
// instead of it (the server is not read at all).
func TestMonitorObservesThroughTheReaderItHas(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"state":"running"}`)
	})
	out, err := (deployment.Monitor{}).Run(map[string]string{"deployment": "p-9", "endpoint": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if out["state"] != "running" {
		t.Fatalf("state=%q, want the HTTP reader's observation", out["state"])
	}
	if _, ok := out["source"]; ok {
		t.Errorf("out=%v, want no source: who looked is not what was seen", out)
	}
	if !srv.asked("/api/pipelines/p-9") {
		t.Fatalf("the HTTP reader did not answer: %v", srv.paths)
	}

	// An injected observer answers instead, and nothing is read over HTTP.
	read := len(srv.paths)
	injected := observeOnce(deployment.Snapshot{ID: "p-9", State: deployment.StateSucceeded})
	out, err = (deployment.Monitor{Observer: injected}).
		Run(map[string]string{"deployment": "p-9", "endpoint": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if out["state"] != "succeeded" {
		t.Fatalf("state=%q, want the injected observer's snapshot", out["state"])
	}
	if len(srv.paths) != read {
		t.Fatalf("the injected observer answered but the server was read: %v", srv.paths[read:])
	}
}

// TestWatchAsksTheAgentOnceAndTellsItWhatHappened: a watch polls the deployment API every
// round (cheap, deterministic) and asks the monitoring agent **once**, when the observation
// is over — handing it the changes it passed through, because asking a model every five
// seconds is one model call per five seconds. One deployment.monitor call holds one agent.
func TestWatchAsksTheAgentOnceAndTellsItWhatHappened(t *testing.T) {
	useRepoPrompt(t)
	reads := 0
	srv := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Count the status reads only: the reader also fetches the logs path.
		if !strings.HasSuffix(r.URL.Path, "/logs") {
			reads++
		}
		state := "running"
		if reads >= 3 {
			state = "succeeded"
		}
		_, _ = io.WriteString(w, `{"state":"`+state+`","phase":"deploy"}`)
	})
	sess := &fakeSession{id: "agent-deployment.monitor-1", answer: agentAnswerJSON}
	b := &fakeBroker{sess: sess}

	out, err := (deployment.Monitor{Agents: b, Sleep: func(context.Context, time.Duration) error { return nil }}).Run(map[string]string{
		"deployment": "p-watch", "endpoint": srv.URL,
		"watch": "true", "interval": "1", "timeout": "30",
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.calls != 1 {
		t.Fatalf("agents acquired=%d, want one agent for the whole call", b.calls)
	}
	if len(sess.prompts) != 1 {
		t.Fatalf("the agent was asked %d time(s), want once for the whole watch", len(sess.prompts))
	}
	prompt := sess.prompts[0]
	// Asked once, told how often it was read and what changed (the two polls where nothing
	// changed are not two lines), and asked about the observation that settled.
	if !strings.Contains(prompt, "watched (3 poll(s), 1 change(s)") {
		t.Fatalf("the observation does not say what the watch saw: %s", prompt)
	}
	if !strings.Contains(prompt, "state: succeeded") {
		t.Fatalf("the observation is not the one that settled: %s", prompt)
	}
	if !sess.released {
		t.Error("the agent was not released when the call ended")
	}
	if out["state"] != "failed" {
		t.Fatalf("state=%q, want the agent's own verdict (agentAnswerJSON says failed)", out["state"])
	}
}

// TestWatchHandsItsJudgeWhatChangedAndItsEvidence: the polls where nothing changed are one
// line — a change is what a monitoring agent is asked about — and a change that looked
// wrong carries its evidence, so a failure that appeared mid-rollout and went away is still
// in front of the agent when the deployment ends up succeeding.
func TestWatchHandsItsJudgeWhatChangedAndItsEvidence(t *testing.T) {
	useRepoPrompt(t)
	reads := 0
	srv := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/logs") {
			if reads < 3 {
				_, _ = io.WriteString(w, `{"lines":["applying revision 2","container terminated: OOMKilled (limit 512Mi)"]}`)
				return
			}
			_, _ = io.WriteString(w, `{"lines":[]}`)
			return
		}
		reads++
		state := "running"
		if reads >= 3 {
			state = "succeeded"
		}
		_, _ = io.WriteString(w, `{"state":"`+state+`"}`)
	})
	sess := &fakeSession{id: "agent-deployment.monitor-1", answer: agentAnswerJSON}
	b := &fakeBroker{sess: sess}

	if _, err := (deployment.Monitor{Agents: b, Sleep: func(context.Context, time.Duration) error { return nil }}).Run(map[string]string{
		"deployment": "p-flap", "endpoint": srv.URL,
		"watch": "true", "interval": "1", "timeout": "30",
	}); err != nil {
		t.Fatal(err)
	}
	if len(sess.prompts) != 1 {
		t.Fatalf("the agent was asked %d time(s), want once", len(sess.prompts))
	}
	prompt := sess.prompts[0]
	if !strings.Contains(prompt, "signals: oom") {
		t.Fatalf("the change the local rules found was not handed on: %s", prompt)
	}
	// The evidence of that change: the logs it rested on are gone from the settled
	// observation, so this can only have come from the trail.
	if !strings.Contains(prompt, "OOMKilled") {
		t.Fatalf("the evidence around the change was not handed on: %s", prompt)
	}
	if !strings.Contains(prompt, "state: succeeded") {
		t.Fatalf("the observation is not the one that settled: %s", prompt)
	}
}

// frameSession is a monitoring agent whose host can describe the runtime the
// observation was delegated from (broker.WorkerPromptContext).
type frameSession struct {
	fakeSession
	frame map[string]string
}

func (s *frameSession) WorkerPlaceholders() map[string]string { return s.frame }

// TestAgentObserverInjectsTheDelegationFrame: the monitoring agent is a delegated
// worker, so it is told the world it is observing, the context of the delegation,
// the completion principles of this task's goal and the constraints it works
// under — the same frame every other delegated worker gets — while a host that
// cannot describe one renders those sections as such instead of leaking {{NAME}}
// to the agent.
func TestAgentObserverInjectsTheDelegationFrame(t *testing.T) {
	useRepoPrompt(t)
	base := observeOnce(deployment.Snapshot{ID: "req-77", State: deployment.StateRunning})

	framed := &frameSession{
		fakeSession: fakeSession{id: "agent-deployment.monitor-1", answer: agentAnswerJSON},
		frame: map[string]string{
			"{{AGENT}}":                 `{"role":"worker","purpose":"deployment.monitor","name":"agent-deployment.monitor-1"}`,
			"{{WORLD}}":                 `{"assets":[{"id":"asset-1","kind":"service","state":"healthy"}]}`,
			"{{RUNTIME_CONTEXT}}":       `{"cycle":2,"task":{"id":"task-9"},"delegated_by":{"agent":"agent-10095"}}`,
			"{{COMPLETION_PRINCIPLES}}": "- Keep the service available and healthy.",
			"{{CONSTRAINTS}}":           `{"deploy":"the Runtime's move, not the agent's"}`,
		},
	}
	if _, err := (&deployment.AgentObserver{Agents: &fakeBroker{sess: framed}, Base: base}).Observe(
		context.Background(), deployment.Request{Deployment: "req-77", Tail: 10, TaskID: "task-9"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Agent", `"role":"worker"`,
		"## World", `"asset-1"`,
		"## Runtime Context", `"delegated_by"`, `"task-9"`,
		"## Completion",
		"## Completion Principles", "- Keep the service available and healthy.",
		"## Constraints", "the Runtime's move, not the agent's",
	} {
		if !strings.Contains(framed.prompt, want) {
			t.Errorf("monitoring prompt missing %q:\n%s", want, framed.prompt)
		}
	}
	if strings.Contains(framed.prompt, "{{") {
		t.Errorf("monitoring prompt still has an unrendered placeholder:\n%s", framed.prompt)
	}

	// A host that cannot describe a runtime still produces a readable prompt: the
	// frame sections say they were not provided.
	plain := &fakeSession{id: "agent-deployment.monitor-2", answer: agentAnswerJSON}
	if _, err := (&deployment.AgentObserver{Agents: &fakeBroker{sess: plain}, Base: base}).Observe(
		context.Background(), deployment.Request{Deployment: "req-77", Tail: 10, TaskID: "task-9"}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(plain.prompt, broker.WorkerFrameMissingValue); n != 5 {
		t.Errorf("prompt marks %d frame sections as not provided, want 5:\n%s", n, plain.prompt)
	}
	if strings.Contains(plain.prompt, "{{") {
		t.Errorf("monitoring prompt still has an unrendered placeholder:\n%s", plain.prompt)
	}
}

// TestShippedMonitoringPromptKeepsTheAgentObservingOnly: the prompt is the
// agent's contract, so the safety property lives in the shipped file, not only
// in the code that renders it.
func TestShippedMonitoringPromptKeepsTheAgentObservingOnly(t *testing.T) {
	useRepoPrompt(t)
	root := os.Getenv("PROJECT_ROOT")
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(deployment.DefaultPromptRel)))
	if err != nil {
		t.Fatal(err)
	}
	prompt := string(b)
	for _, want := range []string{
		"OBSERVE and REPORT",
		"no retry, no",
		"never invent",
		"is not a problem",
		"{{DEPLOYMENT}}",
		"{{OBSERVATION}}",
		// The frame sections a delegated worker's prompt carries: the monitoring
		// agent is told the world it observes, the context of the delegation, what
		// completion means here and the constraints it works under.
		"{{WORLD}}",
		"{{RUNTIME_CONTEXT}}",
		"{{COMPLETION_PRINCIPLES}}",
		"{{CONSTRAINTS}}",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("shipped prompt no longer says %q:\n%s", want, prompt)
		}
	}
}
