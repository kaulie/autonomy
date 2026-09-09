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
	sess, err := rt.AcquireAgent(context.Background(), broker.AcquireAgentOpts{
		Purpose:   "test",
		Workspace: t.TempDir(),
		Backend:   string(AgentBackendLocal),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := sess.ID()
	if id == "" || !strings.HasPrefix(id, "agent-") {
		t.Fatalf("id=%q", id)
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

func TestNewCursorClientIsSingleEntry(t *testing.T) {
	t.Parallel()
	// Smoke: constructor returns a client; real bridge not required.
	c := newCursorClient(t.TempDir())
	if c == nil {
		t.Fatal("nil client")
	}
	_ = c.Close()
}
