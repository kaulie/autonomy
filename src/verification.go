package autonomy

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Verification: do the facts the Completion Contract names hold now?
// (docs/verification.md)
//
// It answers that from the outside. Not from what the planner claims, and not from
// what the step that did the work reported about itself: the planner defines what
// must be true, the executor produces facts, the runtime fills the contract's
// pre-bound evidence slots with those facts, and verification asks the authoritative
// source about the object a slot named — the only object it looks at.
//
// Three verdicts, because two would force a guess:
//
//   - pass         the authoritative answer is the fact the criterion states;
//   - fail         the object is there and the fact does not hold;
//   - inconclusive the fact could not be established at all: no authoritative source,
//     an unreadable evidence slot, or nothing to compare.
//
// Only pass holds a `done` up. fail and inconclusive both leave the claim unproven
// and go back to the planner as the reason this cycle did not conclude — what to do
// about it is the planner's call, not the verifier's (docs/verification.md).
const (
	verificationPass         = "pass"
	verificationFail         = "fail"
	verificationInconclusive = "inconclusive"
)

// Verification is one verdict on one criterion of a task's pinned Completion
// Contract, as the `verification` table records it: what must hold, what the
// authoritative answer was, and where that answer came from.
type Verification struct {
	ID          int64
	TaskID      string
	PlanID      int64
	Cycle       int
	Criterion   string // the criterion's name
	Requirement string // what it says must be true, in its own words
	Method      string // world_model | registry:<capability> | declared:<capability>
	Evidence    string // JSON: the slot the criterion bound, and what it resolved to
	Expected    string // the fact that must hold
	Observed    string // what the authoritative source answered
	Result      string // pass | fail | inconclusive
	Reason      string
	CreatedAt   time.Time
}

// VerificationError is a `done` that did not hold up: the criteria that were not
// verified, and why. It is what the next decision sees in previous_actions.
type VerificationError struct {
	Reason   string
	Verdicts []Verification
}

func (e *VerificationError) Error() string {
	if e == nil {
		return ""
	}
	parts := make([]string, 0, len(e.Verdicts))
	for _, v := range e.Verdicts {
		if v.Result == verificationPass {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s — expected %s, observed %s (via %s)",
			v.Criterion, v.Reason, v.Expected, observedOrNothing(v), v.Method))
	}
	if len(parts) == 0 {
		return "verification refused the done: " + e.Reason
	}
	return fmt.Sprintf("verification refused the done (%s): %s", e.Reason, strings.Join(parts, "; "))
}

// observedOrNothing renders what the authoritative source said, for a message:
// nothing at all is its own answer.
func observedOrNothing(v Verification) string {
	if strings.TrimSpace(v.Observed) == "" {
		return "nothing"
	}
	return v.Observed
}

// verificationReader is how the runtime answers a fact about one kind of evidence
// without being told: the read-only capability that owns that kind of object, and the
// input the evidence object is handed to it in.
//
// The key is the capability that *produced* the evidence (the step the criterion's
// slot resolved to), because that is what says what kind of thing the evidence is: an
// output of service.deploy is a pipeline, and a pipeline's truth lives in the
// deployment control plane. Nothing here matches field names across capabilities — the
// input is named explicitly, once, in one place (see docs/verification.md).
type verificationReader struct {
	Capability string // the capability to ask
	Input      string // the input it takes the evidence object in
}

var verificationReaders = map[string]verificationReader{
	"service.deploy": {Capability: "deployment.monitor", Input: "deployment"},
}

// verifyDone evaluates the task's pinned Completion Contract and returns nil when every
// criterion passed. Every verdict is recorded (best effort — the verdict is what the
// run acts on, the row is what makes it auditable), and a claim that did not hold up
// comes back as a *VerificationError.
//
// It runs once per `done`: a done carries no steps, so there is nothing to observe
// twice, and a refused done re-plans (docs/execution-loop.md).
func (r *Runtime) verifyDone(ctx DecisionContext, planID int64) error {
	taskID := taskIDOf(ctx)
	criteria := pinnedCompletionContract(taskID)
	if len(criteria) == 0 {
		return &VerificationError{Reason: "this task pinned no completion contract, so a done has nothing to be verified against: declare the contract with your first answer (AGENT_V2.md §Completion Contract)"}
	}
	// The evidence slots are resolved against what this task's steps produced, oldest
	// first: the ids a plan could not know at planning time are exactly what its steps
	// produced while it ran.
	prior := taskStepOutputs(taskID)
	verdicts := make([]Verification, 0, len(criteria))
	unverified := 0
	for _, criterion := range criteria {
		verdict := r.verifyCriterion(ctx, planID, criterion, prior)
		verdicts = append(verdicts, verdict)
		saveVerification(verdict)
		if verdict.Result != verificationPass {
			unverified++
		}
	}
	if unverified == 0 {
		return nil
	}
	return &VerificationError{
		Reason:   fmt.Sprintf("%d of %d completion criteria did not hold up", unverified, len(verdicts)),
		Verdicts: verdicts,
	}
}

// verifyCriterion judges one criterion. It never goes looking for evidence: the slot
// the criterion bound is the only thing it looks at, and the answer comes from the
// World Model (when the slot is a World Model read) or from the authoritative
// capability that owns that kind of object.
func (r *Runtime) verifyCriterion(ctx DecisionContext, planID int64, criterion Criterion, prior []ActionResult) Verification {
	v := Verification{
		TaskID:      taskIDOf(ctx),
		PlanID:      planID,
		Cycle:       ctx.Cycle,
		Criterion:   criterion.Name,
		Requirement: criterion.Requirement,
		Expected:    criterion.Expect.describes(),
		Evidence:    evidenceJSON(criterion.Evidence.Source, ""),
		CreatedAt:   time.Now(),
	}
	verdict := func(result, reason, observed, method string) Verification {
		v.Result, v.Reason, v.Observed, v.Method = result, reason, observed, method
		return v
	}
	if !criterion.Expect.Judges() {
		return verdict(verificationInconclusive,
			"the criterion does not say what must hold (expect: {\"exists\": true} or {\"field\": …, \"equals\": …})",
			"", verificationMethodNone)
	}
	if strings.TrimSpace(criterion.Evidence.Source) == "" {
		return verdict(verificationInconclusive,
			"the criterion binds no evidence slot, so there is no object to verify", "", verificationMethodNone)
	}
	src, err := parseInputSource(criterion.Evidence.Source)
	if err != nil {
		return verdict(verificationInconclusive, fmt.Sprintf("the evidence slot is not readable: %v", err), "", verificationMethodNone)
	}
	switch src.kind {
	case InputSourceWorld:
		return r.verifyWorldCriterion(verdict, src, criterion.Expect)
	case InputSourceStep:
		return r.verifyStepCriterion(ctx, &v, verdict, src, criterion, prior)
	}
	return verdict(verificationInconclusive, fmt.Sprintf("unknown evidence source kind %q", src.kind), "", verificationMethodNone)
}

// verdictFunc records one verdict's outcome on the Verification being built.
type verdictFunc func(result, reason, observed, method string) Verification

// verificationMethodNone is the method of a criterion nothing was asked about.
const verificationMethodNone = "-"

// verifyWorldCriterion judges a criterion whose slot is a World Model read: the World
// Model is the authoritative source for a local asset's own state, so the read is the
// answer and no capability has to be asked for it.
func (r *Runtime) verifyWorldCriterion(verdict verdictFunc, src inputSource, expect CriterionExpect) Verification {
	field := src.path[len(src.path)-1]
	if expect.Field != "" && !matches(expect.Field, field) {
		return verdict(verificationInconclusive,
			fmt.Sprintf("the criterion expects field %q but its slot reads %q — the slot is the field that is read", expect.Field, field),
			"", verificationMethodNone)
	}
	value, exists, err := worldFact(src.path)
	if err != nil {
		return verdict(verificationInconclusive, err.Error(), "", "world_model")
	}
	if !exists {
		return verdict(verificationFail,
			fmt.Sprintf("%s is not in the World Model, so the fact about it does not hold", assetFromPath(src.path)),
			"", "world_model")
	}
	observed := fmt.Sprintf("%s=%s", field, value)
	if expect.Exists {
		return verdict(verificationPass, "the object is in the World Model", observed, "world_model")
	}
	if matches(value, expect.Equals) {
		return verdict(verificationPass, fmt.Sprintf("%s is %q, as the criterion says", field, expect.Equals), observed, "world_model")
	}
	return verdict(verificationFail, fmt.Sprintf("%s is %q, and the criterion says %q", field, value, expect.Equals), observed, "world_model")
}

// verifyStepCriterion judges a criterion whose slot is a step's output. What a step
// reported is evidence — a reference — not truth, so the value it produced is only what
// the authoritative source is asked *about*; the verdict comes from that source. The
// context is threaded through because asking is a call this runtime makes on the task's
// behalf (its task id goes to a reader that declares one — see askAuthority).
func (r *Runtime) verifyStepCriterion(ctx DecisionContext, v *Verification, verdict verdictFunc, src inputSource, criterion Criterion, prior []ActionResult) Verification {
	producer, ok := stepOutputByName(prior, src.step)
	if !ok {
		return verdict(verificationInconclusive,
			fmt.Sprintf("no step called %q has produced anything in this task", src.step), "", verificationMethodNone)
	}
	reference := strings.TrimSpace(producer.Output[src.key])
	if reference == "" {
		return verdict(verificationInconclusive,
			fmt.Sprintf("step %q reported no %q to verify (it reported %s)", src.step, src.key, outputKeys(producer.Output)),
			"", verificationMethodNone)
	}
	v.Evidence = evidenceJSON(criterion.Evidence.Source, reference)
	if !criterion.Expect.Exists && criterion.Expect.Field == "" {
		return verdict(verificationInconclusive,
			"a step's own report is evidence, not a fact: the criterion has to name the field of the authoritative answer it is about (expect.field)",
			reference, verificationMethodNone)
	}
	ask, err := authorityFor(criterion, producer, src.key)
	if err != nil {
		return verdict(verificationInconclusive, err.Error(), reference, verificationMethodNone)
	}
	answer, err := r.askAuthority(ctx, ask, prior)
	if err != nil {
		return verdict(verificationInconclusive, fmt.Sprintf("the authoritative source could not answer: %v", err), reference, ask.method)
	}
	if criterion.Expect.Exists {
		return verdict(verificationPass, "the authoritative source answered", reference, ask.method)
	}
	observed := strings.TrimSpace(answer[criterion.Expect.Field])
	if observed == "" {
		return verdict(verificationInconclusive,
			fmt.Sprintf("the authoritative source reported no %q (it reported %s)", criterion.Expect.Field, outputKeys(answer)),
			reference, ask.method)
	}
	if matches(observed, criterion.Expect.Equals) {
		return verdict(verificationPass, fmt.Sprintf("%s is %q, as the criterion says", criterion.Expect.Field, criterion.Expect.Equals), observed, ask.method)
	}
	return verdict(verificationFail, fmt.Sprintf("%s is %q, and the criterion says %q", criterion.Expect.Field, observed, criterion.Expect.Equals), observed, ask.method)
}

// verificationAsk is one authoritative source to query about one piece of evidence: the
// capability, the input the evidence object is handed to it in, and how to name that
// source in the verdict.
type verificationAsk struct {
	capability string
	method     string
	inputs     map[string]StepInput
}

// authorityFor decides who is asked about a piece of evidence: the capability the
// planner named in the criterion's check, or the reader the runtime knows for that kind
// of evidence. A kind nothing can answer is not a failure of the run — it is the
// inconclusive the planner has to deal with.
func authorityFor(criterion Criterion, producer ActionResult, key string) (verificationAsk, error) {
	if criterion.HasCheck() {
		return verificationAsk{
			capability: criterion.Check.Capability,
			method:     "declared:" + criterion.Check.Capability,
			inputs:     criterion.Check.Inputs,
		}, nil
	}
	reader, ok := verificationReaders[strings.ToLower(strings.TrimSpace(producer.Capability))]
	if !ok {
		return verificationAsk{}, fmt.Errorf(
			"nothing authoritative answers about this evidence: %s reports %q, and no reader is registered for it (name the criterion's check, or observe the fact into the World Model)",
			producer.Capability, key)
	}
	return verificationAsk{
		capability: reader.Capability,
		method:     "registry:" + reader.Capability,
		inputs:     map[string]StepInput{reader.Input: BoundInput(criterion.Evidence.Source)},
	}, nil
}

// askAuthority calls one authoritative reader: a capability that observes and changes
// nothing. Its inputs are checked the way a plan step's are — a reader called with an
// input it does not declare, or without one it requires, is a broken verification and
// says so instead of being called anyway.
//
// It is a call the runtime makes on this task's behalf, so it goes through the same
// path a plan step does: the task id is filled for a reader that declares `task_id`
// before the reader runs (fillTaskID). A reader that acquires a worker for this
// observation — deployment.monitor does — therefore produces an agent row and
// reason_turns rows under *this* task, exactly as the step whose evidence it is
// judging did. The inputs the contract bound are still what the reader is called with;
// nothing else is added, and nothing bound is overwritten.
func (r *Runtime) askAuthority(ctx DecisionContext, ask verificationAsk, prior []ActionResult) (map[string]string, error) {
	name := strings.ToLower(strings.TrimSpace(ask.capability))
	reader, ok := r.caps[name]
	if !ok {
		return nil, fmt.Errorf("this runtime has no capability %q", name)
	}
	declared, hasSignature := declaredInputsOf(name)
	label := name + " check"
	for _, key := range sortedInputKeys(ask.inputs) {
		if err := checkDeclaredInput(label, name, key, declared, hasSignature); err != nil {
			return nil, err
		}
	}
	if hasSignature {
		for _, field := range declared {
			if field.Required && !suppliedInput(ask.inputs, field) {
				return nil, fmt.Errorf("the check does not supply %q, which %s requires", field.Name, name)
			}
		}
	}
	values, err := resolveStepInputs(ask.inputs, prior)
	if err != nil {
		return nil, err
	}
	fillTaskID(name, ctx.Task, values)
	return reader.Run(values)
}

// taskStepOutputs is what this task's steps have produced: one entry per step name, the
// most recent successful execution of it, in plan order. A criterion's slot binds a step
// by name, and the contract is the task's rather than one plan's, so a slot is resolved
// against the task's own step history — a slot naming a step this plan does not have is
// a step an earlier cycle ran.
func taskStepOutputs(taskID string) []ActionResult {
	s := activeStore()
	if s == nil || strings.TrimSpace(taskID) == "" {
		return nil
	}
	plans, err := s.ListExecutionPlans(taskID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] read plans of task %s: %v\n", taskID, err)
		return nil
	}
	at := map[string]int{}
	out := []ActionResult{}
	for _, plan := range plans {
		steps, err := s.ListExecutionSteps(plan.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] read steps of plan %d: %v\n", plan.ID, err)
			continue
		}
		for _, step := range steps {
			name := strings.TrimSpace(step.Name)
			if name == "" || step.Status != "ok" {
				continue
			}
			record := ActionResult{
				Capability: step.Capability,
				StepName:   name,
				Output:     parseJSONObject(step.Output),
			}
			if i, seen := at[name]; seen {
				out[i] = record // the most recent successful execution of that name
				continue
			}
			at[name] = len(out)
			out = append(out, record)
		}
	}
	return out
}

// stepOutputByName finds the step a criterion's slot resolved against.
func stepOutputByName(prior []ActionResult, name string) (ActionResult, bool) {
	for _, record := range prior {
		if record.StepName == name {
			return record, true
		}
	}
	return ActionResult{}, false
}

// worldFact reads one World Model field for a criterion, and says whether the asset is
// there at all: a criterion naming an object that does not exist is *failed* by that,
// while a World Model that cannot be read at all is inconclusive.
func worldFact(path []string) (string, bool, error) {
	if _world == nil || _world.assetManager == nil {
		return "", false, fmt.Errorf("this runtime has no World Model to read %s from", strings.Join(path, "."))
	}
	id, field := strings.Join(path[1:len(path)-1], "."), path[len(path)-1]
	asset, err := _world.assetManager.Get(id)
	if err != nil {
		return "", false, nil
	}
	switch field {
	case worldFieldState:
		return asset.State, true, nil
	case worldFieldKind:
		return asset.Kind, true, nil
	}
	return "", true, fmt.Errorf("the World Model has no field %q", field)
}

// assetFromPath names the asset a World Model path reads, for a message.
func assetFromPath(path []string) string {
	if len(path) < 2 {
		return strings.Join(path, ".")
	}
	return strings.Join(path[1:len(path)-1], ".")
}

// matches compares a World value with what a criterion says it must be: both sides are
// trimmed and case is ignored, because "Healthy" and "healthy" are the same state in the
// World's own vocabulary, and a verdict must not turn on which one was typed.
func matches(observed, expected string) bool {
	return strings.EqualFold(strings.TrimSpace(observed), strings.TrimSpace(expected))
}

// evidenceJSON records what a criterion bound and what the runtime resolved it to: the
// slot is the planner's, the reference is the fact the slot was filled with.
func evidenceJSON(slot, reference string) string {
	raw, err := json.Marshal(map[string]string{"slot": strings.TrimSpace(slot), "reference": reference})
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// validateCompletionContract checks a run's first answer's contract before it is pinned:
// a contract the runtime could never evaluate must not become the standard the task ends
// up being held to, so the cycle is refused with a reason the planner can fix — the same
// gate a plan's data lineage passes (src/plan_lineage.go).
//
// What it refuses is a contract that cannot be read at all: an evidence slot that is not
// a readable binding, a slot naming a step of this plan that cannot produce the value, or
// a check the runtime cannot run. A criterion that states a fact with no slot, no
// expectation, or no source to answer it *passes* here: it costs no work, and it is
// reported as inconclusive when the `done` comes (docs/verification.md) — refusing the
// whole plan would trade a verifiable fact for no work at all.
func validateCompletionContract(contract []Criterion, actions []Action) error {
	for i, criterion := range contract {
		label := criterionLabel(criterion, i)
		if strings.TrimSpace(criterion.Evidence.Source) == "" {
			continue
		}
		src, err := parseInputSource(criterion.Evidence.Source)
		if err != nil {
			return fmt.Errorf("completion contract %s: %w", label, err)
		}
		if src.kind == InputSourceStep {
			if err := checkSlotAgainstPlan(label, src, actions); err != nil {
				return err
			}
		}
		if criterion.HasCheck() {
			if err := checkAuthority(label, criterion); err != nil {
				return err
			}
		}
	}
	return nil
}

// criterionLabel names a criterion in an error about the contract itself.
func criterionLabel(criterion Criterion, i int) string {
	if name := strings.TrimSpace(criterion.Name); name != "" {
		return fmt.Sprintf("%q", name)
	}
	return fmt.Sprintf("step %d", i+1)
}

// checkSlotAgainstPlan checks a slot that names a step of *this* plan: the step has to
// be there and its capability has to report the value. A slot naming a step this plan
// does not have is not an error — the contract is the task's, and that step may be one
// an earlier cycle ran.
func checkSlotAgainstPlan(label string, src inputSource, actions []Action) error {
	for _, action := range actions {
		step, ok := capabilityStep(action)
		if !ok || strings.TrimSpace(step.StepName) != src.step {
			continue
		}
		outputs, declared := declaredOutputsOf(step.Name)
		if !declared {
			return fmt.Errorf("completion contract %s: step %q (%s) declares no outputs, so %q cannot come from it", label, src.step, step.Name, src.key)
		}
		if !fieldAccepts(outputs, src.key) {
			return fmt.Errorf("completion contract %s: step %q (%s) does not report %q (it reports %s)", label, src.step, step.Name, src.key, fieldNames(outputs))
		}
		return nil
	}
	return nil
}

// checkAuthority checks a check the planner named: the capability has to exist, its
// inputs have to line up with what it declares, and the field the criterion compares has
// to be one it reports. A check that cannot answer would turn into a verdict, and a
// verdict nothing supports is exactly what verification is here to stop.
func checkAuthority(label string, criterion Criterion) error {
	check := criterion.Check
	f := activeCapabilityFactory()
	if f == nil || !f.Has(check.Capability) {
		return fmt.Errorf("completion contract %s: the check names capability %q, which this runtime does not have", label, check.Capability)
	}
	declared, hasSignature := declaredInputsOf(check.Capability)
	for _, key := range sortedInputKeys(check.Inputs) {
		in := check.Inputs[key]
		if err := checkDeclaredInput(label+" check", check.Capability, key, declared, hasSignature); err != nil {
			return err
		}
		if !in.IsBinding() {
			continue
		}
		if _, err := parseInputSource(in.Source); err != nil {
			return fmt.Errorf("completion contract %s: check input %q: %w", label, key, err)
		}
	}
	if !hasSignature {
		return nil
	}
	for _, field := range declared {
		if field.Required && !suppliedInput(check.Inputs, field) {
			return fmt.Errorf("completion contract %s: the check does not supply %q, which %s requires", label, field.Name, check.Capability)
		}
	}
	if criterion.Expect.Exists || criterion.Expect.Field == "" {
		return nil
	}
	outputs, declaredOutputs := declaredOutputsOf(check.Capability)
	if !declaredOutputs {
		return fmt.Errorf("completion contract %s: %s declares no outputs, so it cannot be asked for %q", label, check.Capability, criterion.Expect.Field)
	}
	if !fieldAccepts(outputs, criterion.Expect.Field) {
		return fmt.Errorf("completion contract %s: %s does not report %q (it reports %s)", label, check.Capability, criterion.Expect.Field, fieldNames(outputs))
	}
	return nil
}
