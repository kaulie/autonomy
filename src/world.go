package autonomy

// World is the observable state of assets.
type World interface {
	Get(assetID string) string
}
