package autonomy

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// StepInput is one value a step calls its capability with.
//
// It is either a literal the plan wrote, or a binding that says where the value
// comes from: an output of an earlier step of the same plan, or a value of the
// World Model. Nothing else — the runtime resolves exactly what the plan bound and
// never goes looking for a value on its own (see docs/execution-step.md, "Plan Data
// Lineage"). Field names are local to each capability, so the planner is the one
// that knows "A's artifact_version is B's version"; the runtime only transfers.
type StepInput struct {
	// Literal is the value the plan wrote. Empty when Source is set.
	Literal string
	// Source is the binding, "step:<name>.output.<key>" or "world_model:<path>".
	// Empty when this is a literal.
	Source string
}

// LiteralInput is the plan value for a plain value.
func LiteralInput(value string) StepInput { return StepInput{Literal: value} }

// BoundInput is the plan value for a binding to where the value comes from.
func BoundInput(source string) StepInput { return StepInput{Source: source} }

// IsBinding reports whether this input names a source instead of carrying a value.
func (in StepInput) IsBinding() bool { return strings.TrimSpace(in.Source) != "" }

// literalInputs turns a plain map (a runtime-filled default, a test's shorthand)
// into step inputs.
func literalInputs(m map[string]string) map[string]StepInput {
	out := make(map[string]StepInput, len(m))
	for k, v := range m {
		out[k] = LiteralInput(v)
	}
	return out
}

// UnmarshalJSON reads both shapes a plan may write: a bare value is a literal
// ("method":"squash"), and {"source":"…"} is a binding.
//
// Anything else is an error rather than a literal. A malformed binding silently
// passed on as text is precisely the failure this model exists to stop: a plan that
// said "the pr_url of step 1" in prose reached the capability as that sentence and
// failed there, far from the mistake.
func (in *StepInput) UnmarshalJSON(raw []byte) error {
	var literal string
	if err := json.Unmarshal(raw, &literal); err == nil {
		*in = StepInput{Literal: literal}
		return nil
	}
	var obj struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf("input must be a value or {\"source\":\"…\"}: %s", strings.TrimSpace(string(raw)))
	}
	source := strings.TrimSpace(obj.Source)
	if source == "" {
		return fmt.Errorf("input %s names no source (want {\"source\":\"step:<name>.output.<key>\"} or {\"source\":\"world_model:<path>\"})", strings.TrimSpace(string(raw)))
	}
	*in = StepInput{Source: source}
	return nil
}

// MarshalJSON writes back what the plan wrote: a binding stays a binding (the plan
// row keeps the planner's own input — docs/execution-step.md).
func (in StepInput) MarshalJSON() ([]byte, error) {
	if in.IsBinding() {
		return json.Marshal(map[string]string{"source": in.Source})
	}
	return json.Marshal(in.Literal)
}

// InputSourceKind is the kind of a binding's source.
type InputSourceKind string

const (
	// InputSourceStep reads an output of an earlier step of the same plan.
	InputSourceStep InputSourceKind = "step"
	// InputSourceWorld reads a value of the World Model.
	InputSourceWorld InputSourceKind = "world_model"

	sourceStepPrefix  = string(InputSourceStep) + ":"
	sourceWorldPrefix = string(InputSourceWorld) + ":"

	// worldAssetRoot is the only World Model root today: assets. A path names one
	// asset and one of its fields, read literally — the runtime looks the asset up
	// by the id the plan wrote, it does not search for a match.
	worldAssetRoot = "asset"

	// The two World Model fields a path may name today: an asset's own record.
	worldFieldState = "state"
	worldFieldKind  = "kind"
)

// stepNamePattern is what a step may be called: a binding says "step:<name>", so a
// name may not contain the dots the path uses.
var stepNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// inputSource is a parsed binding: one place a step input's value comes from.
type inputSource struct {
	raw  string
	kind InputSourceKind
	// step is the source step's name and key the output it reads (kind step).
	step string
	key  string
	// path is the World Model path (kind world_model).
	path []string
}

// parseInputSource reads a binding. The accepted forms are exactly two, and an
// unreadable binding is an error rather than a guess — like a pull request this
// capability cannot name, a source the runtime cannot read must not be invented.
//
//	step:<name>.output.<key>       an output of an earlier step of this plan
//	world_model:asset.<id>.<field> a World Model field: kind | state
func parseInputSource(raw string) (inputSource, error) {
	s := strings.TrimSpace(raw)
	unreadable := func() (inputSource, error) {
		return inputSource{}, fmt.Errorf("cannot read an input source from %q (want step:<name>.output.<key> or world_model:asset.<id>.kind|state)", s)
	}
	switch {
	case strings.HasPrefix(s, sourceStepPrefix):
		rest := strings.TrimPrefix(s, sourceStepPrefix)
		i := strings.Index(rest, ".output.")
		if i <= 0 {
			return unreadable()
		}
		name, key := rest[:i], rest[i+len(".output."):]
		if !stepNamePattern.MatchString(name) || key == "" || strings.ContainsAny(key, ". \t") {
			return unreadable()
		}
		return inputSource{raw: s, kind: InputSourceStep, step: name, key: key}, nil
	case strings.HasPrefix(s, sourceWorldPrefix):
		path := strings.Split(strings.TrimPrefix(s, sourceWorldPrefix), ".")
		if len(path) < 3 || path[0] != worldAssetRoot {
			return unreadable()
		}
		id, field := strings.Join(path[1:len(path)-1], "."), path[len(path)-1]
		if id == "" || (field != worldFieldState && field != worldFieldKind) {
			return unreadable()
		}
		return inputSource{raw: s, kind: InputSourceWorld, path: path}, nil
	default:
		return unreadable()
	}
}
