package capability

import (
	"fmt"
	"strings"

	"github.com/kaulie/autonomy/src/capability/spec"
)

// AssetMutator reads/writes asset state for asset.change (injected by autonomy).
type AssetMutator interface {
	GetState(id string) (state string, err error)
	SetState(id, state string) error
}

// AssetChange mutates the task target asset state.
type AssetChange struct {
	Assets AssetMutator
}

func (AssetChange) Name() string { return "asset.change" }

func (AssetChange) Domain() string { return "server" }

func (AssetChange) Provider() string { return "autonomy" }

func (AssetChange) Description() string {
	return `mutate the task target asset state. input: {"target":"<asset id>"} (required: nothing defaults it)`
}

// Inputs / Outputs declare the capability's call signature for {{CONSTRUCTS}}.
func (AssetChange) Inputs() []spec.Field {
	return []spec.Field{
		{Name: "target", Required: true, Description: "the id of the asset whose state to change"},
	}
}

func (AssetChange) Outputs() []spec.Field {
	return []spec.Field{
		{Name: "state", Description: "the state the asset has now — the change this capability made"},
	}
}

func (c AssetChange) Run(in map[string]string) (map[string]string, error) {
	target := strings.TrimSpace(in["target"])
	if target == "" {
		return nil, fmt.Errorf("asset.change: missing target")
	}
	if c.Assets == nil {
		return nil, fmt.Errorf("asset.change: asset mutator not configured")
	}
	if _, err := c.Assets.GetState(target); err != nil {
		return nil, err
	}
	if err := c.Assets.SetState(target, "changed"); err != nil {
		return nil, err
	}
	state, err := c.Assets.GetState(target)
	if err != nil {
		return nil, err
	}
	return map[string]string{"state": state}, nil
}
