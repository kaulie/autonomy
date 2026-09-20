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
