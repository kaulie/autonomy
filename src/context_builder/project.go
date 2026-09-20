package context_builder

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// ProjectRegistryResolver answers a project ref from the platform's project registry:
// the control plane's `GET /api/projects`, which answers
//
//	{"projectId": "project-749a0238", "name": "autonomy",
//	 "gitRepoUrl": "https://github.com/kaulie/autonomy",
//	 "department": {"departmentId": "D0005", "departmentName": "AI研发部"}}
//
// It is where a project's name, repository and organization live (docs/project.md):
// autonomy reads them instead of keeping a second registry. Reads need no UI version
// header — only the platform's writes do.
type ProjectRegistryResolver struct {
	// URL is the registry's base URL; empty: PROJECTS_API_URL, else
	// DefaultProjectsAPIURL.
	URL string
	// TTL and FailureTTL are how long an answer (and a failure) is reused.
	TTL        time.Duration
	FailureTTL time.Duration
	Client     *http.Client

	mu   sync.Mutex
	read cache
}

const (
	// DefaultProjectsAPIURL is where the project registry lives when PROJECTS_API_URL
	// says nothing: the control plane, which serves this service (see the deployment
	// contract).
	DefaultProjectsAPIURL = "http://127.0.0.1:4211"
	// EnvProjectsAPIURL names the project registry.
	EnvProjectsAPIURL = "PROJECTS_API_URL"
)

// NewProjectRegistry is the resolver as the runtime wires it.
func NewProjectRegistry() *ProjectRegistryResolver { return &ProjectRegistryResolver{} }

func (r *ProjectRegistryResolver) Name() string { return "project_registry" }

// BaseURL is where this resolver reads: its own URL when set, else the env var,
// else the local default.
func (r *ProjectRegistryResolver) BaseURL() string {
	if r != nil && r.URL != "" {
		return r.URL
	}
	return envOr(EnvProjectsAPIURL, DefaultProjectsAPIURL)
}

// registryProject is one project as the registry answers it.
type registryProject struct {
	ID         string `json:"projectId"`
	Name       string `json:"name"`
	GitRepoURL string `json:"gitRepoUrl"`
	Department struct {
		ID   string `json:"departmentId"`
		Name string `json:"departmentName"`
	} `json:"department"`
}

func (r *ProjectRegistryResolver) Resolve(ctx context.Context, refType, id string, _ map[string]any) (map[string]any, bool, error) {
	if refType != RefTypeProject {
		// A team is not a project: nothing to say, and not a failure either.
		return nil, false, nil
	}
	project, err := r.project(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if project == nil {
		return nil, false, nil
	}
	fields := map[string]any{}
	if project.Name != "" {
		fields["name"] = project.Name
	}
	if project.GitRepoURL != "" {
		fields["git_repo_url"] = project.GitRepoURL
	}
	if project.Department.ID != "" || project.Department.Name != "" {
		// The organization by id and name; the catalogue itself (its type, its people)
		// is the organization resolver's to add.
		fields["organization"] = map[string]any{
			"id":   project.Department.ID,
			"name": project.Department.Name,
		}
	}
	return fields, true, nil
}

// project returns the project the registry has by this id, or nil when it does not
// know it. The registry is read as one list (that is the shape it serves) and cached.
func (r *ProjectRegistryResolver) project(ctx context.Context, id string) (*registryProject, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.read.fresh(r.TTL, r.FailureTTL) {
		var rows []registryProject
		if err := fetchJSON(ctx, r.Client, r.BaseURL()+"/api/projects", &rows); err != nil {
			r.read = cache{at: time.Now(), ok: false}
			return nil, err
		}
		byID := make(map[string]registryProject, len(rows))
		for _, row := range rows {
			byID[row.ID] = row
		}
		r.read = cache{at: time.Now(), ok: true, data: byID}
	}
	if !r.read.ok {
		return nil, nil
	}
	byID, _ := r.read.data.(map[string]registryProject)
	project, found := byID[id]
	if !found {
		return nil, nil
	}
	return &project, nil
}
