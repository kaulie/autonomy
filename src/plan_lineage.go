package autonomy

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kaulie/autonomy/src/capability/spec"
)

// Plan data lineage: where each step's inputs come from.
//
// The planner writes the bindings, the runtime does the transfer. What the runtime
// checks before anything runs is what it can know statically — every binding names
// an *earlier* step's *declared* output or a World Model field, and every input the
// capability requires is supplied — and what it resolves while the plan runs is the
// value itself. Nothing is inferred on the way: a value no input bound is not looked
// up in the context, and one capability's field names are never assumed to mean
// another's (field names are local to a capability, so the semantic mapping is the
// planner's to make).
//
// See docs/execution-step.md ("Plan Data Lineage") and src/agent_policy/AGENT_V2.md
// ("Plan Data Lineage").

// validatePlanLineage checks a plan's data dependencies before it is written: a
// plan whose lineage does not line up does not execute at all, so nothing is
// half-run against a value that was never going to arrive. The error names the step
// and the input, because the planner is the one that has to fix it.
func validatePlanLineage(actions []Action) error {
	// Step names first: a binding addresses a step by name, so names have to be
	// unique and readable before any binding can be checked against them.
	named := map[string]int{} // step name -> 1-based position in the plan
	capabilityOf := map[string]string{}
	for i, action := range actions {
		step, ok := capabilityStep(action)
		if !ok {
			continue
		}
		name := strings.TrimSpace(step.StepName)
		if name == "" {
			continue
		}
		if !stepNamePattern.MatchString(name) {
			return fmt.Errorf("plan step %d: %q is not a usable step name (letters, digits, - and _, and no dots — a binding addresses it as step:%s.output.<key>)", i+1, name, name)
		}
		if at, dup := named[name]; dup {
			return fmt.Errorf("two steps are called %q (positions %d and %d): a binding cannot say which one it means", name, at, i+1)
		}
		named[name] = i + 1
		capabilityOf[name] = step.Name
	}

	for i, action := range actions {
		step, ok := capabilityStep(action)
		if !ok {
			continue
		}
		declared, hasSignature := declaredInputsOf(step.Name)
		label := stepLabel(step, i)

		for _, key := range sortedInputKeys(step.Inputs) {
			in := step.Inputs[key]
			if err := checkDeclaredInput(label, step.Name, key, declared, hasSignature); err != nil {
				return err
			}
			if !in.IsBinding() {
				continue
			}
			src, err := parseInputSource(in.Source)
			if err != nil {
				return fmt.Errorf("%s input %q: %w", label, key, err)
			}
			switch src.kind {
			case InputSourceStep:
				at, known := named[src.step]
				if !known {
					return fmt.Errorf("%s input %q: this plan has no step called %q to read %q from", label, key, src.step, src.key)
				}
				if at >= i+1 {
					return fmt.Errorf("%s input %q: step %q is position %d, and a step input can only come from a step that runs before it (%d)", label, key, src.step, at, i+1)
				}
				outputs, declaredOutputs := declaredOutputsOf(capabilityOf[src.step])
				if !declaredOutputs {
					return fmt.Errorf("%s input %q: step %q (%s) declares no outputs, so %q cannot come from it", label, key, src.step, capabilityOf[src.step], src.key)
				}
				if !fieldAccepts(outputs, src.key) {
					return fmt.Errorf("%s input %q: step %q (%s) does not report %q (it reports %s)", label, key, src.step, capabilityOf[src.step], src.key, fieldNames(outputs))
				}
			case InputSourceWorld:
				// The path's shape is checked; the value is read when the step runs,
				// because the World Model is what it is at that moment — a binding is
				// a read, not a snapshot.
			default:
				return fmt.Errorf("%s input %q: unknown source kind %q", label, key, src.kind)
			}
		}

		if !hasSignature {
			continue
		}
		for _, field := range declared {
			if !field.Required {
				continue
			}
			if suppliedInput(step.Inputs, field) {
				continue
			}
			return fmt.Errorf("%s (%s) does not supply %q, which the capability requires — the runtime does not invent it", label, step.Name, field.Name)
		}
	}
	return nil
}

// checkDeclaredInput rejects an input the capability does not declare: field names
// are local to a capability, so a key it never declared is a typo, not a value, and
// the capability would silently ignore it.
func checkDeclaredInput(label, capabilityName, key string, declared []spec.Field, hasSignature bool) error {
	if !hasSignature || fieldAccepts(declared, key) {
		return nil
	}
	return fmt.Errorf("%s (%s) passes %q, which the capability does not declare (it takes %s)", label, capabilityName, key, fieldNames(declared))
}

// resolveStepInputs turns a step's inputs into the values its capability is called
// with: literals as the plan wrote them, bindings resolved against what this plan
// has already produced and against the World Model.
func resolveStepInputs(inputs map[string]StepInput, prior []ActionResult) (map[string]string, error) {
	out := make(map[string]string, len(inputs))
	for _, key := range sortedInputKeys(inputs) {
		in := inputs[key]
		if !in.IsBinding() {
			out[key] = in.Literal
			continue
		}
		value, err := resolveInputSource(in.Source, prior)
		if err != nil {
			return nil, fmt.Errorf("input %q: %w", key, err)
		}
		out[key] = value
	}
	return out, nil
}

// resolveInputSource reads one binding's value: an output of an earlier step of
// this plan, or a World Model field. A source that is missing, or whose value is
// empty, is reported as such — an empty value passed on is a step failing somewhere
// further away, with a worse message.
func resolveInputSource(raw string, prior []ActionResult) (string, error) {
	src, err := parseInputSource(raw)
	if err != nil {
		return "", err
	}
	switch src.kind {
	case InputSourceStep:
		for _, step := range prior {
			if step.StepName != src.step {
				continue
			}
			value, ok := step.Output[src.key]
			if !ok {
				return "", fmt.Errorf("step %q did not report %q, so there is no value to read (it reported %s)", src.step, src.key, outputKeys(step.Output))
			}
			if strings.TrimSpace(value) == "" {
				return "", fmt.Errorf("step %q reported an empty %q, and this step needs a value", src.step, src.key)
			}
			return value, nil
		}
		return "", fmt.Errorf("step %q has produced nothing in this plan", src.step)
	case InputSourceWorld:
		return worldValue(src.path)
	default:
		return "", fmt.Errorf("unknown source kind %q", src.kind)
	}
}

// worldValue reads one World Model field: the asset is looked up by the id the plan
// wrote (no searching, no fallback — a value the plan did not name is not inferred).
func worldValue(path []string) (string, error) {
	if _world == nil || _world.assetManager == nil {
		return "", fmt.Errorf("this runtime has no World Model to read %s from", strings.Join(path, "."))
	}
	id, field := strings.Join(path[1:len(path)-1], "."), path[len(path)-1]
	asset, err := _world.assetManager.Get(id)
	if err != nil {
		return "", fmt.Errorf("world_model:%s: %w", strings.Join(path, "."), err)
	}
	switch field {
	case worldFieldState:
		return asset.State, nil
	case worldFieldKind:
		return asset.Kind, nil
	}
	return "", fmt.Errorf("world_model:%s: the World Model has no field %q", strings.Join(path, "."), field)
}

// capabilityStep is an action that calls a capability — the only kind that carries
// inputs and lineage.
func capabilityStep(action Action) (CapabilityAction, bool) {
	step, ok := action.(CapabilityAction)
	return step, ok
}

// declaredInputsOf / declaredOutputsOf read what a registered capability declares
// about its call shape. ok is false for a capability that is not registered or that
// declares no signature: nothing can be checked about it here, and the other checks
// (an unknown capability fails its step) report it instead.
func declaredInputsOf(name string) ([]spec.Field, bool) {
	return declaredFieldsOf(name, true)
}

func declaredOutputsOf(name string) ([]spec.Field, bool) {
	return declaredFieldsOf(name, false)
}

func declaredFieldsOf(name string, inputs bool) ([]spec.Field, bool) {
	f := activeCapabilityFactory()
	if f == nil {
		return nil, false
	}
	cap := f.Get(strings.ToLower(strings.TrimSpace(name)))
	if cap == nil {
		return nil, false
	}
	declared, ok := cap.(spec.Declared)
	if !ok {
		return nil, false
	}
	if inputs {
		return declared.Inputs(), true
	}
	return declared.Outputs(), true
}

// fieldAccepts reports whether fields contains name (or an alias of it).
func fieldAccepts(fields []spec.Field, name string) bool {
	name = strings.TrimSpace(name)
	for _, field := range fields {
		if field.Name == name {
			return true
		}
		for _, alias := range field.Aliases {
			if alias == name {
				return true
			}
		}
	}
	return false
}

// suppliedInput reports whether a step supplies a declared input under its name or
// one of its aliases.
func suppliedInput(inputs map[string]StepInput, field spec.Field) bool {
	for key := range inputs {
		if key == field.Name {
			return true
		}
		for _, alias := range field.Aliases {
			if key == alias {
				return true
			}
		}
	}
	return false
}

// fieldNames renders a declaration's field names for an error message.
func fieldNames(fields []spec.Field) string {
	if len(fields) == 0 {
		return "none"
	}
	names := make([]string, 0, len(fields))
	for _, field := range fields {
		names = append(names, field.Name)
	}
	return strings.Join(names, ", ")
}

// outputKeys renders the keys a step actually reported.
func outputKeys(output map[string]string) string {
	if len(output) == 0 {
		return "nothing"
	}
	names := make([]string, 0, len(output))
	for key := range output {
		names = append(names, key)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// sortedInputKeys is a step's input keys in a stable order, so a plan's error
// messages do not depend on map iteration order.
func sortedInputKeys(inputs map[string]StepInput) []string {
	keys := make([]string, 0, len(inputs))
	for key := range inputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// stepLabel names a step in an error message: its own name when it has one, and its
// position in the plan otherwise.
func stepLabel(step CapabilityAction, i int) string {
	if name := strings.TrimSpace(step.StepName); name != "" {
		return fmt.Sprintf("step %q", name)
	}
	return fmt.Sprintf("step %d", i+1)
}
