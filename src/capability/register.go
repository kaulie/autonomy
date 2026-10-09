package capability

import (
	"github.com/kaulie/autonomy/src/capability/broker"
	ctxcap "github.com/kaulie/autonomy/src/capability/contextcap"
	"github.com/kaulie/autonomy/src/capability/deployment"
	sd "github.com/kaulie/autonomy/src/capability/software_development"
	ctxsvc "github.com/kaulie/autonomy/src/context"
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
	// Context is the Context Service the context.search / get / list
	// capabilities call. Optional: when nil those capabilities are still
	// registered for the planner but report a typed "not configured" error.
	Context ctxsvc.Service
	// PRWatches is the background pull-request watcher pr.watch registers
	// with. Optional: without it the capability still snapshots (and can
	// poll in-call) but does not emit later world events.
	PRWatches sd.WatchRegistrar
}

// RegisterDefaults registers all built-in capabilities.
// Add new abilities under domain subpackages and register them here.
func RegisterDefaults(f *Factory, deps Deps) {
	if f == nil {
		return
	}
	f.Register(AssetChange{Assets: deps.Assets})
	f.Register(sd.CodeEdit{Agents: deps.Agents})
	// service.deploy talks to the deployment control plane over HTTP, so it needs
	// no host hook: DEPLOYMENT_API_URL (or the local default) is its wiring.
	f.Register(sd.DeployService{})
	// pull_request.review reads a pull request's review opinions — named by its
	// URL or by the branch pair it was opened from. It does NOT merge; landing is
	// left to a human. Like service.deploy it is code over someone else's API
	// (GitHub's REST API) rather than a delegation, so its wiring is the
	// credential (GITHUB_TOKEN / GH_TOKEN, else the gh CLI's own — see
	// github_credential.go) plus GITHUB_API_URL, and no host hook is needed.
	f.Register(sd.PullRequestReview{})
	// pr.check (PR_Check) is the system verification tool: does this PR exist,
	// and what state is it in. Review opinions stay on pull_request.review.
	f.Register(sd.PRCheck{})
	// pr.watch observes a pull request until it is merged (default) and, via
	// the event gateway, wakes the task's agent so it can continue. It does
	// not merge; pr.check remains the verifier.
	f.Register(sd.PRWatch{Watches: deps.PRWatches})
	// The monitor prefers the agent-backed observer when the host provides an
	// agent broker; Deps.Deployments pins a specific (deterministic) source.
	f.Register(deployment.Monitor{Observer: deps.Deployments, Agents: deps.Agents})
	// The Context Service's agent-facing surface (spec 13): an agent discovers
	// project context through these instead of SQL. Deps.Context is the service.
	f.Register(ctxcap.Search{Svc: deps.Context})
	f.Register(ctxcap.Get{Svc: deps.Context})
	f.Register(ctxcap.List{Svc: deps.Context})
}
