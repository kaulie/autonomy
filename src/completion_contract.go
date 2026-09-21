package autonomy

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Completion Contract: what must be true for a task to be done, as the planner
// states it (docs/verification.md, docs/completion-contract.md).
//
// The planner defines it with its first answer of a run and the runtime pins it
// there (src/autonomy.go:Run → Runtime.Execute, cycle 1). It is pinned, not
// re-read: a `done` is judged against the contract this task started with, or a
// `done` could be accepted against a contract weakened on the way out.
//
// A criterion is one fact that must hold. It carries:
//
//   - Requirement — the fact, in the planner's words, for the record;
//   - Evidence — the *slot* the object of that fact will arrive in: the same two
//     sources a plan input binds (`step:<name>.output.<key>`,
//     `world_model:asset.<id>.<field>`). The planner does not know the id an
//     artifact will get — it is produced during execution — so it binds where the id
//     will come from, and the runtime fills the slot by resolving it against what
//     this task's steps actually produced (src/verification.go). The verifier never
//     goes looking for an object that fits: the slot says which object, and only that
//     one is judged.
//   - Expect — the fact itself, in a form the runtime can compare: the object exists,
//     or one field of the authoritative answer has a value.
//   - Check — optional, and only where the runtime's own registry has no reader for
//     that evidence: which authoritative capability to ask, and with what. This is
//     not "how to verify" — it is where the truth about that object lives.
type Criterion struct {
	Name        string
	Requirement string
	Evidence    CriterionEvidence
	Check       CriterionCheck
	Expect      CriterionExpect
	// Raw is the criterion as the planner wrote it, verbatim: it is what gets
	// pinned, so the task keeps the contract's own words rather than a re-rendering.
	Raw string
}

// CriterionEvidence is the slot a criterion's object arrives in: a step output or a
// World Model field, exactly as a plan input binds one.
type CriterionEvidence struct {
	Source string
}

// CriterionCheck names the authoritative reader for evidence the runtime cannot read
// itself (an artifact registry, a service registry, a health check).
type CriterionCheck struct {
	Capability string
	Inputs     map[string]StepInput
}

// CriterionExpect is what must hold of the authoritative answer.
type CriterionExpect struct {
	// Exists is the fact "the object exists".
	Exists bool
	// Field is the field of the authoritative answer the fact is about.
	Field string
	// Equals is the value that field must have.
	Equals string
}

// Judges reports whether the criterion says what must hold. One that does not is not
// verifiable, and verification says so instead of passing it.
//
// A World Model slot already names the field it reads, so a criterion over one may say
// only what that field must equal; a criterion over a step's output has to name the
// field of the authoritative answer it is about.
func (e CriterionExpect) Judges() bool {
	return e.Exists || strings.TrimSpace(e.Field) != "" || strings.TrimSpace(e.Equals) != ""
}

// describes renders the expected fact for a verdict's reason.
func (e CriterionExpect) describes() string {
	if e.Exists {
		return "it exists"
	}
	if strings.TrimSpace(e.Field) == "" {
		if strings.TrimSpace(e.Equals) == "" {
			return "(nothing: the criterion says no fact must hold)"
		}
		return fmt.Sprintf("the bound field == %q", e.Equals)
	}
	return fmt.Sprintf("%s == %q", e.Field, e.Equals)
}

// HasCheck reports whether the planner named the authoritative source itself.
func (c Criterion) HasCheck() bool {
	return strings.TrimSpace(c.Check.Capability) != ""
}

// IsWorld reports whether the criterion's evidence is a World Model read — the one
// evidence source that is itself authoritative, so nothing has to be asked.
func (c Criterion) IsWorld() bool {
	return strings.HasPrefix(strings.TrimSpace(c.Evidence.Source), sourceWorldPrefix)
}

// parseCompletionContract reads the `completion_contracts` field of an AGENT_V2
// answer: {"steps": [{...}]}, or a bare array. An unreadable criterion is an error —
// like an unreadable plan input it is a malformed answer, and the cycle is refused
// rather than run against a contract nobody can judge.
//
// A criterion whose *content* is incomplete (no evidence slot, no expectation) parses
// fine: that is a fact nobody can verify, and verification reports it as
// inconclusive for the planner to fix (docs/verification.md).
func parseCompletionContract(raw json.RawMessage) ([]Criterion, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	steps := []json.RawMessage{}
	if err := json.Unmarshal(raw, &steps); err != nil {
		var obj struct {
			Steps []json.RawMessage `json:"steps"`
		}
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("completion_contracts must be {\"steps\": [...]} or an array: %w", err)
		}
		steps = obj.Steps
	}
	out := make([]Criterion, 0, len(steps))
	for i, step := range steps {
		criterion, err := parseCriterion(string(step))
		if err != nil {
			return nil, fmt.Errorf("completion contract step %d: %w", i+1, err)
		}
		if strings.TrimSpace(criterion.Name) == "" {
			criterion.Name = fmt.Sprintf("C%d", i+1)
		}
		out = append(out, criterion)
	}
	return out, nil
}

// criterionJSON is one criterion as the planner writes it. `requirement` is the
// planner's word for the fact; `criterion` is read as the same thing.
type criterionJSON struct {
	Name        string          `json:"name"`
	Requirement string          `json:"requirement"`
	Criterion   string          `json:"criterion"`
	Evidence    json.RawMessage `json:"evidence"`
	Check       json.RawMessage `json:"check"`
	Expect      json.RawMessage `json:"expect"`
}

// parseCriterion reads one criterion's JSON — the shape a contract row keeps. A step
// written as a plain string is read as a criterion that states a fact and nothing else:
// it has no evidence slot and no expectation, which verification reports as
// inconclusive rather than guessing at what the prose meant.
func parseCriterion(raw string) (Criterion, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Criterion{}, fmt.Errorf("empty criterion")
	}
	var prose string
	if err := json.Unmarshal([]byte(trimmed), &prose); err == nil {
		return Criterion{Requirement: strings.TrimSpace(prose), Raw: trimmed}, nil
	}
	var c criterionJSON
	if err := json.Unmarshal([]byte(trimmed), &c); err != nil {
		return Criterion{}, err
	}
	criterion := Criterion{
		Name:        strings.TrimSpace(c.Name),
		Requirement: strings.TrimSpace(firstNonEmptyString(c.Requirement, c.Criterion)),
		Raw:         trimmed,
	}
	source, err := criterionEvidenceSource(c.Evidence)
	if err != nil {
		return Criterion{}, err
	}
	criterion.Evidence = CriterionEvidence{Source: source}
	if criterion.Check, err = criterionCheck(c.Check); err != nil {
		return Criterion{}, err
	}
	if criterion.Expect, err = criterionExpect(c.Expect); err != nil {
		return Criterion{}, err
	}
	return criterion, nil
}

// criterionEvidenceSource reads a criterion's evidence slot: {"source": "…"} (the
// plan-input shape) or the source as a plain string. A slot that is absent parses:
// the criterion then binds no evidence, which verification reports.
func criterionEvidenceSource(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text), nil
	}
	var obj struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", fmt.Errorf("evidence must be {\"source\": \"…\"}: %w", err)
	}
	return strings.TrimSpace(obj.Source), nil
}

// criterionCheck reads where the truth about a criterion's evidence lives:
// {"capability": "…", "inputs": {…}}. Inputs are read exactly like a plan step's, so a
// check binds the evidence object the same way a step binds its input.
func criterionCheck(raw json.RawMessage) (CriterionCheck, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return CriterionCheck{}, nil
	}
	var obj struct {
		Capability string          `json:"capability"`
		Inputs     json.RawMessage `json:"inputs"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return CriterionCheck{}, fmt.Errorf("check must be {\"capability\": \"…\", \"inputs\": {…}}: %w", err)
	}
	inputs, err := stepInputsFromObject(obj.Inputs)
	if err != nil {
		return CriterionCheck{}, fmt.Errorf("check inputs: %w", err)
	}
	return CriterionCheck{Capability: strings.ToLower(strings.TrimSpace(obj.Capability)), Inputs: inputs}, nil
}

// criterionExpect reads what must hold: {"exists": true} or {"field": "…", "equals":
// "…"}. Both may be absent — the criterion is then unverifiable, and verification says
// so instead of passing it.
func criterionExpect(raw json.RawMessage) (CriterionExpect, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return CriterionExpect{}, nil
	}
	var obj struct {
		Exists *bool  `json:"exists"`
		Field  string `json:"field"`
		Equals string `json:"equals"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return CriterionExpect{}, fmt.Errorf("expect must be {\"exists\": true} or {\"field\": \"…\", \"equals\": \"…\"}: %w", err)
	}
	expect := CriterionExpect{Field: strings.TrimSpace(obj.Field), Equals: strings.TrimSpace(obj.Equals)}
	if obj.Exists != nil {
		expect.Exists = *obj.Exists
	}
	return expect, nil
}

// stepInputsFromObject reads an input object the way a plan step's inputs are read
// (src/prompt.go:planInputs), for the values a criterion's check is called with.
func stepInputsFromObject(raw json.RawMessage) (map[string]StepInput, error) {
	out := map[string]StepInput{}
	if len(raw) == 0 || string(raw) == "null" {
		return out, nil
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	for key, entry := range entries {
		var in StepInput
		if err := json.Unmarshal(entry, &in); err != nil {
			return nil, fmt.Errorf("input %q: %w", key, err)
		}
		out[key] = in
	}
	return out, nil
}

// pinCompletionContract writes the contract this run's first answer declared. The
// criteria written first are the ones the task keeps: AppendCompletionContract
// ignores a row that is already there, so a later cycle restating its contract
// changes nothing (docs/execution-loop.md).
func pinCompletionContract(decision Decision, planID int64) {
	task := decision.Ctx.Task
	if task == nil || strings.TrimSpace(task.ID) == "" || len(decision.Contract) == 0 {
		return
	}
	s := activeVerificationStore()
	if s == nil {
		return
	}
	for i, criterion := range decision.Contract {
		row := ContractCriterion{
			TaskID:    task.ID,
			Idx:       i + 1,
			PlanID:    planID,
			Name:      criterion.Name,
			Criterion: criterion.Raw,
			CreatedAt: time.Now(),
		}
		if err := s.AppendCompletionContract(row); err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] pin completion contract %s/%s: %v\n", task.ID, criterion.Name, err)
		}
	}
}

// pinnedCompletionContract reads the contract a task pinned. Nothing pinned is not an
// error: it is a task whose `done` has nothing to be verified against, which is what
// verification reports.
func pinnedCompletionContract(taskID string) []Criterion {
	// A task pins its contract at its first cycle and is judged against it later, so
	// this read has to see what this runtime pinned — the writer, not a follower
	// (docs/store.md「读写分离」).
	s := writerReads(activeVerificationStore())
	if s == nil || strings.TrimSpace(taskID) == "" {
		return nil
	}
	rows, err := s.ListCompletionContract(taskID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[autonomy] read completion contract %s: %v\n", taskID, err)
		return nil
	}
	out := make([]Criterion, 0, len(rows))
	for _, row := range rows {
		criterion, err := parseCriterion(row.Criterion)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[autonomy] read completion contract %s/%s: %v\n", taskID, row.Name, err)
			continue
		}
		if strings.TrimSpace(criterion.Name) == "" {
			criterion.Name = row.Name
		}
		out = append(out, criterion)
	}
	return out
}

// ContractCriterion is one criterion of a task's pinned Completion Contract, as the
// store keeps it: which task, which position, and the criterion's own JSON verbatim
// (what the planner wrote, so the task keeps its words rather than a re-rendering).
type ContractCriterion struct {
	TaskID    string
	Idx       int
	PlanID    int64 // the first plan the contract came in with, for traceability
	Name      string
	Criterion string
	CreatedAt time.Time
}

// completionContractJSON renders a task's pinned contract for the prompt: the criteria
// as the planner wrote them, so a re-plan is made against the same words it is judged
// by. Empty when nothing is pinned.
func completionContractJSON(taskID string) json.RawMessage {
	criteria := pinnedCompletionContract(taskID)
	if len(criteria) == 0 {
		return nil
	}
	raw := make([]json.RawMessage, 0, len(criteria))
	for _, criterion := range criteria {
		raw = append(raw, json.RawMessage(criterion.Raw))
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	return encoded
}
