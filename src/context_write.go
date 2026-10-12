package autonomy

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	ctxsvc "github.com/kaulie/autonomy/src/context"
)

// The Context Service's operator write path: register a resource into a
// project's context space, then sync it (load the source, split it into
// sections, write the full-text index). The handlers are in http_server.go
// (POST /api/context/resources, POST /api/context/resources/{id}/sync); the
// CLI is `autonomy context register|sync` (cmd/autonomy). Agents only read,
// through context.search / get / list — this path is not a capability.

const (
	// contextShapeFile registers one source file.
	contextShapeFile = "file"
	// contextShapeDirectory walks a directory recursively and registers each
	// Markdown file as its own document.
	contextShapeDirectory = "directory"
)

// RegisterContextResourceRequest is the body of POST /api/context/resources.
type RegisterContextResourceRequest struct {
	// ID is optional: empty mints ctx-<hex>; an existing id is re-registered.
	ID        string                `json:"id,omitempty"`
	ProjectID string                `json:"project_id"`
	Type      ctxsvc.ResourceType   `json:"type,omitempty"`
	Name      string                `json:"name,omitempty"`
	URI       string                `json:"uri,omitempty"`
	Metadata  map[string]string     `json:"metadata,omitempty"`
	Source    ctxsvc.ResourceSource `json:"source"`
	// Shape is the operator form: file (default) or directory (recursive).
	Shape string `json:"shape,omitempty"`
	// Suffixes filters a directory walk by file extension (md, .markdown).
	// Empty means every default document suffix — search all.
	Suffixes []string `json:"suffixes,omitempty"`
}

// Resource is the request as the Context Service stores it.
func (r RegisterContextResourceRequest) Resource() ctxsvc.Resource {
	return ctxsvc.Resource{
		ID:        strings.TrimSpace(r.ID),
		ProjectID: strings.TrimSpace(r.ProjectID),
		Type:      ctxsvc.ResourceType(strings.ToLower(strings.TrimSpace(string(r.Type)))),
		Name:      strings.TrimSpace(r.Name),
		URI:       strings.TrimSpace(r.URI),
		Metadata:  r.Metadata,
		Source:    r.Source,
	}
}

func (r RegisterContextResourceRequest) shape() string {
	switch s := strings.ToLower(strings.TrimSpace(r.Shape)); s {
	case "", contextShapeFile:
		return contextShapeFile
	case contextShapeDirectory:
		return contextShapeDirectory
	default:
		return s
	}
}

// contextDirectoryRegisterResponse is POST /api/context/resources with shape=directory.
type contextDirectoryRegisterResponse struct {
	Shape     string            `json:"shape"`
	Count     int               `json:"count"`
	Resources []ctxsvc.Resource `json:"resources"`
}

func registerContextDirectory(ctx context.Context, svc ctxsvc.Service, req RegisterContextResourceRequest) (contextDirectoryRegisterResponse, error) {
	root := strings.TrimSpace(req.Source.Location)
	if sub := strings.TrimSpace(req.Source.Path); sub != "" {
		if root == "" {
			root = sub
		} else {
			root = filepath.Join(root, sub)
		}
	}
	files, err := ctxsvc.DocumentFiles(root, req.Suffixes)
	if err != nil {
		return contextDirectoryRegisterResponse{}, err
	}
	if len(files) == 0 {
		if len(req.Suffixes) == 0 {
			return contextDirectoryRegisterResponse{}, ctxsvc.Errorf(ctxsvc.ErrInvalidQuery, "no markdown files under %q", root)
		}
		return contextDirectoryRegisterResponse{}, ctxsvc.Errorf(ctxsvc.ErrInvalidQuery, "no files matching suffixes %v under %q", req.Suffixes, root)
	}
	srcType := strings.TrimSpace(req.Source.Type)
	if srcType == "" {
		srcType = "local_file"
	}
	out := make([]ctxsvc.Resource, 0, len(files))
	for _, rel := range files {
		resource, err := svc.Register(ctx, ctxsvc.Resource{
			ProjectID: strings.TrimSpace(req.ProjectID),
			Type:      ctxsvc.ResourceDocument,
			Name:      rel,
			Source: ctxsvc.ResourceSource{
				Type:     srcType,
				Location: root,
				Path:     rel,
			},
			Metadata: req.Metadata,
		})
		if err != nil {
			return contextDirectoryRegisterResponse{}, err
		}
		out = append(out, resource)
	}
	return contextDirectoryRegisterResponse{Shape: contextShapeDirectory, Count: len(out), Resources: out}, nil
}

func rejectFileShapeOnDirectory(req RegisterContextResourceRequest) error {
	loc := strings.TrimSpace(req.Source.Location)
	if loc == "" {
		return nil
	}
	info, err := os.Stat(loc)
	if err != nil || !info.IsDir() {
		return nil
	}
	if strings.TrimSpace(req.Source.Path) != "" {
		return nil
	}
	return ctxsvc.Errorf(ctxsvc.ErrInvalidQuery, "location %q is a directory; use shape directory", loc)
}

// contextErrResponse is a Context Service failure as the write path answers
// it: the typed code an operator branches on, and the message.
type contextErrResponse struct {
	Code    ctxsvc.ErrorCode `json:"code"`
	Message string           `json:"message"`
}

func writeContextErr(w http.ResponseWriter, code int, errCode ctxsvc.ErrorCode, msg string) {
	writeJSON(w, code, contextErrResponse{Code: errCode, Message: msg})
}

// writeContextServiceErr maps a typed Context Service error onto its HTTP
// status: bad input 400, unknown resource 404, unreadable or unparsable
// source 422, anything else (the index itself) 500.
func writeContextServiceErr(w http.ResponseWriter, err error) {
	code := ctxsvc.CodeOf(err)
	status := http.StatusInternalServerError
	switch code {
	case ctxsvc.ErrInvalidQuery, ctxsvc.ErrProjectNotFound:
		// PROJECT_NOT_FOUND is only raised for a missing project id.
		status = http.StatusBadRequest
	case ctxsvc.ErrResourceNotFound:
		status = http.StatusNotFound
	case ctxsvc.ErrSourceUnavailable, ctxsvc.ErrParseFailed:
		status = http.StatusUnprocessableEntity
	case "":
		code = ctxsvc.ErrIndexFailed
	}
	writeContextErr(w, status, code, strings.TrimPrefix(err.Error(), string(code)+": "))
}

// contextService is the Context Service the write path uses, or nil (with the
// 503 already written) when this runtime has none: a store engine that cannot
// back it (sqlite) leaves it unconfigured.
func (s *HTTPServer) contextService(w http.ResponseWriter) ctxsvc.Service {
	if s.Autonomy == nil || s.Autonomy.Context == nil {
		writeContextErr(w, http.StatusServiceUnavailable, ctxsvc.ErrIndexFailed,
			"context service is not configured: it needs the postgres store engine (AUTONOMY_STORE_ENGINE=postgres)")
		return nil
	}
	return s.Autonomy.Context
}

// contextResourcesResponse is the body of GET /api/context/resources.
type contextResourcesResponse struct {
	Resources []ctxsvc.Resource `json:"resources"`
}

// contextSearchResponse is the body of GET /api/context/search.
type contextSearchResponse struct {
	Results []ctxsvc.SearchResult `json:"results"`
}

// contextSearchRequest reads GET /api/context/search's query string:
// project_id, q, limit and the comma-separated types filter. The service
// validates the project and the terms; a limit that is not a number is
// INVALID_QUERY here.
func contextSearchRequest(q url.Values) (ctxsvc.SearchRequest, error) {
	req := ctxsvc.SearchRequest{
		ProjectID: strings.TrimSpace(q.Get("project_id")),
		Query:     q.Get("q"),
	}
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			return req, ctxsvc.Errorf(ctxsvc.ErrInvalidQuery, "limit %q is not a number", raw)
		}
		req.Limit = limit
	}
	for _, t := range strings.Split(q.Get("types"), ",") {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			req.ResourceTypes = append(req.ResourceTypes, ctxsvc.ResourceType(t))
		}
	}
	return req, nil
}
