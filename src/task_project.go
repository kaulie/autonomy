package autonomy

import (
	"context"
	"strings"

	"github.com/kaulie/autonomy/src/context_builder"
)

// TaskProject is the project a task belongs to, and the organization that project
// belongs to — "这条 task 所属的 project，以及 project 所属的组织".
//
// It is a view of the same resolution a decision cycle gets
// (src/context_resolver.go, src/context_builder): the task's
// `context_ref: {"project": "<id>"}` resolved through the runtime's own world (a
// container registered here) and the platform's registries (the project registry, and
// the organization catalogue that names the department the project belongs to). The id
// is always what the task itself said; everything else is whatever was found.
type TaskProject struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Domain      string `json:"domain,omitempty"`
	// GitRepoURL is the project's repository *when the registry hands one over*
	// (`gitRepoUrl` on the project registry's row). It is a stable key, deliberately
	// always present, and empty when nobody says: the platform's project registry no
	// longer carries a repository, and autonomy does not go looking for one on this
	// path — a task's repository, when there is one, is part of its *world* (the
	// organization's services each name their repo, src/context_builder), not of the
	// project row. A consumer that renders "这条任务的世界" therefore shows what it
	// has and does not wait for this field.
	GitRepoURL string `json:"git_repo_url"`
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

// TaskProject resolves one project id the way a cycle's prompt does. It returns nil for
// an empty id — a task that names no project has no project.
func (r *Autonomy) TaskProject(projectID string) *TaskProject {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil
	}
	var containers *ContextContainerManager
	if r != nil {
		containers = r.ContextContainerManager
	}
	ref := context_builder.Ref{context_builder.RefTypeProject: projectID}
	fields := contextSectionFields(r.buildContextSections(ref), containers, context_builder.RefTypeProject, projectID)
	return taskProjectOf(fields)
}

// taskProjectOf is the project a resolved section answers: the id it was resolved as, and
// whatever the process's world and the platform's registries know about it.
func taskProjectOf(fields map[string]any) *TaskProject {
	if len(fields) == 0 {
		return nil
	}
	project := &TaskProject{
		ID:          contextSectionString(fields["id"]),
		Name:        contextSectionString(fields["name"]),
		Description: contextSectionString(fields["description"]),
		Domain:      contextSectionString(fields["domain"]),
		GitRepoURL:  contextSectionString(fields["git_repo_url"]),
	}
	if organization, ok := fields["organization"].(map[string]any); ok {
		project.Organization = &TaskOrganization{
			ID:   contextSectionString(organization["id"]),
			Name: contextSectionString(organization["name"]),
		}
	}
	return project
}

// TaskProjectOf is the project the *task's own context_ref* resolves to: the project the
// task names itself, or — when the ref names a task instead — the project that resolution
// expanded to, because a task's world is the world written on the row it names. It is the
// same resolution a decision cycle gets (fillContextSections), so the detail and the prompt
// answer alike; with no builder (a test, or AUTONOMY_CONTEXT_BUILDER=0) it falls back to
// the project the task's ref names, answered from this process's own world.
func (r *Autonomy) TaskProjectOf(task *Task) *TaskProject {
	if task == nil {
		return nil
	}
	sections := r.buildContextSections(contextRefOf(task))
	if section, ok := sections[context_builder.RefTypeProject]; ok && section != nil {
		return taskProjectOf(section)
	}
	return r.TaskProject(projectRefOf(task))
}

// buildContextSections resolves a ref now: the task detail's way in. A decision cycle
// resolves the same thing before its prompt is rendered (fillContextSections), so both
// answer from one resolution of the same ref.
func (r *Autonomy) buildContextSections(ref context_builder.Ref) map[string]map[string]any {
	if r == nil || r.ContextBuilder == nil {
		return nil
	}
	return r.ContextBuilder.Build(context.Background(), ref).Sections
}

// projectRefOf is the project id a task's context names.
func projectRefOf(task *Task) string {
	if task == nil {
		return ""
	}
	return strings.TrimSpace(task.ContextRef[ContextContainerTypeProject])
}
