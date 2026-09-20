package context_builder

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// OrganizationResolver fills in the organization a project belongs to: the department
// catalogue lives in the organization service (`GET /api/v1/departments/{id}`, which
// answers id / name / type / members), and the project registry only references it by
// id. It reads the department id the project resolver put in `built` — resolvers are
// asked in registration order, which is the pipeline.
//
// The members the catalogue has are deliberately not injected: who works in a
// department is an identity question (the organization service owns it), not something
// every decision cycle needs.
type OrganizationResolver struct {
	// URL is the organization service's base URL; empty: ORGANIZATION_API_URL, else
	// DefaultOrganizationAPIURL.
	URL string
	// TTL and FailureTTL are how long an answer (and a failure) is reused.
	TTL        time.Duration
	FailureTTL time.Duration
	Client     *http.Client

	mu   sync.Mutex
	read cache
}

const (
	// DefaultOrganizationAPIURL is where the organization service lives when
	// ORGANIZATION_API_URL says nothing (the same default the control plane uses for
	// its own department picker).
	DefaultOrganizationAPIURL = "http://127.0.0.1:4244"
	// EnvOrganizationAPIURL names the organization service.
	EnvOrganizationAPIURL = "ORGANIZATION_API_URL"
)

// NewOrganization is the resolver as the runtime wires it.
func NewOrganization() *OrganizationResolver { return &OrganizationResolver{} }

func (r *OrganizationResolver) Name() string { return "organization" }

// BaseURL is where this resolver reads: its own URL when set, else the env var,
// else the local default.
func (r *OrganizationResolver) BaseURL() string {
	if r != nil && r.URL != "" {
		return r.URL
	}
	return envOr(EnvOrganizationAPIURL, DefaultOrganizationAPIURL)
}

// Department is one department as the organization catalogue has it.
type Department struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func (r *OrganizationResolver) Resolve(ctx context.Context, refType, id string, built map[string]any) (map[string]any, bool, error) {
	if refType != RefTypeProject {
		return nil, false, nil
	}
	// The department of the project, as the resolver asked before this one found it.
	organization, _ := built["organization"].(map[string]any)
	departmentID := stringField(organization["id"])
	if departmentID == "" {
		return nil, false, nil
	}
	department, err := r.department(ctx, departmentID)
	if err != nil {
		return nil, false, err
	}
	if department == nil {
		return nil, false, nil
	}
	fields := map[string]any{"id": department.ID, "name": department.Name}
	if department.Name == "" {
		fields["name"] = stringField(organization["name"])
	}
	if department.Type != "" {
		fields["type"] = department.Type
	}
	// The whole object is replaced: the catalogue's answer is the better one, and the
	// project registry's name survives inside it when the catalogue has none.
	return map[string]any{"organization": fields}, true, nil
}

func (r *OrganizationResolver) department(ctx context.Context, id string) (*Department, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	byID, _ := r.read.data.(map[string]Department)
	if !r.read.fresh(r.TTL, r.FailureTTL) {
		var department Department
		if err := fetchJSON(ctx, r.Client, r.BaseURL()+"/api/v1/departments/"+id, &department); err != nil {
			r.read = cache{at: time.Now(), ok: false}
			return nil, err
		}
		if byID == nil {
			byID = map[string]Department{}
		}
		byID[id] = department
		r.read = cache{at: time.Now(), ok: true, data: byID}
	}
	if !r.read.ok {
		return nil, nil
	}
	department, found := byID[id]
	if !found {
		return nil, nil
	}
	return &department, nil
}
