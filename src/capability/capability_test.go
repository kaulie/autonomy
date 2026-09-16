package capability_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability"
	"github.com/kaulie/autonomy/src/capability/broker"
	"github.com/kaulie/autonomy/src/capability/deployment"
	"github.com/kaulie/autonomy/src/capability/spec"
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

func TestRegisterDefaultsIncludesBuiltins(t *testing.T) {
	t.Parallel()
	f := capability.NewFactory()
	assets := memAssets{"1": "alive"}
	capability.RegisterDefaults(f, capability.Deps{Assets: assets})
	all := f.GetAll()
	if len(all) != 5 {
		t.Fatalf("GetAll len=%d want 5", len(all))
	}
	for _, name := range []string{"asset.change", "code_edit", "service.deploy", "pull_request.review", deployment.Name} {
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
	// service.deploy is planner-visible like any other construct: the planner
	// learns it can trigger a deployment pipeline from the same list.
	if !strings.Contains(got, `"name": "service.deploy"`) {
		t.Fatalf("expected service.deploy in constructs: %q", got)
	}
	// Same for landing a pull request: the planner sees the capability that
	// merges a branch pair, so it does not have to invent a git command for it.
	if !strings.Contains(got, `"name": "pull_request.review"`) {
		t.Fatalf("expected pull_request.review in constructs: %q", got)
	}
	out, err := f.Get("asset.change").Run(map[string]string{"target": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if out["state"] != "changed" {
		t.Fatalf("out=%v", out)
	}
	// The asset it changed is the step's own input; reporting it back would be an
	// echo, not an output (see the output rule in docs/execution-step.md).
	if _, ok := out["target"]; ok {
		t.Fatalf("out=%v, want no target echoed back", out)
	}
}

// TestBuiltinsDeclareTheirSignature: every built-in capability says what it takes
// and what it returns, so {{CONSTRUCTS}} carries a callable contract instead of
// prose a planner has to parse.
func TestBuiltinsDeclareTheirSignature(t *testing.T) {
	t.Parallel()
	f := capability.NewFactory()
	capability.RegisterDefaults(f, capability.Deps{Assets: memAssets{"1": "alive"}})
	for _, c := range f.GetAll() {
		declared, ok := c.(spec.Declared)
		if !ok {
			t.Errorf("%s declares no inputs/outputs", c.Name())
			continue
		}
		inputs, outputs := declared.Inputs(), declared.Outputs()
		if len(inputs) == 0 || len(outputs) == 0 {
			t.Errorf("%s declares %d input(s) / %d output(s), want at least one of each", c.Name(), len(inputs), len(outputs))
		}
		for _, fld := range append(append([]spec.Field{}, inputs...), outputs...) {
			if strings.TrimSpace(fld.Name) == "" || strings.TrimSpace(fld.Description) == "" {
				t.Errorf("%s declares an unnamed or undescribed field: %+v", c.Name(), fld)
			}
		}
	}
}

// TestConstructsCarryInputsAndOutputs pins what the runtime injects as
// {{CONSTRUCTS}}: each capability arrives with the fields a caller fills and the
// keys it gets back, so a plan step can be written from the list alone.
func TestConstructsCarryInputsAndOutputs(t *testing.T) {
	t.Parallel()
	f := capability.NewFactory()
	capability.RegisterDefaults(f, capability.Deps{Assets: memAssets{"1": "alive"}})

	var constructs []struct {
		Name   string       `json:"name"`
		Input  []spec.Field `json:"input"`
		Output []spec.Field `json:"output"`
	}
	if err := json.Unmarshal([]byte(f.FormatConstructs()), &constructs); err != nil {
		t.Fatalf("constructs are not the documented JSON: %v", err)
	}
	if len(constructs) != 5 {
		t.Fatalf("constructs=%d want the 5 built-ins", len(constructs))
	}
	inputs := map[string][]spec.Field{}
	outputs := map[string][]spec.Field{}
	for _, c := range constructs {
		if len(c.Input) == 0 || len(c.Output) == 0 {
			t.Errorf("%s arrives with %d input(s) / %d output(s)", c.Name, len(c.Input), len(c.Output))
		}
		inputs[c.Name], outputs[c.Name] = c.Input, c.Output
	}

	// Where a plan step goes wrong when the declaration is missing: the field
	// that names the thing to act on, and the key the next step reads back.
	for _, tc := range []struct {
		capability string
		input      string
		alias      string
		required   bool
		output     string
	}{
		{"asset.change", "target", "", true, "state"},
		{"code_edit", "instruction", "goal", true, "summary"},
		{"service.deploy", "service", "service_id", true, "pipeline_id"},
		// task-15's shape: name the pull request by its url, read the merge sha.
		{"pull_request.review", "pr", "pr_url", false, "sha"},
		// A deployment is followed by its id or by the poll path service.deploy
		// hands back, and signals is where a problem shows up.
		{"deployment.monitor", "deployment", "pipeline_id", false, "signals"},
	} {
		field, ok := fieldNamed(inputs[tc.capability], tc.input)
		if !ok {
			t.Errorf("%s carries no %q input: %+v", tc.capability, tc.input, inputs[tc.capability])
			continue
		}
		if strings.TrimSpace(field.Description) == "" {
			t.Errorf("%s declares %q with no description", tc.capability, tc.input)
		}
		if field.Required != tc.required {
			t.Errorf("%s %q required=%t want %t", tc.capability, tc.input, field.Required, tc.required)
		}
		if tc.alias != "" && !containsString(field.Aliases, tc.alias) {
			t.Errorf("%s %q aliases=%v want %q among them", tc.capability, tc.input, field.Aliases, tc.alias)
		}
		if _, ok := fieldNamed(outputs[tc.capability], tc.output); !ok {
			t.Errorf("%s returns no %q: %+v", tc.capability, tc.output, outputs[tc.capability])
		}
	}

	// service.deploy's poll path is what the monitor takes next: the two have to
	// agree, or a plan cannot chain them.
	poll, ok := fieldNamed(outputs["service.deploy"], "poll")
	if !ok || strings.TrimSpace(poll.Description) == "" {
		t.Errorf("service.deploy does not declare its poll output: %+v", outputs["service.deploy"])
	}
	if _, ok := fieldNamed(inputs["deployment.monitor"], "poll"); !ok {
		t.Errorf("deployment.monitor does not declare the poll input: %+v", inputs["deployment.monitor"])
	}
}

// TestConstructsAreReadableJSON: the descriptions name placeholders (<id>,
// <head/topic branch>), and a planner should read them as written. HTML escaping
// is the same JSON — but \u003cid\u003e is not the same prompt.
func TestConstructsAreReadableJSON(t *testing.T) {
	t.Parallel()
	f := capability.NewFactory()
	capability.RegisterDefaults(f, capability.Deps{Assets: memAssets{"1": "alive"}})
	got := f.FormatConstructs()
	if strings.Contains(got, `\u003c`) || strings.Contains(got, `\u003e`) {
		t.Errorf("constructs HTML-escape their placeholders:\n%s", got)
	}
	if !strings.Contains(got, "<id>") {
		t.Errorf("constructs no longer show what a caller passes:\n%s", got)
	}
}

// TestConstructsDoNotNameProviders: the planner is told what can be done, never
// who does it. A plan step is a capability plus its input, so a top-level provider
// in the list would be an axis the planner cannot put in a step — and one it must
// not schedule on. The runtime keeps that attribution on the step it recorded
// (execution_step.provider), where it is an audit fact instead of an instruction.
//
// A capability may still declare a *data* field called provider among its own
// input/output (code_edit reports the backend that ran the worker): that is a
// value it returns, not a choice offered to the plan.
func TestConstructsDoNotNameProviders(t *testing.T) {
	t.Parallel()
	f := capability.NewFactory()
	capability.RegisterDefaults(f, capability.Deps{Assets: memAssets{"1": "alive"}})

	var constructs []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(f.FormatConstructs()), &constructs); err != nil {
		t.Fatalf("constructs are not JSON: %v", err)
	}
	if len(constructs) == 0 {
		t.Fatal("no constructs")
	}
	for _, c := range constructs {
		if _, ok := c["provider"]; ok {
			t.Errorf("construct %s carries a provider the planner cannot use", string(c["name"]))
		}
	}
	// The capability still has one: it is the runtime's own record (providerOf),
	// not something the prompt carries.
	for _, c := range f.GetAll() {
		if c.Provider() == "" {
			t.Errorf("%s declares no provider, so its step row would say nothing", c.Name())
		}
	}
}

// TestConstructsWithoutASignatureStillRender: declaring inputs/outputs is
// optional — a capability that only has a description is still a construct, and
// it renders without invented fields.
func TestConstructsWithoutASignatureStillRender(t *testing.T) {
	t.Parallel()
	f := capability.NewFactory()
	f.Register(bareCapability{})
	got := f.FormatConstructs()
	if !strings.Contains(got, `"name": "bare"`) || !strings.Contains(got, `"description": "does one thing"`) {
		t.Fatalf("constructs=%q, want the described capability", got)
	}
	if strings.Contains(got, `"input"`) || strings.Contains(got, `"output"`) {
		t.Fatalf("constructs=%q, want no input/output for a capability that declares none", got)
	}
}

type bareCapability struct{}

func (bareCapability) Name() string        { return "bare" }
func (bareCapability) Domain() string      { return "test" }
func (bareCapability) Provider() string    { return "test" }
func (bareCapability) Description() string { return "does one thing" }
func (bareCapability) Run(map[string]string) (map[string]string, error) {
	return nil, nil
}

// fieldNamed finds a declared field by its canonical name.
func fieldNamed(fields []spec.Field, name string) (spec.Field, bool) {
	for _, f := range fields {
		if f.Name == name {
			return f, true
		}
	}
	return spec.Field{}, false
}

func containsString(vals []string, want string) bool {
	for _, v := range vals {
		if v == want {
			return true
		}
	}
	return false
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
