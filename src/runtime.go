package autonomy

import "github.com/kaulie/autonomy/src/capability"

// Runtime reliably executes actions. It does not decide what to do next.
type Runtime struct {
	caps  map[string]capability.Capability
	world *World
}

func NewRuntime(caps ...capability.Capability) *Runtime {
	m := make(map[string]capability.Capability, len(caps))
	for _, c := range caps {
		m[c.Name()] = c
	}
	return &Runtime{caps: m}
}

func (r *Runtime) Execute(decision Decision) (Result, error) {
	action := decision.Action
	err := action.Execute(decision.Ctx)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Message: "Action executed",
	}, nil
}
