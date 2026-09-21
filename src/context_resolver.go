package autonomy

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kaulie/autonomy/src/context_builder"
)

// This file is the runtime's side of the context builder (src/context_builder): the
// resolver only this process can answer — the world someone registered here — the
// wiring of the whole chain, and the hook that resolves a task's context_ref before
// its cycle's prompt is built.

// contextWorldResolver answers from this process's own world: the context containers
// someone registered here (RegisterContextContainer, src/binding.go). It is the one
// resolver the runtime brings; what the platform's registries add comes from the
// module's own resolvers, and because this one is asked first, a registry's answer wins
// on a field both know (a project's name) while a field only this process has (a
// description someone registered) survives.
type contextWorldResolver struct{ autonomy *Autonomy }

func (r contextWorldResolver) Name() string { return "world" }

func (r contextWorldResolver) Resolve(_ context.Context, _, id string, _ map[string]any) (map[string]any, bool, error) {
	if r.autonomy == nil || r.autonomy.ContextContainerManager == nil {
		return nil, false, nil
	}
	container, ok := r.autonomy.ContextContainerManager.ContextContainers[id]
	if !ok {
		return nil, false, nil
	}
	fields := map[string]any{}
	if container.Name != "" {
		fields["name"] = container.Name
	}
	if container.Description != "" {
		fields["description"] = container.Description
	}
	if container.DomainType != "" {
		fields["domain"] = string(container.DomainType)
	}
	return fields, true, nil
}

// newContextBuilder is the runtime's resolver chain: this process's own world and its own
// task rows, then the platform's registries — the project registry (name, repository, the
// project's organization), the organization catalogue (the department itself), the service
// registry (the services that department has, each with its repository) and the task
// registry (a task id the panel owns: what it is, and which project it is in). Order is the
// pipeline: a task ref is expanded into the project ref the platform answered, so the two
// resolvers after it answer that project. It returns nil when the builder is switched off
// (AUTONOMY_CONTEXT_BUILDER=0) — an offline deployment then answers from what the process
// knows, exactly as it did before there was a builder.
func newContextBuilder(autonomy *Autonomy) *context_builder.Builder {
	if !contextBuilderEnabled() {
		return nil
	}
	return context_builder.New(
		contextWorldResolver{autonomy: autonomy},
		contextTaskResolver{autonomy: autonomy},
		context_builder.NewProjectRegistry(),
		context_builder.NewOrganization(),
		context_builder.NewServiceRegistry(),
		context_builder.NewTaskRegistry(),
	).WithTimeout(contextBuilderTimeout())
}

// EnvContextBuilder switches the context builder off ("0", "off", "false", "no"):
// nothing is resolved from the platform's registries, and a prompt carries only what
// this process knows.
const EnvContextBuilder = "AUTONOMY_CONTEXT_BUILDER"

// EnvContextTimeout bounds one resolver call (a Go duration, default 3s).
const EnvContextTimeout = "AUTONOMY_CONTEXT_TIMEOUT"

func contextBuilderEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvContextBuilder))) {
	case "0", "off", "false", "no":
		return false
	}
	return true
}

func contextBuilderTimeout() time.Duration {
	if raw := strings.TrimSpace(os.Getenv(EnvContextTimeout)); raw != "" {
		if budget, err := time.ParseDuration(raw); err == nil && budget > 0 {
			return budget
		}
	}
	return context_builder.DefaultTimeout
}

// contextTaskResolver answers a task ref from this process's own store: a task's world is
// written on its row (`tasks.context_ref`, docs/task.md), so the task that names another
// task gets the world that row names. What it contributes is the task's own facts
// (description, goal type); the project is named as a further ref (ExtraRefs), so the
// platform's project registry answers it — the same project resolution a project ref gets.
//
// The platform's task registry answers the ids this process does not have (a task the
// panel owns and this runtime never ran): registration order is the pipeline, so a row
// here is answered first, and the platform's answer fills in whatever it says the same id
// is.
type contextTaskResolver struct{ autonomy *Autonomy }

func (r contextTaskResolver) Name() string { return "task_store" }

func (r contextTaskResolver) Resolve(_ context.Context, refType, id string, _ map[string]any) (map[string]any, bool, error) {
	task, err := r.task(refType, id)
	if err != nil {
		return nil, false, err
	}
	if task == nil {
		return nil, false, nil
	}
	fields := map[string]any{}
	if task.Description != "" {
		fields["description"] = task.Description
	}
	if task.GoalType != "" {
		fields["goal_type"] = string(task.GoalType)
	}
	return fields, true, nil
}

// ExtraRefs names the project the task's own row points at: that is what "this task shares
// that task's world" means for a ref that names a task.
func (r contextTaskResolver) ExtraRefs(_ context.Context, refType, id string, _ map[string]any) context_builder.Ref {
	task, err := r.task(refType, id)
	if err != nil || task == nil {
		return nil
	}
	projectID := projectRefOf(task)
	if projectID == "" {
		return nil
	}
	return context_builder.Ref{context_builder.RefTypeProject: projectID}
}

// task reads one of this process's task rows for a task ref. A task this process does not
// have is not an error: the platform's registry may still know it.
//
// The read is taken from the writer: the resolver runs inside a run, on a task this
// runtime has just queued, and resolving a task to a project from a row that has not
// replicated yet would resolve the run's world to nothing (docs/store.md「读写分离」).
func (r contextTaskResolver) task(refType, id string) (*Task, error) {
	if refType != context_builder.RefTypeTask || r.autonomy == nil || r.autonomy.Store == nil {
		return nil, nil
	}
	return writerReads(r.autonomy.taskStore()).GetTask(id)
}

// activeContextBuilder is the builder this process resolves context with: nil when the
// runtime was assembled by hand (a test) or the builder is switched off.
func activeContextBuilder() *context_builder.Builder {
	if _autonomy == nil {
		return nil
	}
	return _autonomy.ContextBuilder
}

// fillContextSections resolves the task's context_ref before this cycle's prompt is
// built: which containers the task names, and what this process and the platform's
// registries know about them. A decision cycle is where that belongs — the prompt is
// rendered from this context, so it is fetched first — and it cannot fail one: what
// could not be resolved is simply not in the prompt.
func fillContextSections(decision *DecisionContext) {
	if decision == nil || decision.Task == nil || decision.ContextSections != nil {
		return
	}
	builder := activeContextBuilder()
	if builder == nil {
		return
	}
	ref := contextRefOf(decision.Task)
	if len(ref) == 0 {
		return
	}
	result := builder.Build(decision.Context, ref)
	decision.ContextSections = result.Sections
	reportContextErrors(result.Errors)
}

// contextRefOf is a task's context_ref as the builder speaks it.
func contextRefOf(task *Task) context_builder.Ref {
	ref := context_builder.Ref{}
	if task == nil {
		return ref
	}
	for refType, id := range task.ContextRef {
		if strings.TrimSpace(id) == "" {
			continue
		}
		ref[string(refType)] = id
	}
	return ref
}

// contextSectionFields is what one container a task named resolves to: what the builder
// found (sections), or — with no builder answer — what this process's own world has
// registered. The prompt's context_entity block and the task detail's project are the
// same answer (src/prompt.go, src/task_project.go).
func contextSectionFields(sections map[string]map[string]any, containers *ContextContainerManager, refType, id string) map[string]any {
	if section, ok := sections[refType]; ok && section != nil {
		return section
	}
	fields := map[string]any{"id": id, "type": refType}
	if containers == nil {
		return fields
	}
	if container, ok := containers.ContextContainers[id]; ok {
		if container.Name != "" {
			fields["name"] = container.Name
		}
		if container.Description != "" {
			fields["description"] = container.Description
		}
		if container.DomainType != "" {
			fields["domain"] = string(container.DomainType)
		}
	}
	return fields
}

// contextSectionString reads a string field out of a resolved section: what the builder
// merged there (src/context_builder), or a field this file wrote itself.
func contextSectionString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

// reportedContextErrors keeps a registry that is down from printing on every cycle:
// the first time the builder says something failed is the interesting one.
var reportedContextErrors sync.Map

func reportContextErrors(errors []error) {
	for _, err := range errors {
		if err == nil {
			continue
		}
		message := err.Error()
		if _, seen := reportedContextErrors.LoadOrStore(message, true); seen {
			continue
		}
		fmt.Fprintf(os.Stderr, "[autonomy] context: %s\n", message)
	}
}
