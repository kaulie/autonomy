package autonomy

// World is the observable state of assets.
type World struct {
	assets       map[string]Asset
	assetManager *AssetManager
	events       []Event
}

var _world *World // only one world instance is allowed

func buildWorld() *World {
	if _world != nil {
		return _world
	}
	_world = &World{
		assetManager: NewAssetManager(),
		events:       make([]Event, 0),
	}
	return _world
}

// StateWriter is an optional World capability used by UpdateWorld.
type StateWriter interface {
	Set(assetID, state string)
}

func (w *World) getWorld() *World {
	return _world
}
