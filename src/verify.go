package autonomy

// Verifier checks whether the world satisfies the task contract.
type Verifier interface {
	Verify(task Task, world World) bool
}

// StateVerifier passes when world state of the task asset equals Contract.ExpectedState.
type StateVerifier struct{}

func (StateVerifier) Verify(task Task, world World) bool {
	return world.Get(task.Asset) == task.Contract.ExpectedState
}
