package autonomy

import (
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability/spec"
)

// builder and deployer are the two sides of a lineage: one declares an output, the
// other a required input, and their field names need not agree — the plan is what
// says "build's artifact_version is deploy's version".
func builder() *declaredCapability {
	return &declaredCapability{
		fakeCapability: fakeCapability{name: "build", out: map[string]string{"artifact_version": "v1.2.3", "service": "acp"}},
		outputs:        []spec.Field{{Name: "artifact_version"}, {Name: "service"}},
	}
}

func deployer() *declaredCapability {
	return &declaredCapability{
		fakeCapability: fakeCapability{name: "deploy", out: map[string]string{"pipeline_id": "pipe-1"}},
		inputs:         []spec.Field{{Name: "version", Required: true}, {Name: "target_service"}},
		outputs:        []spec.Field{{Name: "pipeline_id"}},
	}
}

func TestPlanLineageAcceptsWhatItCanResolve(t *testing.T) {
	withFakeCapability(t, builder(), deployer())

	good := []Action{
		CapabilityAction{Name: "build", StepName: "build", Inputs: map[string]StepInput{}},
		CapabilityAction{Name: "deploy", StepName: "deploy", Inputs: map[string]StepInput{
			"version":        BoundInput("step:build.output.artifact_version"),
			"target_service": BoundInput("world_model:asset.repo.state"),
		}},
	}
	if err := validatePlanLineage(good); err != nil {
		t.Fatalf("a plan whose lineage lines up was refused: %v", err)
	}

	// A literal is its own source, and an unnamed step is fine as long as nothing
	// binds to it.
	literals := []Action{
		CapabilityAction{Name: "deploy", Inputs: map[string]StepInput{"version": LiteralInput("v1.0.0")}},
	}
	if err := validatePlanLineage(literals); err != nil {
		t.Fatalf("a plan of literals was refused: %v", err)
	}
}

func TestPlanLineageRefusesAPlanItCannotRead(t *testing.T) {
	withFakeCapability(t, builder(), deployer())

	cases := []struct {
		name string
		plan []Action
		want string
	}{
		{
			name: "a source that is neither form",
			plan: []Action{CapabilityAction{Name: "deploy", StepName: "deploy", Inputs: map[string]StepInput{
				"version": BoundInput("the version of the build step"),
			}}},
			want: "cannot read an input source",
		},
		{
			name: "a step no plan named",
			plan: []Action{CapabilityAction{Name: "deploy", StepName: "deploy", Inputs: map[string]StepInput{
				"version": BoundInput("step:nowhere.output.artifact_version"),
			}}},
			want: `no step called "nowhere"`,
		},
		{
			name: "a step that runs later",
			plan: []Action{
				CapabilityAction{Name: "deploy", StepName: "deploy", Inputs: map[string]StepInput{
					"version": BoundInput("step:build.output.artifact_version"),
				}},
				CapabilityAction{Name: "build", StepName: "build", Inputs: map[string]StepInput{}},
			},
			want: "can only come from a step that runs before it",
		},
		{
			name: "a step that reads itself",
			plan: []Action{CapabilityAction{Name: "deploy", StepName: "deploy", Inputs: map[string]StepInput{
				"version": BoundInput("step:deploy.output.pipeline_id"),
			}}},
			want: "can only come from a step that runs before it",
		},
		{
			name: "an output the source capability does not report",
			plan: []Action{
				CapabilityAction{Name: "build", StepName: "build", Inputs: map[string]StepInput{}},
				CapabilityAction{Name: "deploy", StepName: "deploy", Inputs: map[string]StepInput{
					"version": BoundInput("step:build.output.commit_sha"),
				}},
			},
			want: `does not report "commit_sha" (it reports artifact_version, service)`,
		},
		{
			name: "an input the capability does not declare",
			plan: []Action{CapabilityAction{Name: "deploy", StepName: "deploy", Inputs: map[string]StepInput{
				"versoin": LiteralInput("v1.0.0"),
			}}},
			want: `passes "versoin", which the capability does not declare`,
		},
		{
			name: "a required input nothing supplies",
			plan: []Action{CapabilityAction{Name: "deploy", StepName: "deploy", Inputs: map[string]StepInput{}}},
			want: `does not supply "version", which the capability requires`,
		},
		{
			name: "two steps with one name",
			plan: []Action{
				CapabilityAction{Name: "build", StepName: "build"},
				CapabilityAction{Name: "build", StepName: "build"},
			},
			want: `two steps are called "build"`,
		},
		{
			name: "a name a binding cannot address",
			plan: []Action{CapabilityAction{Name: "build", StepName: "build.v1"}},
			want: "not a usable step name",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePlanLineage(tc.plan)
			if err == nil {
				t.Fatal("the plan was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestResolveStepInputs pins what a binding resolves to, and that a source that
// cannot deliver a value says so here — where the step that needed it is known —
// instead of failing somewhere further away.
func TestResolveStepInputs(t *testing.T) {
	_world = &World{assetManager: &AssetManager{
		assets:     []Asset{{ID: "repo", Kind: "git", State: "dirty"}},
		assetsByID: map[string]Asset{"repo": {ID: "repo", Kind: "git", State: "dirty"}},
	}}
	t.Cleanup(func() { _world = nil })

	prior := []ActionResult{{StepName: "build", Capability: "build", Output: map[string]string{"artifact_version": "v1.2.3"}}}
	got, err := resolveStepInputs(map[string]StepInput{
		"version": BoundInput("step:build.output.artifact_version"),
		"method":  LiteralInput("squash"),
		"target":  BoundInput("world_model:asset.repo.state"),
	}, prior)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := map[string]string{"version": "v1.2.3", "method": "squash", "target": "dirty"}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("input %q=%q, want %q (all: %v)", key, got[key], value, got)
		}
	}

	cases := []struct {
		name  string
		prior []ActionResult
		in    map[string]StepInput
		want  string
	}{
		{
			name:  "a key the step did not report",
			prior: prior,
			in:    map[string]StepInput{"v": BoundInput("step:build.output.commit_sha")},
			want:  `did not report "commit_sha"`,
		},
		{
			name:  "a key the step reported empty",
			prior: []ActionResult{{StepName: "build", Output: map[string]string{"pr_url": ""}}},
			in:    map[string]StepInput{"pr": BoundInput("step:build.output.pr_url")},
			want:  `reported an empty "pr_url"`,
		},
		{
			name:  "a step that produced nothing",
			prior: prior,
			in:    map[string]StepInput{"v": BoundInput("step:later.output.artifact_version")},
			want:  `step "later" has produced nothing in this plan`,
		},
		{
			name:  "an asset the world does not have",
			prior: prior,
			in:    map[string]StepInput{"t": BoundInput("world_model:asset.ghost.state")},
			want:  "asset ghost not found",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveStepInputs(tc.in, tc.prior)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want it to mention %q", err, tc.want)
			}
		})
	}

	// A runtime with no World Model cannot read one, and says so rather than
	// handing the step an empty value.
	_world = nil
	if _, err := resolveStepInputs(map[string]StepInput{"t": BoundInput("world_model:asset.repo.state")}, prior); err == nil || !strings.Contains(err.Error(), "no World Model") {
		t.Fatalf("err=%v, want the missing World Model reported", err)
	}
}

// TestAPlanStepReadsWhatAnEarlierStepProduced is the whole point, end to end: two
// capabilities whose field names disagree, wired by the plan, with the plan row
// keeping the binding and the step row keeping the value that arrived.
func TestAPlanStepReadsWhatAnEarlierStepProduced(t *testing.T) {
	store := executionTestStore(t)
	build, deploy := builder(), deployer()
	withFakeCapability(t, build, deploy)

	result, err := NewRuntime(NewAgentFactory()).Execute(Decision{
		Type: "plan",
		Ctx:  executionContext(),
		Actions: []Action{
			CapabilityAction{Name: "build", StepName: "build", Inputs: map[string]StepInput{}, ExpectedEffect: "the artifact is built"},
			CapabilityAction{Name: "deploy", StepName: "deploy", Inputs: map[string]StepInput{
				"version": BoundInput("step:build.output.artifact_version"),
			}, ExpectedEffect: "the artifact's version is deployed"},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if deploy.in["version"] != "v1.2.3" {
		t.Fatalf("the deployer was called with %v, want build's artifact_version", deploy.in)
	}
	if len(result.Actions) != 2 || result.Actions[1].StepName != "deploy" || result.Actions[1].Input["version"] != "v1.2.3" {
		t.Fatalf("records=%+v, want the resolved input on the second step", result.Actions)
	}

	plans, err := store.ListExecutionPlans("task-exec")
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans=%+v err=%v", plans, err)
	}
	planned, err := store.ListExecutionStepPlan(plans[0].ID)
	if err != nil || len(planned) != 2 {
		t.Fatalf("planned=%+v err=%v", planned, err)
	}
	// The plan row keeps what the planner wrote: the binding itself, and the name a
	// binding addresses (otherwise the lineage in a stored plan points at no row).
	if planned[1].Name != "deploy" || planned[0].Name != "build" {
		t.Errorf("plan step names=%q/%q, want build/deploy", planned[0].Name, planned[1].Name)
	}
	if !strings.Contains(planned[1].Input, `"source":"step:build.output.artifact_version"`) {
		t.Errorf("plan step input=%s, want the binding the planner wrote", planned[1].Input)
	}
	// The step row keeps what the capability was actually called with: the value.
	executed, err := store.ListExecutionSteps(plans[0].ID)
	if err != nil || len(executed) != 2 {
		t.Fatalf("executed=%+v err=%v", executed, err)
	}
	if executed[1].Name != "deploy" || executed[1].Input != `{"version":"v1.2.3"}` {
		t.Errorf("execution step=%+v, want the name and the resolved value", executed[1])
	}
	if executed[0].Input != "{}" {
		t.Errorf("first step input=%s, want {} for a step that bound nothing", executed[0].Input)
	}
}

// TestAPlanWhoseLineageDoesNotLineUpDoesNotRun: the refusal happens before the plan
// is written, so nothing is executed against a value that was never going to arrive
// and there are no rows pretending a step ran.
func TestAPlanWhoseLineageDoesNotLineUpDoesNotRun(t *testing.T) {
	store := executionTestStore(t)
	build, deploy := builder(), deployer()
	withFakeCapability(t, build, deploy)

	result, err := NewRuntime(NewAgentFactory()).Execute(Decision{
		Type: "plan",
		Ctx:  executionContext(),
		Actions: []Action{
			CapabilityAction{Name: "build", StepName: "build", Inputs: map[string]StepInput{}, ExpectedEffect: "the artifact is built"},
			CapabilityAction{Name: "deploy", StepName: "deploy", Inputs: map[string]StepInput{
				"version": BoundInput("step:build.output.commit_sha"),
			}, ExpectedEffect: "the artifact's version is deployed"},
		},
	})
	if err == nil {
		t.Fatal("a plan reading an output nobody declares was executed")
	}
	if !strings.Contains(result.Message, "does not report") {
		t.Fatalf("result.Message=%q, want the lineage refusal", result.Message)
	}
	plans, err := store.ListExecutionPlans("task-exec")
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 0 {
		t.Fatalf("plans=%+v, want none: a plan that cannot run is not written", plans)
	}
}
