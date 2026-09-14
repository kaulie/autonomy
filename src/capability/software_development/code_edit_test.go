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
	sess     *mockSession
	err      error
}

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
