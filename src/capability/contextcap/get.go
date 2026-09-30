package contextcap

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/kaulie/autonomy/src/capability/spec"
	ctxsvc "github.com/kaulie/autonomy/src/context"
)

// Get is the context.get capability: it fetches one section's full content with
// its provenance, the step an agent takes after a search picks a candidate.
type Get struct {
	Svc ctxsvc.Service
}

func (Get) Name() string     { return GetName }
func (Get) Domain() string   { return Domain }
func (Get) Provider() string { return Provider }

func (Get) Description() string {
	return `context.get: fetch one section's full content and provenance. input: {"section_id":"<id>","resource_id":"<optional cross-check>"}. output: {resource_id, section_id, heading, content, revision, source}. Errors are typed: RESOURCE_NOT_FOUND / INVALID_QUERY / INDEX_FAILED.`
}

func (Get) Inputs() []spec.Field {
	return []spec.Field{
		{Name: "section_id", Required: true, Description: "the section to fetch, as a context.search result reported it"},
		{Name: "resource_id", Description: "optional: refuse the fetch unless the section belongs to this resource"},
	}
}

func (Get) Outputs() []spec.Field {
	return []spec.Field{
		{Name: "resource_id", Description: "the resource the section belongs to"},
		{Name: "section_id", Description: "the section id"},
		{Name: "heading", Description: "the section heading"},
		{Name: "content", Description: "the section's full content"},
		{Name: "revision", Description: "the resource revision the section was indexed at"},
		{Name: "source", Description: "JSON of the resource's source (type/location/repository/path)"},
	}
}

func (c Get) Run(in map[string]string) (map[string]string, error) {
	if c.Svc == nil {
		return nil, notConfigured(GetName)
	}
	sectionID := strings.TrimSpace(in["section_id"])
	if sectionID == "" {
		return nil, ctxsvc.Errorf(ctxsvc.ErrInvalidQuery, "section_id is required")
	}
	section, err := c.Svc.GetSection(context.Background(), sectionID)
	if err != nil {
		return nil, err
	}
	if want := strings.TrimSpace(in["resource_id"]); want != "" && want != section.ResourceID {
		return nil, ctxsvc.Errorf(ctxsvc.ErrResourceNotFound,
			"section %q belongs to resource %q, not %q", sectionID, section.ResourceID, want)
	}
	source, err := json.Marshal(section.Source)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"resource_id": section.ResourceID,
		"section_id":  section.ID,
		"heading":     section.Heading,
		"content":     section.Content,
		"revision":    section.Revision,
		"source":      string(source),
	}, nil
}
