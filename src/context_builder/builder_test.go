package context_builder

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// stubResolver is a resolver whose answer is written down: what it returns, what it
// saw, whether it knows anything, and how long it takes.
type stubResolver struct {
	name   string
	fields map[string]any
	ok     bool
	err    error
	delay  time.Duration
	seen   map[string]any
}

func (s *stubResolver) Name() string { return s.name }

func (s *stubResolver) Resolve(ctx context.Context, refType, id string, built map[string]any) (map[string]any, bool, error) {
	s.seen = built
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
	return s.fields, s.ok, s.err
}

func TestBuildMergesResolversInOrder(t *testing.T) {
	local := &stubResolver{name: "world", ok: true, fields: map[string]any{
		"name": "the container's own name", "description": "Project 1 description", "domain": "software_development"}}
	registry := &stubResolver{name: "project_registry", ok: true, fields: map[string]any{
		"name": "autonomy", "git_repo_url": "https://github.com/kaulie/autonomy"}}

	result := New(local, registry).Build(context.Background(), Ref{RefTypeProject: "project-1"})
	if len(result.Errors) != 0 {
		t.Fatalf("errors=%v, want none", result.Errors)
	}
	section := result.Sections[RefTypeProject]
	// Later answers win per field; fields only the first resolver knows survive.
	if section["name"] != "autonomy" {
		t.Fatalf("name=%v, want the registry's answer to win", section["name"])
	}
	if section["description"] != "Project 1 description" || section["domain"] != "software_development" {
		t.Fatalf("section=%v, want the fields only the process knows", section)
	}
	if section["git_repo_url"] != "https://github.com/kaulie/autonomy" {
		t.Fatalf("section=%v, want the registry's repository", section)
	}
	// The ref's own identity is the task's, not a resolver's.
	if section["id"] != "project-1" || section["type"] != RefTypeProject {
		t.Fatalf("section=%v, want the id and type the task named", section)
	}
}

func TestBuildSeesWhatEarlierResolversFound(t *testing.T) {
	first := &stubResolver{name: "world", ok: true, fields: map[string]any{"name": "Project 1"}}
	second := &stubResolver{name: "second", ok: true, fields: map[string]any{"from_first": nil}}
	New(first, second).Build(context.Background(), Ref{RefTypeProject: "project-1"})

	// The second resolver was asked with the first one's merged answer: that is the
	// pipeline a resolver that builds on another's answer (organization on a project's
	// department id) relies on.
	if second.seen["name"] != "Project 1" {
		t.Fatalf("second resolver saw %v, want the first one's fields", second.seen)
	}
}

func TestBuildKeepsGoingWhenAResolverFails(t *testing.T) {
	broken := &stubResolver{name: "project_registry", err: errors.New("dial tcp: connection refused")}
	working := &stubResolver{name: "organization", ok: true, fields: map[string]any{"organization": map[string]any{"id": "D0005"}}}

	result := New(broken, working).Build(context.Background(), Ref{RefTypeProject: "project-1"})
	if len(result.Errors) != 1 {
		t.Fatalf("errors=%v, want the failure recorded", result.Errors)
	}
	if !strings.Contains(result.Errors[0].Error(), "project_registry") {
		t.Fatalf("error=%v, want it to name the resolver", result.Errors[0])
	}
	section := result.Sections[RefTypeProject]
	if section["organization"] == nil || section["id"] != "project-1" {
		t.Fatalf("section=%v, want what the working resolver found and the ref itself", section)
	}
}

func TestBuildIsBoundedByItsTimeout(t *testing.T) {
	slow := &stubResolver{name: "slow", ok: true, delay: time.Second}
	quick := &stubResolver{name: "quick", ok: true, fields: map[string]any{"name": "autonomy"}}

	start := time.Now()
	result := New(slow, quick).WithTimeout(20*time.Millisecond).Build(context.Background(), Ref{RefTypeProject: "project-1"})
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("build took %s, want the resolver's call bounded", elapsed)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("errors=%v, want the slow resolver's timeout recorded", result.Errors)
	}
	if result.Sections[RefTypeProject]["name"] != "autonomy" {
		t.Fatalf("section=%v, want what still answered", result.Sections[RefTypeProject])
	}
}

// stubExpander is a resolver that also names a further ref — the shape a task resolver
// has: the task, and the project its world is in.
type stubExpander struct {
	stubResolver
	// forType limits what it expands: a ref of another type is not its business.
	forType string
	extra   Ref
}

func (s *stubExpander) ExtraRefs(_ context.Context, refType, _ string, _ map[string]any) Ref {
	if s.forType != "" && s.forType != refType {
		return nil
	}
	return s.extra
}

func TestBuildFollowsAnExpansionOneHop(t *testing.T) {
	// A task ref names the project its world is in; the project is resolved with the same
	// chain, and nothing beyond it is (one hop).
	project := &stubResolver{name: "project_registry", ok: true, fields: map[string]any{"name": "autonomy"}}
	// This one would expand a project — but the project here came from an expansion, so it
	// is never asked, and the team it would have named stays out of the result.
	projectExpander := &stubExpander{
		stubResolver: stubResolver{name: "project_expander"},
		forType:      RefTypeProject, extra: Ref{RefTypeTeam: "team-1"},
	}
	task := &stubExpander{
		stubResolver: stubResolver{name: "task_store", ok: true, fields: map[string]any{"description": "do it"}},
		forType:      RefTypeTask, extra: Ref{RefTypeProject: "project-749a0238"},
	}

	result := New(task, project, projectExpander).Build(context.Background(), Ref{RefTypeTask: "task-1"})
	if len(result.Errors) != 0 {
		t.Fatalf("errors=%v, want none", result.Errors)
	}
	if result.Sections[RefTypeTask]["description"] != "do it" {
		t.Fatalf("sections=%v, want the ref the caller named", result.Sections)
	}
	if result.Sections[RefTypeProject]["name"] != "autonomy" {
		t.Fatalf("sections=%v, want the ref the expansion named", result.Sections)
	}
	if _, found := result.Sections[RefTypeTeam]; found {
		t.Fatalf("sections=%v, want an expansion's own expansion not followed", result.Sections)
	}
}

func TestBuildResolvesARefOnceEvenWhenAnExpansionNamesItBack(t *testing.T) {
	// A ref that names itself back: a ref already resolved is not resolved twice, so this
	// terminates.
	reads := &countingResolver{
		name: "task_store", ok: true, fields: map[string]any{"description": "do it"},
		forType: RefTypeTask, extra: Ref{RefTypeTask: "task-1"},
	}

	result := New(reads).Build(context.Background(), Ref{RefTypeTask: "task-1"})
	if len(result.Sections) != 1 || reads.calls() != 1 {
		t.Fatalf("sections=%v calls=%d, want the ref resolved once", result.Sections, reads.calls())
	}
}

// countingResolver is an expander that counts how many times it was asked.
type countingResolver struct {
	name    string
	fields  map[string]any
	ok      bool
	forType string
	extra   Ref
	asked   int
}

func (c *countingResolver) Name() string { return c.name }

func (c *countingResolver) Resolve(context.Context, string, string, map[string]any) (map[string]any, bool, error) {
	c.asked++
	return c.fields, c.ok, nil
}

func (c *countingResolver) calls() int { return c.asked }

func (c *countingResolver) ExtraRefs(_ context.Context, refType, _ string, _ map[string]any) Ref {
	if c.forType != "" && c.forType != refType {
		return nil
	}
	return c.extra
}

func TestBuildOfNothingIsEmpty(t *testing.T) {
	resolver := &stubResolver{name: "world", ok: true, fields: map[string]any{"name": "x"}}
	builder := New(resolver)

	if result := builder.Build(context.Background(), nil); len(result.Sections) != 0 || len(result.Errors) != 0 {
		t.Fatalf("result=%+v, want nothing for a ref that names nothing", result)
	}
	if result := builder.Build(context.Background(), Ref{RefTypeProject: "  ", RefTypeTeam: "team-1", "": "x"}); len(result.Sections) != 1 {
		t.Fatalf("result=%+v, want only the refs that name an id", result)
	}
	var none *Builder
	if result := none.Build(context.Background(), Ref{RefTypeProject: "project-1"}); len(result.Sections) != 0 {
		t.Fatalf("result=%+v, want a nil builder to resolve nothing", result)
	}
}

func TestAResolverThatKnowsNothingIsNotAFailure(t *testing.T) {
	quiet := &stubResolver{name: "world"}
	result := New(quiet).Build(context.Background(), Ref{RefTypeTeam: "team-1"})
	if len(result.Errors) != 0 {
		t.Fatalf("errors=%v, want none", result.Errors)
	}
	section := result.Sections[RefTypeTeam]
	if len(section) != 2 || section["id"] != "team-1" || section["type"] != RefTypeTeam {
		t.Fatalf("section=%v, want the ref itself and nothing else", section)
	}
}
