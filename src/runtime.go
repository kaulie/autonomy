package autonomy

// Runtime reliably executes actions. It does not decide what to do next.
type Runtime struct {
	caps  map[string]Capability
	world *World
}

func NewRuntime(caps ...Capability) *Runtime {
	m := make(map[string]Capability, len(caps))
	for _, c := range caps {
		m[c.Name()] = c
	}
	return &Runtime{caps: m}
}

func (r *Runtime) Execute(decision Decision) (Result, error) {
	// fmt.Println("Executing decision: ", decision)
	action := decision.Action
	err := action.Execute(decision.Ctx)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Message: "Action executed",
	}, nil
}
