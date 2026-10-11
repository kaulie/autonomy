package autonomy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	ctxsvc "github.com/kaulie/autonomy/src/context"
)

func doContextDelete(t *testing.T, srv *HTTPServer, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, path, nil))
	return rec
}

func TestContextDeleteResource(t *testing.T) {
	srv, registered := indexedContextServer(t)

	rec := doContextGet(t, srv, "/api/context/search?project_id=project-749a0238&q=heartbeat")
	var search contextSearchResponse
	decodeContextBody(t, rec, &search)
	if len(search.Results) == 0 {
		t.Fatalf("search before delete: %s", rec.Body.String())
	}
	sectionID := search.Results[0].SectionID

	rec = doContextDelete(t, srv, "/api/context/resources/"+registered.ID)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("delete: %d %q", rec.Code, rec.Body.String())
	}

	for _, path := range []string{"/api/context/resources/" + registered.ID, "/api/context/sections/" + sectionID} {
		if rec = doContextGet(t, srv, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s after delete = %d %s, want 404", path, rec.Code, rec.Body.String())
		}
	}
	rec = doContextGet(t, srv, "/api/context/resources?project_id=project-749a0238")
	var list contextResourcesResponse
	decodeContextBody(t, rec, &list)
	if len(list.Resources) != 0 {
		t.Errorf("list after delete = %+v", list)
	}
	rec = doContextGet(t, srv, "/api/context/search?project_id=project-749a0238&q=heartbeat")
	decodeContextBody(t, rec, &search)
	if len(search.Results) != 0 {
		t.Errorf("search after delete = %+v", search)
	}

	rec = doContextDelete(t, srv, "/api/context/resources/"+registered.ID)
	var body contextErrResponse
	decodeContextBody(t, rec, &body)
	if rec.Code != http.StatusNotFound || body.Code != ctxsvc.ErrResourceNotFound {
		t.Errorf("delete unknown = %d %s, want 404 RESOURCE_NOT_FOUND", rec.Code, rec.Body.String())
	}
}

func TestContextDeleteResourceWithoutServiceIs503(t *testing.T) {
	rec := doContextDelete(t, NewHTTPServer(&Autonomy{}), "/api/context/resources/ctx-1")
	var body contextErrResponse
	decodeContextBody(t, rec, &body)
	if rec.Code != http.StatusServiceUnavailable || body.Code != ctxsvc.ErrIndexFailed {
		t.Errorf("delete without service = %d %s, want 503 INDEX_FAILED", rec.Code, rec.Body.String())
	}
}
