package autonomy

import "fmt"

// Runtime reliably executes actions. It does not decide what to do next.
type Runtime interface {
	Execute(action Action) (map[string]string, error)
}

// LocalRuntime looks up capabilities by name and runs them in-process.
type LocalRuntime struct {
	caps map[string]Capability
}

func NewRuntime(caps ...Capability) *LocalRuntime {
	m := make(map[string]Capability, len(caps))
	for _, c := range caps {
		m[c.Name()] = c
	}
	return &LocalRuntime{caps: m}
}

func (r *LocalRuntime) Execute(action Action) (map[string]string, error) {
	c, ok := r.caps[action.Capability]
	if !ok {
		return nil, fmt.Errorf("unknown capability %q", action.Capability)
	}
	return c.Run(action.Input)
}
