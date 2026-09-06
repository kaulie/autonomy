package autonomy

import "fmt"

// Action is one concrete execution of a capability against a target.
type Action interface {
	Execute(ctx DecisionContext) error
}

type NothingAction struct {
}

func (a NothingAction) Execute(ctx DecisionContext) error {
	fmt.Println("NothingAction: Execute")
	return nil
}

type SimpleAction struct {
}

func (a SimpleAction) Execute(ctx DecisionContext) error {
	assetID := ctx.Task.Target
	asset, err := _world.getWorld().assetManager.Get(assetID)
	if err != nil {
		return err
	}
	asset.State = "changed"
	_world.getWorld().assetManager.Set(assetID, asset) // must write back to world, cause asset is just a local copy
	fmt.Println("SimpleAction: Execute: ", asset.State)
	return nil
}
