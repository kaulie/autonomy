package autonomy

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
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

// UnmarshalJSON reads the shapes a plan may write for one input:
//
//	"main"                            a literal value
//	300 / 1.5 / true / false / null   any scalar is a literal too — a capability's
//	                                  inputs are strings, and a number is a value
//	{"source": "…"}                   a binding to where the value comes from
//	{"value": 300}                    a literal written as an object
//
// An object that names neither a source nor a value is an error, never a literal:
// that is the shape a broken binding has, and passing it on as text is precisely the
// failure the binding model exists to stop (a plan that said "the pr_url of step 1"
// reached the capability as that sentence).
func (in *StepInput) UnmarshalJSON(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	// UseNumber keeps 300 as the text "300": the capability reads text, and a
	// float64 would turn it into 300 or 3e+02.
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return fmt.Errorf("input must be a value or {\"source\":\"…\"}: %s", trimmed)
	}
	if object, ok := value.(map[string]any); ok {
		return in.fromObject(object, trimmed)
	}
	if err := in.setLiteral(value, trimmed); err != nil {
		return err
	}
	return nil
}

// fromObject reads the object forms a plan may write: a binding, or a literal under
// "value". Anything else is refused — an object with no source and no value is what a
// broken binding looks like, and a broken binding must not travel as text.
func (in *StepInput) fromObject(object map[string]any, raw string) error {
	source, hasSource := object["source"]
	if !hasSource {
		spec, hasValue := object["value"]
		if !hasValue {
			return fmt.Errorf("input %s names neither a source nor a value (want a plain value, {\"source\":\"step:<name>.output.<key>\"} or {\"source\":\"world_model:<path>\"})", raw)
		}
		return in.setLiteral(spec, raw)
	}
	text, ok := source.(string)
	if !ok {
		return fmt.Errorf("input %s: \"source\" must be a string, not %s", raw, jsonValueKind(source))
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("input %s names no source (want {\"source\":\"step:<name>.output.<key>\"} or {\"source\":\"world_model:<path>\"})", raw)
	}
	*in = StepInput{Source: strings.TrimSpace(text)}
	return nil
}

// setLiteral turns a JSON scalar — or a structured value, which travels as the JSON
// text it is — into the text a capability reads.
func (in *StepInput) setLiteral(value any, raw string) error {
	switch v := value.(type) {
	case nil:
		*in = StepInput{}
	case string:
		*in = StepInput{Literal: v}
	case json.Number:
		*in = StepInput{Literal: v.String()}
	case bool:
		*in = StepInput{Literal: strconv.FormatBool(v)}
	case map[string]any, []any:
		encoded, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("input %s: %w", raw, err)
		}
		*in = StepInput{Literal: string(encoded)}
	default:
		return fmt.Errorf("input must be a value or {\"source\":\"…\"}: %s", raw)
	}
	return nil
}

// jsonValueKind names a JSON value's type for an error message.
func jsonValueKind(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case json.Number:
		return "a number"
	case bool:
		return "a boolean"
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	}
	return "an unknown value"
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
