package software_development_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability/broker"
	sd "github.com/kaulie/autonomy/src/capability/software_development"
)

type mockSession struct {
	id      string
	summary string
	err     error
	prompt  string
}

func (m *mockSession) ID() string { return m.id }

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
	_, err := c.Run(map[string]string{"workspace": "/tmp/ws"})
	if err == nil || !strings.Contains(err.Error(), "instruction") {
		t.Fatalf("err=%v", err)
	}
}

func TestCodeEditUsesWorkspaceAndBroker(t *testing.T) {
	t.Parallel()
	sess := &mockSession{id: "agent-code_edit-1", summary: "edited files"}
	m := &mockBroker{sess: sess}
	c := sd.CodeEdit{Agents: m}
	out, err := c.Run(map[string]string{
		"workspace":   "/Users/gaolei/agent-workspace-sandbox/agent-1/",
		"instruction": "add hello endpoint",
		"task_id":     "task-9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.lastOpts.Workspace == "" || m.lastOpts.Purpose != sd.Name || m.lastOpts.TaskID != "task-9" {
		t.Fatalf("acquire opts: %+v", m.lastOpts)
	}
	if !strings.Contains(sess.prompt, "add hello endpoint") {
		t.Fatalf("prompt=%q", sess.prompt)
	}
	if out["provider"] != sd.Provider || out["status"] != "ok" || out["agent_id"] != sess.id {
		t.Fatalf("out=%v", out)
	}
	if c.Name() != sd.Name || c.Domain() != sd.Domain || c.Provider() != sd.Provider {
		t.Fatalf("meta name=%s domain=%s provider=%s", c.Name(), c.Domain(), c.Provider())
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
