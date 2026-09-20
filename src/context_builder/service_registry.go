package context_builder

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ServiceRegistryResolver lists the services an organization has registered: a project
// belongs to a department, and which services that department has — each with the
// repository its code lives in — is the service registry's fact
// (`GET /v1/orgs/{orgId}/services`, the org view the platform itself reads).
//
// The scope is two hops, and both come from the ref's own facts: the project registry
// says which department the project belongs to (the organization resolver, asked before
// this one, refines that answer), and that department id is what the service list is
// asked for. A project whose organization nobody knows has no list to look up: nothing
// to say, and not a failure.
//
// What the registry keeps about a service for its own bookkeeping (namespace, owner,
// accounting fields) stays there — a decision cycle needs what the service is and where
// its code is, not who registered it.
type ServiceRegistryResolver struct {
	// URL is the service registry's base URL; empty: SERVICE_REGISTRY_API_URL, else
	// DefaultServiceRegistryAPIURL.
	URL string
	// TTL and FailureTTL are how long an answer (and a failure) is reused.
	TTL        time.Duration
	FailureTTL time.Duration
	Client     *http.Client

	mu   sync.Mutex
	read cache
}

const (
	// DefaultServiceRegistryAPIURL is where the service registry lives when
	// SERVICE_REGISTRY_API_URL says nothing: the 服务中心, where every service of the
	// platform is registered (the same default scripts/register-contract.sh registers
	// with).
	DefaultServiceRegistryAPIURL = "http://127.0.0.1:4240"
	// EnvServiceRegistryAPIURL names the service registry.
	EnvServiceRegistryAPIURL = "SERVICE_REGISTRY_API_URL"
)

// NewServiceRegistry is the resolver as the runtime wires it.
func NewServiceRegistry() *ServiceRegistryResolver { return &ServiceRegistryResolver{} }

func (r *ServiceRegistryResolver) Name() string { return "service_registry" }

// BaseURL is where this resolver reads: its own URL when set, else the env var, else
// the local default.
func (r *ServiceRegistryResolver) BaseURL() string {
	if r != nil && r.URL != "" {
		return r.URL
	}
	return envOr(EnvServiceRegistryAPIURL, DefaultServiceRegistryAPIURL)
}

// registryService is one service as the registry answers it, narrowed to what a
// decision cycle can use.
type registryService struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	GitRepoURL  string `json:"gitRepoUrl"`
	// Version is the revision the registry has for this service: what is deployed of
	// it, as the service itself last registered.
	Version string `json:"version"`
}

// registryOrgServices is the org view's shape. The organization's own name (and whether
// the organization service could be reached at all) is the answer the caller already
// has from the resolvers before this one.
type registryOrgServices struct {
	Services []registryService `json:"services"`
}

func (r *ServiceRegistryResolver) Resolve(ctx context.Context, refType, id string, built map[string]any) (map[string]any, bool, error) {
	if refType != RefTypeProject {
		// A team is not a project: nothing to say, and not a failure either.
		return nil, false, nil
	}
	// Which organization's services to ask for: what the resolvers asked before this one
	// found (the project registry, then the department catalogue that refines it).
	organization, _ := built["organization"].(map[string]any)
	organizationID := stringField(organization["id"])
	if organizationID == "" {
		return nil, false, nil
	}
	services, err := r.services(ctx, organizationID)
	if err != nil {
		return nil, false, err
	}
	if len(services) == 0 {
		// An organization nobody registered a service in has none: silence, not an
		// empty list that reads like an answer.
		return nil, false, nil
	}
	// This answer replaces the organization field, so it carries what was already in it
	// (the id, name and type the earlier resolvers found) plus the services.
	fields := make(map[string]any, len(organization)+1)
	for key, value := range organization {
		fields[key] = value
	}
	fields["services"] = services
	return map[string]any{"organization": fields}, true, nil
}

// services lists an organization's services, remembered per organization: one run
// resolves its ref every cycle, and an organization's services change when someone
// registers one, not per cycle.
func (r *ServiceRegistryResolver) services(ctx context.Context, organizationID string) ([]map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	byOrg, _ := r.read.data.(map[string][]map[string]any)
	if !r.read.fresh(r.TTL, r.FailureTTL) {
		var answer registryOrgServices
		url := r.BaseURL() + "/v1/orgs/" + url.PathEscape(organizationID) + "/services"
		if err := fetchJSON(ctx, r.Client, url, &answer); err != nil {
			r.read = cache{at: time.Now(), ok: false}
			return nil, err
		}
		if byOrg == nil {
			byOrg = map[string][]map[string]any{}
		}
		byOrg[organizationID] = serviceFields(answer.Services)
		r.read = cache{at: time.Now(), ok: true, data: byOrg}
	}
	if !r.read.ok {
		return nil, nil
	}
	return byOrg[organizationID], nil
}

// serviceFields is the list as the prompt carries it: a row with no name is not a
// service anyone can refer to, and a row's empty fields are left out rather than served
// as empty strings.
func serviceFields(services []registryService) []map[string]any {
	fields := make([]map[string]any, 0, len(services))
	for _, service := range services {
		name := strings.TrimSpace(service.Name)
		if name == "" {
			continue
		}
		row := map[string]any{"name": name}
		if service.Description != "" {
			row["description"] = service.Description
		}
		if service.GitRepoURL != "" {
			row["git_repo_url"] = service.GitRepoURL
		}
		if service.Version != "" {
			row["version"] = service.Version
		}
		fields = append(fields, row)
	}
	return fields
}
