package autonomy

import "fmt"

// Runtime looks up capabilities by name and runs them.
type Runtime struct {
	caps map[string]Capability
}

func NewRuntime(caps ...Capability) *Runtime {
	m := make(map[string]Capability, len(caps))
	for _, c := range caps {
		m[c.Name()] = c
	}
	return &Runtime{caps: m}
}

func (r *Runtime) Run(step Step) (map[string]string, error) {
	c, ok := r.caps[step.Capability]
	if !ok {
		return nil, fmt.Errorf("unknown capability %q", step.Capability)
	}
	return c.Run(step.Input)
}
