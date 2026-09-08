package software_development_test

import (
	"context"
	"strings"
	"testing"

	sd "github.com/kaulie/autonomy/src/capability/software_development"
)

type mockRunner struct {
	lastWS, lastInst string
	summary          string
	err              error
}

func (m *mockRunner) RunEdit(_ context.Context, workspace, instruction string) (string, error) {
	m.lastWS = workspace
	m.lastInst = instruction
	return m.summary, m.err
}

func TestCodeEditRequiresInstruction(t *testing.T) {
	t.Parallel()
	c := sd.CodeEdit{Runner: &mockRunner{summary: "ok"}}
	_, err := c.Run(map[string]string{"workspace": "/tmp/ws"})
	if err == nil || !strings.Contains(err.Error(), "instruction") {
		t.Fatalf("err=%v", err)
	}
}

func TestCodeEditUsesWorkspaceAndRunner(t *testing.T) {
	t.Parallel()
	m := &mockRunner{summary: "edited files"}
	c := sd.CodeEdit{Runner: m}
	out, err := c.Run(map[string]string{
		"workspace":   "/Users/gaolei/agent-workspace-sandbox/agent-1/",
		"instruction": "add hello endpoint",
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.lastWS == "" || m.lastInst == "" {
		t.Fatalf("runner not called: %+v", m)
	}
	if out["provider"] != sd.Provider || out["status"] != "ok" {
		t.Fatalf("out=%v", out)
	}
	if c.Name() != sd.Name || c.Domain() != sd.Domain || c.Provider() != sd.Provider {
		t.Fatalf("meta name=%s domain=%s provider=%s", c.Name(), c.Domain(), c.Provider())
	}
}
