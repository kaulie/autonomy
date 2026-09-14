package software_development_test

import (
	"context"
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

func TestCodeEditRequiresInstruction(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	sess := &mockSession{id: "agent-code_edit-3", workspace: "/sandbox/agent-10099/", summary: "done"}
	c := sd.CodeEdit{Agents: &mockBroker{sess: sess}}
	if _, err := c.Run(map[string]string{"instruction": "do it"}); err != nil {
		t.Fatalf("instruction-only run failed: %v", err)
	}
	if !strings.Contains(sess.prompt, "/sandbox/agent-10099/") {
		t.Fatalf("prompt=%q", sess.prompt)
	}
}

func TestCodeEditPromptDelegatesAutonomy(t *testing.T) {
	t.Parallel()
	sess := &mockSession{id: "agent-code_edit-2", summary: "done"}
	c := sd.CodeEdit{Agents: &mockBroker{sess: sess}}
	_, err := c.Run(map[string]string{
		"workspace":   "/tmp/ws",
		"instruction": "add hello endpoint",
	})
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
}
