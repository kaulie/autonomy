package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// contextAPI is a stand-in for the runtime's context write path: it records
// what the CLI sent and answers the way the runtime does.
func contextAPI(t *testing.T) (*httptest.Server, *[]string, *contextResourceRequest) {
	t.Helper()
	var calls []string
	var registered contextResourceRequest
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/context/resources", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if err := json.NewDecoder(r.Body).Decode(&registered); err != nil {
			t.Errorf("register body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"ctx-1","project_id":"` + registered.ProjectID + `","type":"document"}`))
	})
	mux.HandleFunc("POST /api/context/resources/{id}/sync", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.PathValue("id") != "ctx-1" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"RESOURCE_NOT_FOUND","message":"resource \"` + r.PathValue("id") + `\" is not registered"}`))
			return
		}
		_, _ = w.Write([]byte(`{"resource_id":"ctx-1","status":"indexed","sections":3}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &calls, &registered
}

func TestContextRegisterAndSync(t *testing.T) {
	srv, calls, registered := contextAPI(t)
	var stdout, stderr bytes.Buffer
	code := cli([]string{"context", "register", "--server", srv.URL, "--project", "project-749a0238",
		"--name", "runtime.md", "--source-type", "git", "--location", "/srv/autonomy", "--path", "docs/runtime.md", "--sync"},
		&stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	want := []string{"POST /api/context/resources", "POST /api/context/resources/ctx-1/sync"}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v", *calls)
	}
	got := *registered
	if got.ProjectID != "project-749a0238" || got.Type != "document" || got.Name != "runtime.md" ||
		got.Source != (contextSource{Type: "git", Location: "/srv/autonomy", Path: "docs/runtime.md"}) {
		t.Fatalf("register body = %+v", got)
	}
	out := stdout.String()
	if !strings.Contains(out, `"id": "ctx-1"`) || !strings.Contains(out, `"status": "indexed"`) {
		t.Fatalf("stdout = %q", out)
	}
}

func TestContextRegisterWithoutSync(t *testing.T) {
	srv, calls, _ := contextAPI(t)
	var stdout, stderr bytes.Buffer
	if code := cli([]string{"context", "register", "--server", srv.URL, "--project", "p", "--location", "a.md"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %v, want register only", *calls)
	}
}

func TestContextSyncError(t *testing.T) {
	srv, _, _ := contextAPI(t)
	var stdout, stderr bytes.Buffer
	code := cli([]string{"context", "sync", "--server", srv.URL, "nope"}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "RESOURCE_NOT_FOUND: resource \"nope\" is not registered") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
}

func TestContextUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"context"},
		{"context", "bogus"},
		{"context", "register", "--name", "x"},
		{"context", "sync"},
	} {
		var stdout, stderr bytes.Buffer
		if code := cli(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit %d, want 2 (stderr %q)", args, code, stderr.String())
		}
	}
}
