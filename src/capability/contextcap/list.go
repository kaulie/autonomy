package contextcap

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/kaulie/autonomy/src/capability/spec"
	ctxsvc "github.com/kaulie/autonomy/src/context"
)

// List is the context.list capability: the resource inventory of a project, so
// an agent can see what context exists before searching it.
type List struct {
	Svc ctxsvc.Service
}

func (List) Name() string     { return ListName }
func (List) Domain() string   { return Domain }
func (List) Provider() string { return Provider }

func (List) Description() string {
	return `context.list: list a project's registered context resources (inventory, not content). input: {"project_id":"<project id>"}. output: {"resources":[{id, name, type, uri, revision, source_type}]}. Errors are typed: PROJECT_NOT_FOUND / INDEX_FAILED.`
}

func (List) Inputs() []spec.Field {
	return []spec.Field{
		{Name: "project_id", Aliases: []string{"project"}, Required: true, Description: "the project whose resources to list"},
	}
}

func (List) Outputs() []spec.Field {
	return []spec.Field{
		{Name: "resources", Description: `JSON array of {id, name, type, uri, revision, source_type}`},
	}
}

func (c List) Run(in map[string]string) (map[string]string, error) {
	if c.Svc == nil {
		return nil, notConfigured(ListName)
	}
	projectID := strings.TrimSpace(firstNonEmpty(in["project_id"], in["project"]))
	resources, err := c.Svc.ListResources(context.Background(), projectID)
	if err != nil {
		return nil, err
	}
	type row struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Type       string `json:"type"`
		URI        string `json:"uri,omitempty"`
		Revision   string `json:"revision,omitempty"`
		SourceType string `json:"source_type,omitempty"`
	}
	rows := make([]row, 0, len(resources))
	for _, r := range resources {
		rows = append(rows, row{r.ID, r.Name, string(r.Type), r.URI, r.Revision, r.Source.Type})
	}
	encoded, err := json.Marshal(map[string]any{"resources": rows})
	if err != nil {
		return nil, err
	}
	return map[string]string{"resources": string(encoded), "count": strconv.Itoa(len(rows))}, nil
}
