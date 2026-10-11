package autonomy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	ctxsvc "github.com/kaulie/autonomy/src/context"
)

func doContextGet(t *testing.T, srv *HTTPServer, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// indexedContextServer registers and syncs one Markdown document through the
// write path, so the read path has something to answer with.
func indexedContextServer(t *testing.T) (*HTTPServer, ctxsvc.Resource) {
	t.Helper()
	srv, _ := contextWriteServer(t)
	doc := filepath.Join(t.TempDir(), "runtime.md")
	if err := os.WriteFile(doc, []byte("# Runtime\n\n## Heartbeat\n\nagents send a heartbeat every tick\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `{"project_id":"project-749a0238","name":"runtime.md","source":{"type":"local_file","location":"` + doc + `"}}`
	rec := doContextPost(t, srv, "/api/context/resources", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	var registered ctxsvc.Resource
	decodeContextBody(t, rec, &registered)
	if rec = doContextPost(t, srv, "/api/context/resources/"+registered.ID+"/sync", ""); rec.Code != http.StatusOK {
		t.Fatalf("sync: %d %s", rec.Code, rec.Body.String())
	}
	return srv, registered
}

func TestContextReadPathListGetSearchSection(t *testing.T) {
	srv, registered := indexedContextServer(t)

	rec := doContextGet(t, srv, "/api/context/resources?project_id=project-749a0238")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var list contextResourcesResponse
	decodeContextBody(t, rec, &list)
	if len(list.Resources) != 1 || list.Resources[0].ID != registered.ID {
		t.Fatalf("list = %+v", list)
	}

	rec = doContextGet(t, srv, "/api/context/resources/"+registered.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	var got ctxsvc.Resource
	decodeContextBody(t, rec, &got)
	if got.ID != registered.ID || got.Name != "runtime.md" || got.Checksum == "" {
		t.Fatalf("get = %+v", got)
	}

	rec = doContextGet(t, srv, "/api/context/search?project_id=project-749a0238&q=heartbeat&limit=5&types=document")
	if rec.Code != http.StatusOK {
		t.Fatalf("search: %d %s", rec.Code, rec.Body.String())
	}
	var search contextSearchResponse
	decodeContextBody(t, rec, &search)
	if len(search.Results) == 0 || search.Results[0].ResourceID != registered.ID || search.Results[0].SectionID == "" {
		t.Fatalf("search = %+v", search)
	}

	rec = doContextGet(t, srv, "/api/context/sections/"+search.Results[0].SectionID)
	if rec.Code != http.StatusOK {
		t.Fatalf("section: %d %s", rec.Code, rec.Body.String())
	}
	var section ctxsvc.Section
	decodeContextBody(t, rec, &section)
	if section.ResourceID != registered.ID || section.Content == "" {
		t.Fatalf("section = %+v", section)
	}

	rec = doContextGet(t, srv, "/api/context/search?project_id=project-749a0238&q=heartbeat&types=service")
	decodeContextBody(t, rec, &search)
	if rec.Code != http.StatusOK || len(search.Results) != 0 {
		t.Fatalf("types filter: %d %s", rec.Code, rec.Body.String())
	}
}

func TestContextReadPathErrors(t *testing.T) {
	srv, _ := indexedContextServer(t)
	cases := []struct {
		path   string
		status int
		code   ctxsvc.ErrorCode
	}{
		{"/api/context/resources", http.StatusBadRequest, ctxsvc.ErrProjectNotFound},
		{"/api/context/resources?project_id=%20", http.StatusBadRequest, ctxsvc.ErrProjectNotFound},
		{"/api/context/resources/ctx-missing", http.StatusNotFound, ctxsvc.ErrResourceNotFound},
		{"/api/context/sections/sec-missing", http.StatusNotFound, ctxsvc.ErrResourceNotFound},
		{"/api/context/search?q=heartbeat", http.StatusBadRequest, ctxsvc.ErrProjectNotFound},
		{"/api/context/search?project_id=project-749a0238&q=", http.StatusBadRequest, ctxsvc.ErrInvalidQuery},
		{"/api/context/search?project_id=project-749a0238&q=heartbeat&limit=ten", http.StatusBadRequest, ctxsvc.ErrInvalidQuery},
	}
	for _, c := range cases {
		rec := doContextGet(t, srv, c.path)
		var body contextErrResponse
		decodeContextBody(t, rec, &body)
		if rec.Code != c.status || body.Code != c.code {
			t.Errorf("GET %s = %d %s, want %d %s", c.path, rec.Code, rec.Body.String(), c.status, c.code)
		}
	}
}

func TestContextReadPathWithoutServiceIs503(t *testing.T) {
	srv := NewHTTPServer(&Autonomy{})
	for _, path := range []string{
		"/api/context/resources?project_id=p",
		"/api/context/resources/ctx-1",
		"/api/context/search?project_id=p&q=x",
		"/api/context/sections/sec-1",
	} {
		rec := doContextGet(t, srv, path)
		var body contextErrResponse
		decodeContextBody(t, rec, &body)
		if rec.Code != http.StatusServiceUnavailable || body.Code != ctxsvc.ErrIndexFailed {
			t.Errorf("GET %s = %d %s, want 503 INDEX_FAILED", path, rec.Code, rec.Body.String())
		}
	}
}
