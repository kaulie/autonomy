package autonomy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability"
)

func TestAgentWorkspacePath(t *testing.T) {
	t.Parallel()
	got := AgentWorkspacePath("agent-1")
	wantPrefix := filepath.Join(DefaultAgentWorkspaceRoot, "agent-1")
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("got %q want prefix %q", got, wantPrefix)
	}
	if !strings.HasSuffix(got, string(os.PathSeparator)) {
		t.Fatalf("expected trailing separator: %q", got)
	}
}

func TestNewAgentSetsWorkspace(t *testing.T) {
	t.Parallel()
	f := NewAgentFactory()
	a := f.NewAgent()
	if a.Workspace == "" {
		t.Fatal("empty Workspace")
	}
	if a.Name == "" || !strings.Contains(a.Workspace, a.Name) {
		t.Fatalf("Workspace=%q must contain agent name %q", a.Workspace, a.Name)
	}
}

func TestParseDecisionRegisteredCapability(t *testing.T) {
	capF := capability.NewFactory()
	capability.RegisterDefaults(capF, capability.Deps{})
	prev := _autonomy
	_autonomy = &Autonomy{CapabilityFactory: capF}
	t.Cleanup(func() { _autonomy = prev })

	decision, err := parseDecision(`{"type":"plan","reason":"edit","plan":[{"capability":"code_edit","input":{"instruction":"add feature"}}],"need":{}}`)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Type != "plan" || decision.Reason != "edit" {
		t.Fatalf("decision=%+v", decision)
	}
	if len(decision.Actions) != 1 {
		t.Fatalf("actions=%d, want 1: %+v", len(decision.Actions), decision.Actions)
	}
	ca, ok := decision.Actions[0].(CapabilityAction)
	if !ok {
		t.Fatalf("action type %T", decision.Actions[0])
	}
	if ca.Name != "code_edit" || ca.Input["instruction"] != "add feature" {
		t.Fatalf("action=%+v", ca)
	}
}
