package software_development_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability/broker"
	sd "github.com/kaulie/autonomy/src/capability/software_development"
)

type mockSession struct {
	id        string
	workspace string
	summary   string
	err       error
	prompt    string
}

func (m *mockSession) ID() string { return m.id }

func (m *mockSession) Workspace() string { return m.workspace }

func (m *mockSession) Prompt(_ context.Context, prompt string) (string, error) {
	m.prompt = prompt
	return m.summary, m.err
}

func (m *mockSession) Release(context.Context) error { return nil }

type mockBroker struct {
	lastOpts broker.AcquireAgentOpts
	sess     broker.AgentSession
	err      error
}

// mockWorkerSession is a session whose host can describe the delegation's runtime
// context (broker.WorkerPromptContext), so a test can pin what the worker prompt
// does with those values.
type mockWorkerSession struct {
	mockSession
	values map[string]string
}

func (m *mockWorkerSession) WorkerPlaceholders() map[string]string { return m.values }

func (m *mockBroker) AcquireAgent(_ context.Context, opts broker.AcquireAgentOpts) (broker.AgentSession, error) {
	m.lastOpts = opts
	if m.err != nil {
		return nil, m.err
	}
	return m.sess, nil
}

// useRepoPrompt points PROJECT_ROOT at this repository, so the worker prompt is
// read from its real file ($PROJECT_ROOT/src/agent_policy/CODE_EDIT.md).
func useRepoPrompt(t *testing.T) {
	t.Helper()
	t.Setenv("PROJECT_ROOT", filepath.Join("..", "..", ".."))
}

// TestCodeEditPromptIsReadFromTheTemplateEachRun pins the point of keeping the
// prompt out of Go source: the file under $PROJECT_ROOT is the prompt. Editing it
// (or replacing it) changes what the worker receives, with no code change.
func TestCodeEditPromptIsReadFromTheTemplateEachRun(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, filepath.FromSlash(sd.DefaultPromptRel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("ws={{WORKSPACE}} goal={{GOAL}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROJECT_ROOT", root)

	sess := &mockSession{id: "agent-code_edit-4", workspace: "/sandbox/agent-10099/", summary: "done"}
	if _, err := (sd.CodeEdit{Agents: &mockBroker{sess: sess}}).Run(map[string]string{"instruction": "do it"}); err != nil {
		t.Fatal(err)
	}
	if want := "ws=/sandbox/agent-10099/ goal=do it\n"; sess.prompt != want {
		t.Fatalf("prompt=%q, want the rendered template %q", sess.prompt, want)
	}
}

// TestCodeEditFailsWithoutPromptTemplate: no template, no delegation — and no
// agent is acquired just to be left unprompted.
func TestCodeEditFailsWithoutPromptTemplate(t *testing.T) {
	t.Setenv("PROJECT_ROOT", t.TempDir())
	m := &mockBroker{sess: &mockSession{id: "agent-code_edit-5", workspace: "/ws/"}}
	_, err := (sd.CodeEdit{Agents: m}).Run(map[string]string{"instruction": "do it"})
	if err == nil || !strings.Contains(err.Error(), sd.DefaultPromptRel) {
		t.Fatalf("err=%v, want a missing-template error naming %s", err, sd.DefaultPromptRel)
	}
	if m.lastOpts.Purpose != "" {
		t.Fatalf("acquired an agent without a prompt: %+v", m.lastOpts)
	}
}

func TestCodeEditRequiresInstruction(t *testing.T) {
	useRepoPrompt(t)
	c := sd.CodeEdit{Agents: &mockBroker{sess: &mockSession{summary: "ok"}}}
	_, err := c.Run(map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "instruction") {
		t.Fatalf("err=%v", err)
	}
}

// TestCodeEditDelegatesToTheWorkersOwnWorkspace pins the delegation contract: the
// worker runs in its own AGENT_WORKSPACE. The caller's workspace (the planner's)
// is never forwarded as the acquisition workspace nor written into the prompt —
// that is what used to put the worker inside the planner's sandbox.
func TestCodeEditDelegatesToTheWorkersOwnWorkspace(t *testing.T) {
	useRepoPrompt(t)
	const plannerWorkspace = "/Users/gaolei/agent-workspace-sandbox/agent-10095"
	const workerWorkspace = "/Users/gaolei/agent-workspace-sandbox/agent-10096/"
	sess := &mockSession{id: "agent-code_edit-1", workspace: workerWorkspace, summary: "edited files"}
	m := &mockBroker{sess: sess}
	c := sd.CodeEdit{Agents: m}
	out, err := c.Run(map[string]string{
		"workspace":   plannerWorkspace,
		"instruction": "add hello endpoint",
		"task_id":     "task-9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.lastOpts.Workspace != "" {
		t.Fatalf("acquire opts kept the caller's workspace: %+v", m.lastOpts)
	}
	if m.lastOpts.Purpose != sd.Name || m.lastOpts.TaskID != "task-9" {
		t.Fatalf("acquire opts: %+v", m.lastOpts)
	}
	if !strings.Contains(sess.prompt, workerWorkspace) {
		t.Fatalf("prompt does not name the worker's own workspace:\n%s", sess.prompt)
	}
	if strings.Contains(sess.prompt, plannerWorkspace) {
		t.Fatalf("prompt leaks the caller's workspace into the worker:\n%s", sess.prompt)
	}
	if !strings.Contains(sess.prompt, "add hello endpoint") {
		t.Fatalf("prompt=%q", sess.prompt)
	}
	if strings.Contains(sess.prompt, "{{") {
		t.Fatalf("prompt still has an unrendered placeholder:\n%s", sess.prompt)
	}
	if out["workspace"] != workerWorkspace {
		t.Fatalf("out workspace=%q, want the worker's own %q", out["workspace"], workerWorkspace)
	}
	if out["provider"] != sd.Provider || out["status"] != "ok" || out["agent_id"] != sess.id {
		t.Fatalf("out=%v", out)
	}
	if c.Name() != sd.Name || c.Domain() != sd.Domain || c.Provider() != sd.Provider {
		t.Fatalf("meta name=%s domain=%s provider=%s", c.Name(), c.Domain(), c.Provider())
	}
}

// TestCodeEditNeedsNoWorkspaceInput: the delegating agent does not have to supply
// a workspace at all — the worker's own is the one that counts.
func TestCodeEditNeedsNoWorkspaceInput(t *testing.T) {
	useRepoPrompt(t)
	sess := &mockSession{id: "agent-code_edit-3", workspace: "/sandbox/agent-10099/", summary: "done"}
	c := sd.CodeEdit{Agents: &mockBroker{sess: sess}}
	if _, err := c.Run(map[string]string{"instruction": "do it"}); err != nil {
		t.Fatalf("instruction-only run failed: %v", err)
	}
	if !strings.Contains(sess.prompt, "/sandbox/agent-10099/") {
		t.Fatalf("prompt=%q", sess.prompt)
	}
}

// TestCodeEditInjectsTheHostRuntimeContext: the worker prompt may name the
// runtime the worker is working for — World, Runtime Context, Completion
// Principles, Constraints, Constructs — and those values come from the host that
// acquired the session (broker.WorkerPromptContext), as of the delegating cycle.
// The worker's own workspace and goal stay this package's to fill.
func TestCodeEditInjectsTheHostRuntimeContext(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, filepath.FromSlash(sd.DefaultPromptRel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	tmpl := "ws={{WORKSPACE}}\ngoal={{GOAL}}\nworld={{WORLD}}\nrc={{RUNTIME_CONTEXT}}\n" +
		"principles={{COMPLETION_PRINCIPLES}}\nconstraints={{CONSTRAINTS}}\nconstructs={{CONSTRUCTS}}\n" +
		"unknown={{NOT_A_PLACEHOLDER}}\n"
	if err := os.WriteFile(path, []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROJECT_ROOT", root)

	const workerWorkspace = "/sandbox/agent-10099/"
	const someoneElsesWorkspace = "/sandbox/agent-10095/"
	sess := &mockWorkerSession{
		mockSession: mockSession{id: "agent-code_edit-9", workspace: workerWorkspace, summary: "done"},
		values: map[string]string{
			"{{WORLD}}":                 `{"assets":[{"id":"asset-1","state":"healthy"}]}`,
			"{{RUNTIME_CONTEXT}}":       `{"agent":{"name":"agent-code_edit-9","workspace":"` + workerWorkspace + `"},"task":{"id":"task-9"},"delegated_by":{"agent":"agent-10095"}}`,
			"{{COMPLETION_PRINCIPLES}}": "- Deliver a working implementation that satisfies the requested feature.",
			"{{CONSTRUCTS}}":            `[{"name":"code_edit"}]`,
			// An empty host value is a gap, not a blank section.
			"{{CONSTRAINTS}}": "",
			// The host does not get to decide what the worker's own workspace and
			// goal are.
			"{{WORKSPACE}}": someoneElsesWorkspace,
			"{{GOAL}}":      "someone else's goal",
		},
	}
	if _, err := (sd.CodeEdit{Agents: &mockBroker{sess: sess}}).Run(map[string]string{"instruction": "do it"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ws=" + workerWorkspace,
		"goal=do it",
		`"asset-1"`,
		`"name":"agent-code_edit-9"`,
		`"delegated_by"`,
		`"task-9"`,
		"- Deliver a working implementation that satisfies the requested feature.",
		`[{"name":"code_edit"}]`,
		// A placeholder outside the vocabulary stays visible rather than silently
		// dropping the line.
		"unknown={{NOT_A_PLACEHOLDER}}",
	} {
		if !strings.Contains(sess.prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, sess.prompt)
		}
	}
	for _, line := range []string{"constraints=(not provided by this runtime)"} {
		if !strings.Contains(sess.prompt, line) {
			t.Fatalf("prompt missing %q:\n%s", line, sess.prompt)
		}
	}
	if strings.Contains(sess.prompt, someoneElsesWorkspace) || strings.Contains(sess.prompt, "someone else's goal") {
		t.Fatalf("the host overrode the worker's own workspace/goal:\n%s", sess.prompt)
	}
}

// TestCodeEditPromptRendersWithoutAHostContext: a session whose host cannot
// describe a runtime context (here: a plain session) still produces a readable
// prompt — the shipped template's frame placeholders say they were not provided
// instead of reaching the worker as raw {{NAME}}.
func TestCodeEditPromptRendersWithoutAHostContext(t *testing.T) {
	useRepoPrompt(t)
	sess := &mockSession{id: "agent-code_edit-7", workspace: "/sandbox/agent-10099/", summary: "done"}
	if _, err := (sd.CodeEdit{Agents: &mockBroker{sess: sess}}).Run(map[string]string{"instruction": "do it"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sess.prompt, "{{") {
		t.Fatalf("prompt still has an unrendered placeholder:\n%s", sess.prompt)
	}
	for _, want := range []string{
		"## World",
		"## Runtime Context",
		"## Completion",
		"## Completion Principles",
		"## Constraints",
		"## Constructs",
	} {
		if !strings.Contains(sess.prompt, want) {
			t.Fatalf("shipped template missing %q:\n%s", want, sess.prompt)
		}
	}
	if n := strings.Count(sess.prompt, "(not provided by this runtime)"); n != 5 {
		t.Fatalf("prompt marks %d frame placeholders as not provided, want 5:\n%s", n, sess.prompt)
	}
}

// TestCodeEditPromptDelegatesAutonomy: the shipped template still hands the
// worker the objective and the autonomy to choose the implementation.
func TestCodeEditPromptDelegatesAutonomy(t *testing.T) {
	useRepoPrompt(t)
	sess := &mockSession{id: "agent-code_edit-2", workspace: "/tmp/ws/", summary: "done"}
	c := sd.CodeEdit{Agents: &mockBroker{sess: sess}}
	_, err := c.Run(map[string]string{"instruction": "add hello endpoint"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"autonomous software engineer",
		"Goal",
		"add hello endpoint",
		"understand the requirement",
	} {
		if !strings.Contains(sess.prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, sess.prompt)
		}
	}
	if strings.Contains(sess.prompt, "Make only necessary changes") {
		t.Fatalf("prompt still contains micromanaging instruction:\n%s", sess.prompt)
	}
	if strings.Contains(sess.prompt, "{{") {
		t.Fatalf("shipped template has an unrendered placeholder:\n%s", sess.prompt)
	}
}
