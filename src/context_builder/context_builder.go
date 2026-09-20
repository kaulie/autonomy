// Package context_builder resolves a task's context_ref into the facts a decision
// cycle should see, before the prompt is rendered.
//
// A task says which world it is in by reference — `context_ref: {"project":
// "project-749a0238"}` (docs/task.md, docs/context.md) — and a reference is only
// useful if something resolves it. This package is that resolver, on its own: it
// knows nothing about tasks, agents, prompts or the runtime. It takes a Ref and asks
// every Resolver what it knows about each container the Ref names.
//
//	ref := context_builder.Ref{"project": "project-749a0238"}
//	result := builder.Build(ctx, ref)
//	// result.Sections["project"] = {id, type, name, git_repo_url, organization{…}}
//
// The runtime supplies the resolver for its own world (the context containers
// someone registered in this process, src/context_resolver.go) and this package
// supplies the ones that ask the platform's registries (the project registry, the
// organization catalogue). Nothing here can fail a caller: a resolver that errors or
// runs out of time contributes nothing — the error is reported alongside — and what
// the others found still stands. The ref's own id and type are always in the result,
// because they are what the task said.
package context_builder

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Ref types a task's context can name. They are the vocabulary of the reference
// (docs/context.md); a task may name a world this package has no resolver for, and
// that is not an error — the ref is still reported.
const (
	RefTypeProject = "project"
	RefTypeTeam    = "team"
	// RefTypeTask names a task whose world this task shares: what that task is, and —
	// because a task's world is the world it names — the project it is in, with that
	// project's organization and services (docs/task.md, docs/context.md).
	RefTypeTask = "task"
)

// Ref is a task's context_ref: the container type -> the id it names.
type Ref map[string]string

// Resolver knows things about the containers a task named. It is asked once per
// (ref type, id) pair the Ref carries.
//
// ok=false means "nothing to say about this one", which is not a failure; an error is
// recorded and the build goes on. built is what the resolvers asked before this one
// found for the same ref, already merged — so a resolver can build on another's
// answer, which is how the organization resolver learns a project's department id.
type Resolver interface {
	Name() string
	Resolve(ctx context.Context, refType, id string, built map[string]any) (fields map[string]any, ok bool, err error)
}

// Expander is a Resolver that also names the refs a container's world is made of. A task
// is the case this exists for: a task says which world it is in on its own row, so a ref
// that names a task is a ref that names a project too — and then that project's
// organization, and that organization's services, are the same resolution a project ref
// gets (docs/context-builder.md).
//
// What an expander names is resolved with the same resolver chain, so its sections appear
// next to the ref that named it. It is **one hop**: what an expansion names is resolved
// but not expanded again (a ref names a handful of containers, not a graph), and a ref
// already resolved is never resolved twice — so a chain of references cannot loop. An
// expander that cannot name a further referee names none; it returns no error, because
// naming one is not what the section promised.
type Expander interface {
	ExtraRefs(ctx context.Context, refType, id string, fields map[string]any) Ref
}

// maxContextRefs bounds one build. A ref names a container, an expansion names a handful
// more — the cap is a safety valve, not a limit a resolution should reach.
const maxContextRefs = 8

// pendingRef is one ref to resolve: the ones the caller named (which may expand), then the
// ones those named (which may not).
type pendingRef struct {
	refType string
	id      string
	expand  bool
}

// Result is what a build found.
type Result struct {
	// Sections is the ref type -> the fields known about the container it names,
	// including the ref's own id and type. Empty for a Ref that names nothing.
	Sections map[string]map[string]any
	// Errors is what resolvers said while failing. They are not fatal, but they are
	// the reason a section is thinner than it could be.
	Errors []error
}

// Builder runs the resolvers in order for each ref.
type Builder struct {
	resolvers []Resolver
	timeout   time.Duration
}

// DefaultTimeout is what one resolver call may take: a registry is a lookup, not a
// dependency, and a decision cycle will not wait on one.
const DefaultTimeout = 3 * time.Second

// New builds a resolver chain. The order is the order resolvers are asked, and a
// later answer wins per field: a platform registry's name beats this process's copy of
// it, while a field only the process knows (a description someone registered) is left
// alone.
func New(resolvers ...Resolver) *Builder {
	return &Builder{resolvers: resolvers, timeout: DefaultTimeout}
}

// WithTimeout bounds each resolver call, and returns the builder for chaining.
func (b *Builder) WithTimeout(d time.Duration) *Builder {
	if b != nil && d > 0 {
		b.timeout = d
	}
	return b
}

// Build resolves every container the Ref names — and, one hop further, the containers
// those names expand into (an Expander: a task names the project its world is in) — with
// the same resolver chain. It returns what could be resolved and never an error: a
// decision cycle reasons about the world it could read, and a registry that is down means
// a thinner prompt, not a failed run.
func (b *Builder) Build(ctx context.Context, ref Ref) Result {
	result := Result{Sections: map[string]map[string]any{}}
	if b == nil {
		return result
	}
	if ctx == nil {
		ctx = context.Background()
	}
	queue := namedRefs(ref)
	seen := map[string]bool{}
	for i := 0; i < len(queue); i++ {
		item := queue[i]
		key := item.refType + "\x00" + item.id
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(result.Sections) >= maxContextRefs {
			// The safety valve: expansions are followed in the order they were named, and
			// a ref that names this many containers is not a resolution any more.
			break
		}
		fields := map[string]any{}
		for _, resolver := range b.resolvers {
			if resolver == nil {
				continue
			}
			callCtx, cancel := context.WithTimeout(ctx, b.timeout)
			found, ok, err := resolver.Resolve(callCtx, item.refType, item.id, fields)
			cancel()
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("%s (%s %s): %w", resolver.Name(), item.refType, item.id, err))
				continue
			}
			if !ok {
				continue
			}
			for key, value := range found {
				if value == nil || value == "" {
					continue
				}
				fields[key] = value
			}
		}
		// The ref's own identity is not something a resolver gets a say in: the task
		// named this id, of this type.
		fields["id"] = item.id
		fields["type"] = item.refType
		result.Sections[item.refType] = fields
		if !item.expand {
			continue
		}
		for _, resolver := range b.resolvers {
			expander, ok := resolver.(Expander)
			if !ok {
				continue
			}
			callCtx, cancel := context.WithTimeout(ctx, b.timeout)
			extra := expander.ExtraRefs(callCtx, item.refType, item.id, fields)
			cancel()
			for refType, id := range extra {
				refType, id = strings.TrimSpace(refType), strings.TrimSpace(id)
				if refType == "" || id == "" || seen[refType+"\x00"+id] {
					continue
				}
				queue = append(queue, pendingRef{refType: refType, id: id})
			}
		}
	}
	return result
}

// namedRefs is the refs a caller named, in a stable order and without the ones that name
// nothing. They are the refs that may expand: what they expand to is resolved, not
// expanded again.
func namedRefs(ref Ref) []pendingRef {
	refs := make([]pendingRef, 0, len(ref))
	for refType, id := range ref {
		refType, id = strings.TrimSpace(refType), strings.TrimSpace(id)
		if refType == "" || id == "" {
			continue
		}
		refs = append(refs, pendingRef{refType: refType, id: id, expand: true})
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].refType != refs[j].refType {
			return refs[i].refType < refs[j].refType
		}
		return refs[i].id < refs[j].id
	})
	return refs
}
