package autonomy

import (
	"net/http"
	"strings"

	ctxsvc "github.com/kaulie/autonomy/src/context"
)

// The Context Service's operator write path: register a resource into a
// project's context space, then sync it (load the source, split it into
// sections, write the full-text index). The handlers are in http_server.go
// (POST /api/context/resources, POST /api/context/resources/{id}/sync); the
// CLI is `autonomy context register|sync` (cmd/autonomy). Agents only read,
// through context.search / get / list — this path is not a capability.

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
	writeContextErr(w, status, code, err.Error())
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
