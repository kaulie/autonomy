package autonomy

import "fmt"

// Verifier checks whether the world satisfies the task contract.
type Verifier struct {
	// Verify(task Task, world *World) bool
}

func NewVerifier() *Verifier {
	return &Verifier{}
}

// StateVerifier passes when world state of the task target equals Contract.ExpectedState.
// type StateVerifier struct{}

func (v *Verifier) Verify(task *Task, world *World) bool {
	fmt.Println("Verifier: Verify: ", task.Target, ", ", task.Contract.ExpectedState)
	asset, err := world.assetManager.Get(task.Target)
	fmt.Println("Verifier: Asset: ", asset.State)
	if err != nil {
		return false
	}
	return asset.State == task.Contract.ExpectedState
}
