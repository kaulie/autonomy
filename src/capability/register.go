package capability

import (
	"github.com/kaulie/autonomy/src/capability/broker"
	"github.com/kaulie/autonomy/src/capability/deployment"
	sd "github.com/kaulie/autonomy/src/capability/software_development"
)

// Deps are runtime hooks built-in capabilities need from the Autonomy host.
type Deps struct {
	Assets AssetMutator
	// Agents is Runtime (or a test double): capabilities acquire agents through it.
	Agents broker.AgentBroker
	// Deployments is the deployment state source deployment.monitor follows.
	// Optional: when nil the capability builds its own HTTP observer from the
	// request (or $AUTONOMY_DEPLOYMENT_ENDPOINT).
	Deployments deployment.Observer
}

// RegisterDefaults registers all built-in capabilities.
// Add new abilities under domain subpackages and register them here.
func RegisterDefaults(f *Factory, deps Deps) {
	if f == nil {
		return
	}
	f.Register(AssetChange{Assets: deps.Assets})
	f.Register(sd.CodeEdit{Agents: deps.Agents})
	// The monitor prefers the agent-backed observer when the host provides an
	// agent broker; Deps.Deployments pins a specific (deterministic) source.
	f.Register(deployment.Monitor{Observer: deps.Deployments, Agents: deps.Agents})
}
