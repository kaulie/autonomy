package autonomy

import (
	"context"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability/broker"
)

func TestRuntimeAcquireLocalAgentRegistersInFactory(t *testing.T) {
	t.Parallel()
	f := NewAgentFactory()
	rt := NewRuntime(f)
	requested := t.TempDir()
	sess, err := rt.AcquireAgent(context.Background(), broker.AcquireAgentOpts{
		Purpose:   "test",
		Workspace: requested,
		Backend:   string(AgentBackendLocal),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := sess.ID()
	if id == "" || !strings.HasPrefix(id, "agent-") {
		t.Fatalf("id=%q", id)
	}
	if got := sess.Workspace(); got != requested {
		t.Fatalf("session workspace=%q, want the requested %q", got, requested)
	}
	if f.Get(id) == nil {
		t.Fatal("agent not registered in factory")
	}
	if _, err := sess.Prompt(context.Background(), "hi"); err == nil {
		t.Fatal("expected prompt unsupported for local backend")
	}
	if err := sess.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.Get(id) != nil {
		t.Fatal("ephemeral agent still in factory after Release")
	}
}

// TestRuntimeAcquireAgentWithoutWorkspaceUsesItsOwn pins what a delegating
// capability relies on: acquire without a workspace and the agent runs in its own
// AGENT_WORKSPACE, which the session reports back.
func TestRuntimeAcquireAgentWithoutWorkspaceUsesItsOwn(t *testing.T) {
	t.Parallel()
	f := NewAgentFactory()
	rt := NewRuntime(f)
	sess, err := rt.AcquireAgent(context.Background(), broker.AcquireAgentOpts{
		Purpose: "code_edit",
		Backend: string(AgentBackendLocal),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Release(context.Background()) }()
	ws := sess.Workspace()
	if ws == "" || !strings.Contains(ws, sess.ID()) {
		t.Fatalf("session workspace=%q, want the agent's own sandbox for %q", ws, sess.ID())
	}
	if want := AgentWorkspacePath(sess.ID()); ws != want {
		t.Fatalf("session workspace=%q, want %q", ws, want)
	}
}

func TestNewCursorClientIsSingleEntry(t *testing.T) {
	t.Parallel()
	// Smoke: constructor returns a client; real bridge not required.
	c := newCursorClient(t.TempDir())
	if c == nil {
		t.Fatal("nil client")
	}
	_ = c.Close()
}
