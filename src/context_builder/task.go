package context_builder

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// TaskRegistryResolver answers a task ref from the platform's task registry: the control
// plane's `GET /api/tasks/{taskId}`, which answers
//
//	{"task":    {"taskId": "task-2ecd5e15ae3047f0", "projectId": "project-749a0238",
//	             "title": "…", "description": "…", "taskType": "general", "goal": "merge", …},
//	 "project": {"projectId": "project-749a0238", "name": "autonomy",
//	             "gitRepoUrl": "https://github.com/kaulie/autonomy",
//	             "department": {"departmentId": "D0005", "departmentName": "AI研发部"}}}
//
// It is how a task id becomes a world: what the task is (title / description / goal / task
// type), and which project it is in — the same facts the platform's own task page reads.
// The project itself is not answered here: this resolver names it as a further ref
// (ExtraRefs), so the project's own resolvers answer it exactly as they answer a project
// the task named itself — its name and repository, its organization, and that
// organization's services.
//
// The platform's bookkeeping about a task (workspace, provider, model, agent id, PR link,
// timestamps, token stats) deliberately stays there: a decision cycle needs what the task
// is and where its world is, and the rest is the panel's.
type TaskRegistryResolver struct {
	// URL is the task registry's base URL; empty: TASKS_API_URL, else the control plane
	// (DefaultProjectsAPIURL) — the platform serves this registry and the project one.
	URL string
	// TTL and FailureTTL are how long an answer (and a failure) is reused.
	TTL        time.Duration
	FailureTTL time.Duration
	Client     *http.Client

	mu   sync.Mutex
	read cache
}

// EnvTaskRegistryAPIURL names the task registry. Its default is the control plane's own
// address (DefaultProjectsAPIURL): `GET /api/tasks/{id}` and `GET /api/projects` are the
// same service.
const EnvTaskRegistryAPIURL = "TASKS_API_URL"

// NewTaskRegistry is the resolver as the runtime wires it.
func NewTaskRegistry() *TaskRegistryResolver { return &TaskRegistryResolver{} }

func (r *TaskRegistryResolver) Name() string { return "task_registry" }

// BaseURL is where this resolver reads: its own URL when set, else the env var, else the
// control plane.
func (r *TaskRegistryResolver) BaseURL() string {
	if r != nil && r.URL != "" {
		return r.URL
	}
	return envOr(EnvTaskRegistryAPIURL, DefaultProjectsAPIURL)
}

// registryTask is one task as the platform answers it, narrowed to what a decision cycle
// can use.
type registryTask struct {
	ID          string `json:"taskId"`
	ProjectID   string `json:"projectId"`
	Title       string `json:"title"`
	Description string `json:"description"`
	TaskType    string `json:"taskType"`
	Goal        string `json:"goal"`
}

// registryTaskDetail is the detail's shape: the task, and the project it is in.
type registryTaskDetail struct {
	Task    registryTask `json:"task"`
	Project struct {
		ID string `json:"projectId"`
	} `json:"project"`
}

func (r *TaskRegistryResolver) Resolve(ctx context.Context, refType, id string, _ map[string]any) (map[string]any, bool, error) {
	if refType != RefTypeTask {
		// A project is not a task: nothing to say, and not a failure either.
		return nil, false, nil
	}
	task, err := r.task(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if task == nil {
		return nil, false, nil
	}
	fields := map[string]any{}
	for key, value := range map[string]string{
		"title": task.Title, "description": task.Description,
		"goal": task.Goal, "task_type": task.TaskType,
	} {
		if value != "" {
			fields[key] = value
		}
	}
	return fields, true, nil
}

// ExtraRefs names the project that task's world is in, so the project resolvers answer it
// (and, in turn, the organization, and that organization's services). The row is already
// in hand — what Resolve read is cached — so naming it costs no further read.
func (r *TaskRegistryResolver) ExtraRefs(_ context.Context, refType, id string, _ map[string]any) Ref {
	if refType != RefTypeTask {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.read.ok {
		return nil
	}
	byID, _ := r.read.data.(map[string]registryTask)
	projectID := byID[id].ProjectID
	if projectID == "" {
		return nil
	}
	return Ref{RefTypeProject: projectID}
}

// task returns the task the registry has by this id, or nil when it does not know it.
// One task is read per call (that is the shape the registry serves by id), and answers —
// including "no such task" — are cached.
func (r *TaskRegistryResolver) task(ctx context.Context, id string) (*registryTask, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	byID, _ := r.read.data.(map[string]registryTask)
	if !r.read.fresh(r.TTL, r.FailureTTL) {
		var detail registryTaskDetail
		url := r.BaseURL() + "/api/tasks/" + url.PathEscape(id)
		found, err := fetchJSONFound(ctx, r.Client, url, &detail)
		if err != nil {
			r.read = cache{at: time.Now(), ok: false}
			return nil, err
		}
		if byID == nil {
			byID = map[string]registryTask{}
		}
		if found {
			task := detail.Task
			if task.ID == "" {
				task.ID = id
			}
			if task.ProjectID == "" {
				// A row without the project column, with the project in the detail.
				task.ProjectID = detail.Project.ID
			}
			byID[id] = task
		}
		// "Nobody has this one" is an answer too, and a successful read: cached with the
		// rest, because a task that does not exist is not a registry that is down.
		r.read = cache{at: time.Now(), ok: true, data: byID}
	}
	if !r.read.ok {
		return nil, nil
	}
	task, found := byID[id]
	if !found {
		return nil, nil
	}
	return &task, nil
}
