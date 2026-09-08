package capability

import (
	sd "github.com/kaulie/autonomy/src/capability/software_development"
)

// Deps are runtime hooks built-in capabilities need from the Autonomy host.
type Deps struct {
	Assets AssetMutator
	Editor sd.CursorRunner
}

// RegisterDefaults registers all built-in capabilities.
// Add new abilities under domain subpackages and register them here.
func RegisterDefaults(f *Factory, deps Deps) {
	if f == nil {
		return
	}
	f.Register(AssetChange{Assets: deps.Assets})
	f.Register(sd.CodeEdit{Runner: deps.Editor})
}
