package capability

// Deps are runtime hooks built-in capabilities need from the Autonomy host.
type Deps struct {
	Assets AssetMutator
}

// RegisterDefaults registers all built-in capabilities.
// Add new abilities in this package and register them here.
func RegisterDefaults(f *Factory, deps Deps) {
	if f == nil {
		return
	}
	f.Register(AssetChange{Assets: deps.Assets})
}
