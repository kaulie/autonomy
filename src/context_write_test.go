package autonomy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctxsvc "github.com/kaulie/autonomy/src/context"
)

func contextWriteServer(t *testing.T) (*HTTPServer, ctxsvc.Service) {
	t.Helper()
	svc := ctxsvc.NewService(ctxsvc.NewMemoryRepository(), ctxsvc.FileSourceLoader{})
	return NewHTTPServer(&Autonomy{Context: svc}), svc
}

func doContextPost(t *testing.T, srv *HTTPServer, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

func decodeContextBody(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

func TestContextWritePathRegisterSyncSearch(t *testing.T) {
	srv, svc := contextWriteServer(t)
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
	if registered.ID == "" || registered.Type != ctxsvc.ResourceDocument || registered.ProjectID != "project-749a0238" {
		t.Fatalf("registered resource = %+v", registered)
	}

	syncPath := "/api/context/resources/" + registered.ID + "/sync"
	rec = doContextPost(t, srv, syncPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("sync: %d %s", rec.Code, rec.Body.String())
	}
	var first ctxsvc.SyncResult
	decodeContextBody(t, rec, &first)
	if first.Status != ctxsvc.SyncIndexed || first.Sections != 1 || first.Checksum == "" {
		t.Fatalf("first sync = %+v", first)
	}

	results, err := svc.Search(context.Background(), ctxsvc.SearchRequest{ProjectID: "project-749a0238", Query: "heartbeat"})
	if err != nil || len(results) != 1 || results[0].ResourceID != registered.ID {
		t.Fatalf("search = %+v, %v", results, err)
	}

	rec = doContextPost(t, srv, syncPath, "")
	var second ctxsvc.SyncResult
	decodeContextBody(t, rec, &second)
	if rec.Code != http.StatusOK || second.Status != ctxsvc.SyncUnchanged || second.Checksum != first.Checksum {
		t.Fatalf("re-sync: %d %+v", rec.Code, second)
	}
}

func TestContextWritePathSkipsNonDocuments(t *testing.T) {
	srv, _ := contextWriteServer(t)
	rec := doContextPost(t, srv, "/api/context/resources", `{"id":"repo-1","project_id":"p","type":"repository","name":"autonomy"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	rec = doContextPost(t, srv, "/api/context/resources/repo-1/sync", "")
	var result ctxsvc.SyncResult
	decodeContextBody(t, rec, &result)
	if rec.Code != http.StatusOK || result.Status != ctxsvc.SyncSkipped || result.Sections != 0 {
		t.Fatalf("sync repository: %d %+v", rec.Code, result)
	}
}

func TestContextWritePathErrors(t *testing.T) {
	srv, _ := contextWriteServer(t)
	cases := []struct {
		name, path, body string
		status           int
		code             ctxsvc.ErrorCode
	}{
		{"bad json", "/api/context/resources", `{`, http.StatusBadRequest, ctxsvc.ErrInvalidQuery},
		{"no project", "/api/context/resources", `{"name":"x"}`, http.StatusBadRequest, ctxsvc.ErrProjectNotFound},
		{"bad type", "/api/context/resources", `{"project_id":"p","type":"wiki"}`, http.StatusBadRequest, ctxsvc.ErrInvalidQuery},
		{"unknown id", "/api/context/resources/nope/sync", "", http.StatusNotFound, ctxsvc.ErrResourceNotFound},
	}
	for _, tc := range cases {
		rec := doContextPost(t, srv, tc.path, tc.body)
		var got contextErrResponse
		decodeContextBody(t, rec, &got)
		if rec.Code != tc.status || got.Code != tc.code || got.Message == "" {
			t.Errorf("%s: %d %+v, want %d %s", tc.name, rec.Code, got, tc.status, tc.code)
		}
	}

	rec := doContextPost(t, srv, "/api/context/resources", `{"id":"gone","project_id":"p","source":{"location":"/no/such/file.md"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	rec = doContextPost(t, srv, "/api/context/resources/gone/sync", "")
	var got contextErrResponse
	decodeContextBody(t, rec, &got)
	if rec.Code != http.StatusUnprocessableEntity || got.Code != ctxsvc.ErrSourceUnavailable {
		t.Fatalf("missing source: %d %+v", rec.Code, got)
	}
}

func TestContextWritePathWithoutService(t *testing.T) {
	srv := NewHTTPServer(&Autonomy{})
	for _, path := range []string{"/api/context/resources", "/api/context/resources/x/sync"} {
		rec := doContextPost(t, srv, path, `{"project_id":"p"}`)
		var got contextErrResponse
		decodeContextBody(t, rec, &got)
		if rec.Code != http.StatusServiceUnavailable || got.Code != ctxsvc.ErrIndexFailed {
			t.Errorf("%s: %d %+v", path, rec.Code, got)
		}
	}
}
