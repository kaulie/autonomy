package autonomy

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// TaskProject is the project a task belongs to, and the organization that project
// belongs to — "这条 task 所属的 project，以及 project 所属的组织".
//
// The id is what the task itself said: `context_ref: {"project": "<id>"}`. The rest
// is whatever is known about that id, from the two places that know anything:
//
//   - the runtime's own context containers (this process's world, docs/context.md):
//     name / description / domain of a container someone registered here;
//   - the platform's project registry (the control plane's `GET /api/projects`):
//     the project's name and repository, and the 部门 it belongs to. A project's
//     organization is a fact of the platform, not something autonomy invents —
//     which is why this is a read and not a second registry.
//
// Neither is required: a task detail is not an error because a registry is down,
// and an id nothing knows is still the id.
type TaskProject struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Domain      string `json:"domain,omitempty"`
	// GitRepoURL is the project's repository, as the registry has it: the code the
	// work is about.
	GitRepoURL string `json:"git_repo_url,omitempty"`
	// Organization is the 部门 the project belongs to. Absent when the registry does
	// not know this project (or nobody configured one).
	Organization *TaskOrganization `json:"organization,omitempty"`
}

// TaskOrganization is the organization a project belongs to, as the platform's
// registry names it.
type TaskOrganization struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// TaskProject resolves one project id: this runtime's world first, then the
// platform's registry. It returns nil for an empty id — a task that names no
// project has no project.
func (r *Autonomy) TaskProject(projectID string) *TaskProject {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil
	}
	project := &TaskProject{ID: projectID}
	if r != nil && r.ContextContainerManager != nil {
		if container, ok := r.ContextContainerManager.ContextContainers[projectID]; ok {
			project.Name = container.Name
			project.Description = container.Description
			project.Domain = string(container.DomainType)
		}
	}
	if registered := registryProjectByID(projectID); registered != nil {
		// The registry is the authority on what a project *is* (its name, its
		// repository, its organization); the container is what this process happens
		// to hold about it (the description and domain someone registered here).
		if registered.Name != "" {
			project.Name = registered.Name
		}
		project.GitRepoURL = registered.RepoURL
		if registered.Department.ID != "" || registered.Department.Name != "" {
			project.Organization = &TaskOrganization{
				ID:   registered.Department.ID,
				Name: registered.Department.Name,
			}
		}
	}
	return project
}

// projectRefOf is the project id a task's context names.
func projectRefOf(task *Task) string {
	if task == nil {
		return ""
	}
	return strings.TrimSpace(task.ContextRef[ContextContainerTypeProject])
}

// The platform's project registry (the control plane's GET /api/projects), read
// as the shape it answers: only what a task detail needs.
type registryProject struct {
	ID         string `json:"projectId"`
	Name       string `json:"name"`
	RepoURL    string `json:"gitRepoUrl"`
	Department struct {
		ID   string `json:"departmentId"`
		Name string `json:"departmentName"`
	} `json:"department"`
}

// DefaultProjectsAPIURL is where the project registry lives when PROJECTS_API_URL
// says nothing: the control plane, which serves this service (see the deployment
// contract). Reads need no UI version header — only the platform's writes do.
const DefaultProjectsAPIURL = "http://127.0.0.1:4211"

// EnvProjectsAPIURL names the project registry a task detail reads.
const EnvProjectsAPIURL = "PROJECTS_API_URL"

func projectsAPIURL() string {
	if url := strings.TrimSpace(os.Getenv(EnvProjectsAPIURL)); url != "" {
		return strings.TrimRight(url, "/")
	}
	return DefaultProjectsAPIURL
}

// The registry is read at most once per few seconds: it changes when someone
// creates or renames a project, not per task detail request. A failure is cached
// only briefly, so a registry that comes back is picked up.
const (
	registryTTL        = 30 * time.Second
	registryFailureTTL = 5 * time.Second
	registryTimeout    = 2 * time.Second
)

var registryProjects struct {
	sync.Mutex
	at   time.Time
	ok   bool
	byID map[string]*registryProject
}

// registryProjectByID reads the project out of the platform's registry, or nil
// when it is unreachable or does not know this id. Nothing here is fatal: the
// caller is filling in a task detail.
func registryProjectByID(id string) *registryProject {
	return loadRegistryProjects()[strings.TrimSpace(id)]
}

type registryStatusError int

func (e registryStatusError) Error() string {
	return "project registry answered " + http.StatusText(int(e))
}

func errRegistryStatus(code int) error { return registryStatusError(code) }

func loadRegistryProjects() map[string]*registryProject {
	registryProjects.Lock()
	defer registryProjects.Unlock()
	ttl := registryTTL
	if !registryProjects.ok {
		ttl = registryFailureTTL
	}
	if registryProjects.byID != nil && time.Since(registryProjects.at) < ttl {
		return registryProjects.byID
	}
	projects, err := fetchRegistryProjects(projectsAPIURL())
	if err != nil {
		registryProjects.at = time.Now()
		registryProjects.ok = false
		return nil
	}
	registryProjects.at = time.Now()
	registryProjects.ok = true
	registryProjects.byID = projects
	return projects
}

// forgetRegistryProjects drops the cache (tests, and a caller that just changed a
// project through the platform).
func forgetRegistryProjects() {
	registryProjects.Lock()
	defer registryProjects.Unlock()
	registryProjects.byID = nil
	registryProjects.ok = false
	registryProjects.at = time.Time{}
}

func fetchRegistryProjects(baseURL string) (map[string]*registryProject, error) {
	client := &http.Client{Timeout: registryTimeout}
	resp, err := client.Get(baseURL + "/api/projects")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, errRegistryStatus(resp.StatusCode)
	}
	var rows []registryProject
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	byID := make(map[string]*registryProject, len(rows))
	for i := range rows {
		id := strings.TrimSpace(rows[i].ID)
		if id == "" {
			continue
		}
		byID[id] = &rows[i]
	}
	return byID, nil
}
