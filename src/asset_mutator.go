package autonomy

import "fmt"

// assetManagerMutator adapts AssetManager to capability.AssetMutator.
type assetManagerMutator struct {
	m *AssetManager
}

func (a assetManagerMutator) GetState(id string) (string, error) {
	if a.m == nil {
		return "", fmt.Errorf("asset manager not ready")
	}
	asset, err := a.m.Get(id)
	if err != nil {
		return "", err
	}
	return asset.State, nil
}

func (a assetManagerMutator) SetState(id, state string) error {
	if a.m == nil {
		return fmt.Errorf("asset manager not ready")
	}
	asset, err := a.m.Get(id)
	if err != nil {
		return err
	}
	asset.State = state
	a.m.Set(id, asset)
	return nil
}

func worldAssetMutator() assetManagerMutator {
	if _world == nil {
		return assetManagerMutator{}
	}
	return assetManagerMutator{m: _world.assetManager}
}
