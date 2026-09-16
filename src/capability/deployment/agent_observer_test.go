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

	"github.com/kaulie/autonomy/src/capability/broker"
	"github.com/kaulie/autonomy/src/capability/deployment"
)

// fakeSession is one acquired monitoring agent.
type fakeSession struct {
	id       string
	answer   string
	err      error
	prompt   string
	released bool
}

func (s *fakeSession) ID() string { return s.id }

func (s *fakeSession) Workspace() string { return "/sandbox/agent-deployment.monitor-1/" }

func (s *fakeSession) Prompt(_ context.Context, prompt string) (string, error) {
	s.prompt = prompt
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
	if out["source"] != "agent" {
		t.Fatalf("source=%q, want the monitoring agent to have produced this", out["source"])
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

// TestMonitorReportsWhichProviderMonitored: without a broker the deterministic
// reader answers (and says so); an explicitly injected observer is reported as
// custom.
func TestMonitorReportsWhichProviderMonitored(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"state":"running"}`)
	})
	out, err := (deployment.Monitor{}).Run(map[string]string{"deployment": "p-9", "endpoint": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if out["source"] != "http" {
		t.Fatalf("source=%q, want http without an agent broker", out["source"])
	}

	out, err = (deployment.Monitor{Observer: observeOnce(deployment.Snapshot{ID: "p-9", State: deployment.StateRunning})}).
		Run(map[string]string{"deployment": "p-9", "endpoint": "http://x"})
	if err != nil {
		t.Fatal(err)
	}
	if out["source"] != "custom" {
		t.Fatalf("source=%q, want custom for an injected observer", out["source"])
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
			"{{RUNTIME_CONTEXT}}":       `{"step":2,"task":{"id":"task-9"},"delegated_by":{"agent":"agent-10095"}}`,
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
