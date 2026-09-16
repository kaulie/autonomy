// Package capability holds the Capability interface, factory, and built-in abilities.
// Domain-specific abilities live under subpackages (e.g. software_development).
package capability

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/kaulie/autonomy/src/capability/spec"
)

// Capability is a named thing the system can do to or with the world.
type Capability interface {
	Name() string
	Domain() string
	// Provider is who provides this capability (e.g. cursor, autonomy). It is the
	// runtime's own attribution — it travels on the step the runtime recorded
	// (execution_step.provider), not into {{CONSTRUCTS}}: a plan step has no
	// provider to fill in, so the planner is not offered one.
	Provider() string
	Description() string
	Run(in map[string]string) (map[string]string, error)
}

// Factory creates and caches capabilities by name.
type Factory struct {
	capabilities       []Capability
	capabilitiesByName map[string]Capability
}

func NewFactory() *Factory {
	return &Factory{
		capabilities:       make([]Capability, 0),
		capabilitiesByName: make(map[string]Capability),
	}
}

func (f *Factory) GetAll() []Capability {
	return f.capabilities
}

func (f *Factory) Register(c Capability) {
	name := c.Name()
	if _, exists := f.capabilitiesByName[name]; exists {
		for i, existing := range f.capabilities {
			if existing.Name() == name {
				f.capabilities[i] = c
				break
			}
		}
	} else {
		f.capabilities = append(f.capabilities, c)
	}
	f.capabilitiesByName[name] = c
}

func (f *Factory) Get(name string) Capability {
	return f.capabilitiesByName[name]
}

// Has reports whether a capability name is registered.
func (f *Factory) Has(name string) bool {
	if f == nil {
		return false
	}
	_, ok := f.capabilitiesByName[name]
	return ok
}

// FormatConstructs renders registered capabilities for AGENT_V2 {{CONSTRUCTS}}
// as a JSON array: what the runtime can do, and — for a capability that declares
// them (spec.Declared) — the inputs it takes and the outputs it returns, so the
// planner can call it correctly from this list alone.
//
// The provider is deliberately not part of this list. Who serves a capability is
// the runtime's decision, not something a plan step can carry (a step is a
// capability plus its input), and a plan that schedules on "who" cannot be
// executed. The runtime keeps that attribution where it belongs — on the step it
// recorded (see execution_step.provider / Runtime.providerOf) — rather than
// offering the planner an axis it cannot use.
func (f *Factory) FormatConstructs() string {
	if f == nil || len(f.capabilities) == 0 {
		return "[]"
	}
	type constructJSON struct {
		Name        string       `json:"name"`
		Domain      string       `json:"domain"`
		Description string       `json:"description"`
		Input       []spec.Field `json:"input,omitempty"`
		Output      []spec.Field `json:"output,omitempty"`
	}
	out := make([]constructJSON, 0, len(f.capabilities))
	for _, c := range f.capabilities {
		item := constructJSON{
			Name:        c.Name(),
			Domain:      c.Domain(),
			Description: c.Description(),
		}
		if declared, ok := c.(spec.Declared); ok {
			item.Input = declared.Inputs()
			item.Output = declared.Outputs()
		}
		out = append(out, item)
	}
	// The descriptions name placeholders (<id>, <head/topic branch>), so HTML
	// escaping is turned off: \u003cid\u003e is the same JSON, but it is not what a
	// planner should have to read.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return "[]"
	}
	return strings.TrimRight(buf.String(), "\n")
}
