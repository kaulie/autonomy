package autonomy

import "github.com/kaulie/autonomy/src/llmbackend"

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHealthReportsTheLLMBackend: when every run of a runtime fails the same way,
// the first question is which backend it runs on — and a caller cannot read the
// runtime's environment. /health answers it (and the model that backend defaults
// to), so a Cursor account out of quota and a Cline switch are told apart without
// reading a log or a process's env (docs/llm-backend.md).
func TestHealthReportsTheLLMBackend(t *testing.T) {
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	t.Setenv("AUTONOMY_CLINE_PROVIDER", "deepseek")
	t.Setenv("AUTONOMY_CLINE_MODEL", "deepseek-v4-pro")

	got := healthBody(t, "/health")
	if got.Status != "ok" {
		t.Fatalf("status=%q want ok", got.Status)
	}
	if got.LLMBackend != string(llmbackend.Cline) || got.LLMModel != "deepseek-v4-pro" {
		t.Fatalf("health=%+v want cline on deepseek-v4-pro", got)
	}
	if alias := healthBody(t, "/healthz"); alias != got {
		t.Fatalf("healthz=%+v want the same answer as /health (%+v)", alias, got)
	}

	// The default is Cursor, and its model default is the Cursor one:
	// AUTONOMY_LLM_MODEL is that backend's and never a Cline provider's.
	t.Setenv("AUTONOMY_LLM_BACKEND", "")
	t.Setenv("AUTONOMY_LLM_MODEL", "")
	t.Setenv("AUTONOMY_CLINE_MODEL", "")
	got = healthBody(t, "/health")
	if got.LLMBackend != string(llmbackend.Cursor) || got.LLMModel != "composer-2" {
		t.Fatalf("health=%+v want the cursor default", got)
	}

	// A Cline runtime with no AUTONOMY_CLINE_MODEL reports no model rather than
	// inventing one: the bridge resolves it from the provider/model saved by
	// `cline auth`, which this process does not hold.
	t.Setenv("AUTONOMY_LLM_BACKEND", "cline")
	if got = healthBody(t, "/health"); got.LLMBackend != string(llmbackend.Cline) || got.LLMModel != "" {
		t.Fatalf("health=%+v want cline with no model of its own", got)
	}
}

// healthBody is what one probe answers, decoded.
func healthBody(t *testing.T, path string) healthResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	// The probe answers from the process's own configuration, so it needs no
	// Autonomy: the nil server is the same one the contract test builds.
	NewHTTPServer(nil).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
	}
	var got healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%s body=%s: %v", path, rec.Body.String(), err)
	}
	return got
}
