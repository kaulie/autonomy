package contextcap

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/kaulie/autonomy/src/capability/spec"
	ctxsvc "github.com/kaulie/autonomy/src/context"
)

// Search is the context.search capability: full-text + metadata-filtered
// search returning candidate sections, never whole documents.
type Search struct {
	Svc ctxsvc.Service
}

func (Search) Name() string     { return SearchName }
func (Search) Domain() string   { return Domain }
func (Search) Provider() string { return Provider }

func (Search) Description() string {
	return `context.search: find candidate sections of a project's context by query. input: {"project_id":"<project id>","query":"<terms>","limit":10,"resource_types":"document,repository","metadata":"{\"key\":\"value\"}"}. output: {"results":[{resource_id, section_id, title, heading, snippet, score, revision}]} — candidates to judge, not whole documents; fetch one with context.get. Errors are typed: PROJECT_NOT_FOUND / INVALID_QUERY / INDEX_FAILED.`
}

func (Search) Inputs() []spec.Field {
	return []spec.Field{
		{Name: "project_id", Aliases: []string{"project"}, Required: true, Description: "the project whose context to search"},
		{Name: "query", Aliases: []string{"q"}, Required: true, Description: "the search terms (lexical full-text; at least one term)"},
		{Name: "limit", Description: "maximum candidates to return (default 10, max 50)"},
		{Name: "resource_types", Aliases: []string{"types"}, Description: "comma-separated filter: document, repository, service"},
		{Name: "metadata", Description: "JSON object of metadata key/values every candidate resource must match"},
	}
}

func (Search) Outputs() []spec.Field {
	return []spec.Field{
		{Name: "results", Description: `JSON array of {resource_id, section_id, title, heading, snippet, score, revision}`},
	}
}

func (c Search) Run(in map[string]string) (map[string]string, error) {
	if c.Svc == nil {
		return nil, notConfigured(SearchName)
	}
	req := ctxsvc.SearchRequest{
		ProjectID: strings.TrimSpace(firstNonEmpty(in["project_id"], in["project"])),
		Query:     firstNonEmpty(in["query"], in["q"]),
	}
	if v := strings.TrimSpace(in["limit"]); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, ctxsvc.Errorf(ctxsvc.ErrInvalidQuery, "limit %q is not a number", v)
		}
		req.Limit = n
	}
	for _, t := range splitList(firstNonEmpty(in["resource_types"], in["types"])) {
		req.ResourceTypes = append(req.ResourceTypes, ctxsvc.ResourceType(t))
	}
	if v := strings.TrimSpace(in["metadata"]); v != "" {
		var metadata map[string]string
		if err := json.Unmarshal([]byte(v), &metadata); err != nil {
			return nil, ctxsvc.Errorf(ctxsvc.ErrInvalidQuery, "metadata is not a JSON object: %v", err)
		}
		req.Metadata = metadata
	}

	results, err := c.Svc.Search(context.Background(), req)
	if err != nil {
		return nil, err
	}
	type row struct {
		ResourceID string  `json:"resource_id"`
		SectionID  string  `json:"section_id"`
		Title      string  `json:"title"`
		Heading    string  `json:"heading"`
		Snippet    string  `json:"snippet"`
		Score      float64 `json:"score"`
		Revision   string  `json:"revision,omitempty"`
	}
	rows := make([]row, 0, len(results))
	for _, r := range results {
		rows = append(rows, row{r.ResourceID, r.SectionID, r.ResourceName, r.Heading, r.Snippet, r.Score, r.Revision})
	}
	encoded, err := json.Marshal(map[string]any{"results": rows})
	if err != nil {
		return nil, err
	}
	return map[string]string{"results": string(encoded), "count": strconv.Itoa(len(rows))}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
