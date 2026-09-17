package autonomy

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kaulie/autonomy/src/capability"
	"github.com/kaulie/autonomy/src/capability/spec"
)

// Verification: the facts a task's Completion Contract names, judged against the world
// by the authoritative sources — not by what the planner claims, and not by what the
// step that did the work reported about itself (docs/verification.md).

// fakeVerificationReader is an authoritative reader a test can register: what it takes,
// what it reports, and what it was last asked.
type fakeVerificationReader struct {
	name    string
	inputs  []spec.Field
	outputs []spec.Field
	answer  map[string]string
	err     error
	asked   map[string]string
}

func (r *fakeVerificationReader) Name() string          { return r.name }
func (r *fakeVerificationReader) Domain() string        { return "test" }
func (r *fakeVerificationReader) Provider() string      { return "autonomy" }
func (r *fakeVerificationReader) Description() string   { return "an authoritative reader" }
func (r *fakeVerificationReader) Inputs() []spec.Field  { return r.inputs }
func (r *fakeVerificationReader) Outputs() []spec.Field { return r.outputs }
func (r *fakeVerificationReader) Run(in map[string]string) (map[string]string, error) {
	r.asked = in
	if r.err != nil {
		return nil, r.err
	}
	return r.answer, nil
}

// verificationWorld puts one asset in the World Model the verifier reads.
func verificationWorld(t *testing.T, asset Asset) {
	t.Helper()
	prev := _world
	_world = &World{assetManager: &AssetManager{
		assets:     []Asset{asset},
		assetsByID: map[string]Asset{asset.ID: asset},
	}}
	t.Cleanup(func() { _world = prev })
}

// pinCriterion pins one criterion for a task, the way a run's first answer would.
func pinCriterion(t *testing.T, taskID, criterion string) {
	t.Helper()
	parsed, err := parseCompletionContract(json.RawMessage(`{"steps":[` + criterion + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	pinCompletionContract(Decision{Contract: parsed, Ctx: DecisionContext{Task: &Task{ID: taskID}}}, 0)
}

// lastVerdicts reads the verdicts a task's contract produced, oldest first.
func lastVerdicts(t *testing.T, store *SQLiteStore, taskID string) []Verification {
	t.Helper()
	verdicts, err := store.ListVerifications(taskID)
	if err != nil {
		t.Fatal(err)
	}
	return verdicts
}

// TestVerifyDoneAsksTheAuthoritativeSourceForStepEvidence: a step's own report is the
// evidence slot's *reference*, not the truth. The verdict comes from the capability that
// owns that kind of object — the reader the runtime knows for the evidence's producer, or
// the one the criterion names — and how it answered is recorded next to the reference.
func TestVerifyDoneAsksTheAuthoritativeSourceForStepEvidence(t *testing.T) {
	const criterion = `{"name":"C2","requirement":"the deployment completed",` +
		`"evidence":{"source":"step:deploy.output.pipeline_id"},` +
		`"expect":{"field":"state","equals":"succeeded"}}`

	cases := []struct {
		name       string
		producer   string
		reader     *fakeVerificationReader
		wantResult string
		wantMethod string
		wantAsked  string
	}{
		{
			name:     "the reader the runtime knows for a pipeline",
			producer: "service.deploy",
			reader: &fakeVerificationReader{
				name:    "deployment.monitor",
				inputs:  []spec.Field{{Name: "deployment", Required: true}},
				outputs: []spec.Field{{Name: "state"}, {Name: "healthy"}},
				answer:  map[string]string{"state": "succeeded"},
			},
			wantResult: verificationPass,
			wantMethod: "registry:deployment.monitor",
			wantAsked:  "pipe-9",
		},
		{
			name:     "the source answered, and the answer is not the fact",
			producer: "service.deploy",
			reader: &fakeVerificationReader{
				name:    "deployment.monitor",
				inputs:  []spec.Field{{Name: "deployment", Required: true}},
				outputs: []spec.Field{{Name: "state"}},
				answer:  map[string]string{"state": "failed"},
			},
			wantResult: verificationFail,
			wantMethod: "registry:deployment.monitor",
			wantAsked:  "pipe-9",
		},
		{
			name:     "the source could not answer",
			producer: "service.deploy",
			reader: &fakeVerificationReader{
				name:    "deployment.monitor",
				inputs:  []spec.Field{{Name: "deployment", Required: true}},
				outputs: []spec.Field{{Name: "state"}},
				err:     errVerificationTestReader,
			},
			wantResult: verificationInconclusive,
			wantMethod: "registry:deployment.monitor",
		},
		{
			name:       "nothing authoritative answers about this evidence",
			producer:   "code_edit",
			wantResult: verificationInconclusive,
			wantMethod: verificationMethodNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := executionTestStore(t)
			prevAuto := _autonomy
			factory := capability.NewFactory()
			if tc.reader != nil {
				factory.Register(tc.reader)
			}
			_autonomy = &Autonomy{CapabilityFactory: factory}
			t.Cleanup(func() { _autonomy = prevAuto })

			task := &Task{ID: "task-step"}
			planID, err := store.CreateExecutionPlan(ExecutionPlan{TaskID: task.ID, DecisionType: "plan", Cycle: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendExecutionStep(ExecutionStep{
				PlanID: planID, TaskID: task.ID, Cycle: 1, Idx: 1, Name: "deploy",
				Capability: tc.producer, Status: "ok",
				Output: `{"pipeline_id":"pipe-9","state":"queued"}`,
			}); err != nil {
				t.Fatal(err)
			}
			pinCriterion(t, task.ID, criterion)

			rt := NewRuntime(NewAgentFactory(), factory.GetAll()...)
			runErr := rt.verifyDone(DecisionContext{Task: task, Cycle: 2}, planID)

			verdicts := lastVerdicts(t, store, task.ID)
			if len(verdicts) != 1 {
				t.Fatalf("verdicts=%d, want one", len(verdicts))
			}
			verdict := verdicts[0]
			if verdict.Result != tc.wantResult {
				t.Fatalf("result=%q (%s), want %q", verdict.Result, verdict.Reason, tc.wantResult)
			}
			if verdict.Method != tc.wantMethod {
				t.Fatalf("method=%q, want %q", verdict.Method, tc.wantMethod)
			}
			if !strings.Contains(verdict.Evidence, "pipe-9") {
				t.Fatalf("evidence=%s, want the reference the slot resolved to", verdict.Evidence)
			}
			if tc.wantAsked != "" && tc.reader.asked["deployment"] != tc.wantAsked {
				t.Fatalf("the reader was asked with %v, want the evidence object in it", tc.reader.asked)
			}
			if tc.wantResult == verificationPass && runErr != nil {
				t.Fatalf("err=%v, want the claim to hold up", runErr)
			}
			if tc.wantResult != verificationPass && runErr == nil {
				t.Fatal("a claim that did not hold up was accepted")
			}
		})
	}
}

// TestVerificationReaderIsCalledWithTheTaskID: the authoritative reader is called the way
// a plan step is — through the same path, task id included. A reader that acquires a
// worker (deployment.monitor does) has to attribute that worker's agent row and its
// reason_turns rows to the task whose done is being verified; calling the capability
// around the runtime's own fill is how runs end up with turns that belong to nobody.
// What the contract bound for `task_id`, if anything, still wins.
func TestVerificationReaderIsCalledWithTheTaskID(t *testing.T) {
	const criterion = `{"name":"C2","requirement":"the deployment completed",` +
		`"evidence":{"source":"step:deploy.output.pipeline_id"},` +
		`"expect":{"field":"state","equals":"succeeded"}`

	cases := []struct {
		name     string
		check    string // appended to the criterion: a check that names the task id itself
		wantTask string
	}{
		{name: "the runtime fills the task id", wantTask: "task-attribution"},
		{
			name: "what the contract bound wins",
			check: `,"check":{"capability":"deployment.monitor","inputs":{` +
				`"deployment":{"source":"step:deploy.output.pipeline_id"},"task_id":"task-bound"}}`,
			wantTask: "task-bound",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := executionTestStore(t)
			prevAuto := _autonomy
			factory := capability.NewFactory()
			reader := &fakeVerificationReader{
				name:    "deployment.monitor",
				inputs:  []spec.Field{{Name: "deployment", Required: true}, {Name: "task_id"}},
				outputs: []spec.Field{{Name: "state"}},
				answer:  map[string]string{"state": "succeeded"},
			}
			factory.Register(reader)
			_autonomy = &Autonomy{CapabilityFactory: factory}
			t.Cleanup(func() { _autonomy = prevAuto })

			task := &Task{ID: "task-attribution"}
			planID, err := store.CreateExecutionPlan(ExecutionPlan{TaskID: task.ID, DecisionType: "plan", Cycle: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendExecutionStep(ExecutionStep{
				PlanID: planID, TaskID: task.ID, Cycle: 1, Idx: 1, Name: "deploy",
				Capability: "service.deploy", Status: "ok",
				Output: `{"pipeline_id":"pipe-9"}`,
			}); err != nil {
				t.Fatal(err)
			}
			pinCriterion(t, task.ID, criterion+tc.check+"}")

			rt := NewRuntime(NewAgentFactory(), factory.GetAll()...)
			if err := rt.verifyDone(DecisionContext{Task: task, Cycle: 2}, planID); err != nil {
				t.Fatalf("err=%v, want the claim to hold up", err)
			}
			if got := reader.asked["task_id"]; got != tc.wantTask {
				t.Fatalf("the reader was asked task_id=%q, want %q (%v)", got, tc.wantTask, reader.asked)
			}
		})
	}
}

// TestVerifyDoneWithNoPinnedContract: nothing pinned is not a pass and not a crash — a
// `done` has nothing to be verified against, which is what it is told.
func TestVerifyDoneWithNoPinnedContract(t *testing.T) {
	executionTestStore(t)
	task := &Task{ID: "task-nocontract"}
	rt := NewRuntime(NewAgentFactory())
	err := rt.verifyDone(DecisionContext{Task: task, Cycle: 2}, 0)
	if err == nil || !strings.Contains(err.Error(), "no completion contract") {
		t.Fatalf("err=%v, want the missing contract", err)
	}
}

// TestPinCompletionContractKeepsTheFirstContract: the contract is pinned by the run's
// first answer and a later cycle restating it changes nothing — otherwise a `done` would
// be judged against a standard it weakened on the way out.
func TestPinCompletionContractKeepsTheFirstContract(t *testing.T) {
	store := executionTestStore(t)
	task := &Task{ID: "task-pin"}

	pinCriterion(t, task.ID, `{"name":"C1","requirement":"the service is healthy",`+
		`"evidence":{"source":"world_model:asset.asset-1.state"},"expect":{"equals":"healthy"}}`)
	// A second cycle restating the contract — with something easier to satisfy.
	pinCriterion(t, task.ID, `{"name":"C1","requirement":"anything at all",`+
		`"evidence":{"source":"world_model:asset.asset-1.kind"},"expect":{"exists":true}}`)

	pinned := pinnedCompletionContract(task.ID)
	if len(pinned) != 1 {
		t.Fatalf("pinned=%d criteria, want the one the first answer declared", len(pinned))
	}
	if !strings.Contains(pinned[0].Raw, "is healthy") {
		t.Fatalf("pinned criterion=%s, want the first one", pinned[0].Raw)
	}
	rows, err := store.ListCompletionContract(task.ID)
	if err != nil || len(rows) != 1 || rows[0].Idx != 1 {
		t.Fatalf("rows=%v err=%v, want one row at idx 1", rows, err)
	}
}

// errVerificationTestReader is the failure an authoritative reader a test registers can
// come back with: "I could not answer", which is inconclusive, not a verdict.
var errVerificationTestReader = errors.New("the authoritative source is unreachable")

// TestVerifyDoneJudgesWorldCriteria: a criterion whose slot is a World Model read is
// judged against that read — pass when the world says what the criterion says, fail when
// it says something else, fail when the object is not there at all, and inconclusive when
// the criterion states no fact or no readable slot.
func TestVerifyDoneJudgesWorldCriteria(t *testing.T) {
	cases := []struct {
		name      string
		asset     Asset
		criterion string
		result    string
	}{
		{
			name:  "the world says what the criterion says",
			asset: Asset{ID: "asset-1", Kind: "service", State: "healthy"},
			criterion: `{"name":"C1","requirement":"the service is healthy",` +
				`"evidence":{"source":"world_model:asset.asset-1.state"},"expect":{"equals":"healthy"}}`,
			result: verificationPass,
		},
		{
			name:  "the world says something else",
			asset: Asset{ID: "asset-1", Kind: "service", State: "healthy"},
			criterion: `{"name":"C1","requirement":"the service is degraded",` +
				`"evidence":{"source":"world_model:asset.asset-1.state"},"expect":{"equals":"degraded"}}`,
			result: verificationFail,
		},
		{
			name:  "the object is not in the World Model",
			asset: Asset{ID: "asset-1", Kind: "service", State: "healthy"},
			criterion: `{"name":"C1","requirement":"asset-9 is healthy",` +
				`"evidence":{"source":"world_model:asset.asset-9.state"},"expect":{"equals":"healthy"}}`,
			result: verificationFail,
		},
		{
			name:  "the criterion states no fact",
			asset: Asset{ID: "asset-1", Kind: "service", State: "healthy"},
			criterion: `{"name":"C1","requirement":"the service is healthy",` +
				`"evidence":{"source":"world_model:asset.asset-1.state"}}`,
			result: verificationInconclusive,
		},
		{
			name:      "prose is not a fact",
			asset:     Asset{ID: "asset-1", Kind: "service", State: "healthy"},
			criterion: `"the service is healthy, somehow"`,
			result:    verificationInconclusive,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := executionTestStore(t)
			verificationWorld(t, tc.asset)
			task := &Task{ID: "task-verify"}
			pinCriterion(t, task.ID, tc.criterion)

			rt := NewRuntime(NewAgentFactory())
			err := rt.verifyDone(DecisionContext{Task: task, Cycle: 2}, 0)

			verdicts := lastVerdicts(t, store, task.ID)
			if len(verdicts) != 1 {
				t.Fatalf("verdicts=%d, want one per criterion", len(verdicts))
			}
			if verdicts[0].Result != tc.result {
				t.Fatalf("result=%q (%s), want %q", verdicts[0].Result, verdicts[0].Reason, tc.result)
			}
			if tc.result == verificationPass && err != nil {
				t.Fatalf("err=%v, want a done that holds up to be accepted", err)
			}
			if tc.result != verificationPass && err == nil {
				t.Fatal("a claim that did not hold up was accepted")
			}
		})
	}
}
