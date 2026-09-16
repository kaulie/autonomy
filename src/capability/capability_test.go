package capability_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability"
	"github.com/kaulie/autonomy/src/capability/broker"
	"github.com/kaulie/autonomy/src/capability/deployment"
)

type memAssets map[string]string

func (m memAssets) GetState(id string) (string, error) {
	s, ok := m[id]
	if !ok {
		return "", errNotFound(id)
	}
	return s, nil
}

func (m memAssets) SetState(id, state string) error {
	if _, ok := m[id]; !ok {
		return errNotFound(id)
	}
	m[id] = state
	return nil
}

type errNotFound string

func (e errNotFound) Error() string { return "asset " + string(e) + " not found" }

func TestRegisterDefaultsIncludesAssetChange(t *testing.T) {
	t.Parallel()
	f := capability.NewFactory()
	assets := memAssets{"1": "alive"}
	capability.RegisterDefaults(f, capability.Deps{Assets: assets})
	all := f.GetAll()
	if len(all) != 3 {
		t.Fatalf("GetAll len=%d want 3", len(all))
	}
	for _, name := range []string{"asset.change", "code_edit", deployment.Name} {
		if f.Get(name) == nil {
			t.Fatalf("capability %s not registered; GetAll=%v", name, all)
		}
	}
	got := f.FormatConstructs()
	for _, name := range []string{"asset.change", "code_edit", deployment.Name} {
		if !strings.Contains(got, `"name": "`+name+`"`) {
			t.Fatalf("expected %s in constructs: %q", name, got)
		}
	}
	out, err := f.Get("asset.change").Run(map[string]string{"target": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if out["state"] != "changed" {
		t.Fatalf("out=%v", out)
	}
}

// TestRegisterDefaultsWiresTheDeploymentMonitor: the host's hooks reach the
// monitor — the agent broker (which makes the observation agent-backed) and a
// pinned deployment source (a deterministic one, or a test double).
func TestRegisterDefaultsWiresTheDeploymentMonitor(t *testing.T) {
	t.Parallel()
	obs := &stubObserver{}
	broker := &stubBroker{}
	f := capability.NewFactory()
	capability.RegisterDefaults(f, capability.Deps{Deployments: obs, Agents: broker})
	mon, ok := f.Get(deployment.Name).(deployment.Monitor)
	if !ok {
		t.Fatalf("registered %s with type %T", deployment.Name, f.Get(deployment.Name))
	}
	if mon.Observer != obs {
		t.Fatalf("monitor observer = %#v, want the injected one", mon.Observer)
	}
	if mon.Agents != broker {
		t.Fatalf("monitor agents = %#v, want the injected broker", mon.Agents)
	}
}

type stubObserver struct{}

func (stubObserver) Observe(context.Context, deployment.Request) (deployment.Snapshot, error) {
	return deployment.Snapshot{State: deployment.StateSucceeded}, nil
}

type stubBroker struct{}

func (stubBroker) AcquireAgent(context.Context, broker.AcquireAgentOpts) (broker.AgentSession, error) {
	return nil, nil
}
