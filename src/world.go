package autonomy

// World is the observable state of assets.
type World interface {
	Get(assetID string) string
}

// StateWriter is an optional World capability used by UpdateWorld.
type StateWriter interface {
	Set(assetID, state string)
}
