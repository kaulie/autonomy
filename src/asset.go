package autonomy

import "fmt"

// Asset is something in the world that can be observed, referenced, or changed.
type Asset struct {
	ID    string
	Kind  string
	State string
}

type AssetManager struct {
	assets     []Asset
	assetsByID map[string]Asset
}

func NewAssetManager() *AssetManager {

	//asset registry
	assets := []Asset{}
	assetsByID := make(map[string]Asset)
	for _, asset := range assets {
		assetsByID[asset.ID] = asset
	}
	return &AssetManager{
		assets:     assets,
		assetsByID: assetsByID,
	}
}

func (m *AssetManager) Get(id string) (Asset, error) {
	asset, ok := m.assetsByID[id]
	if !ok {
		return Asset{}, fmt.Errorf("asset %s not found", id)
	}
	return asset, nil
}

func (m *AssetManager) Set(id string, asset Asset) {
	m.assetsByID[id] = asset
}

// List returns a snapshot of registered assets in registration order.
func (m *AssetManager) List() []Asset {
	out := make([]Asset, 0, len(m.assets))
	for _, a := range m.assets {
		if cur, ok := m.assetsByID[a.ID]; ok {
			out = append(out, cur)
			continue
		}
		out = append(out, a)
	}
	return out
}
