package autonomy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The vendor and model catalogues are what a pool UI offers as lists instead of asking a human to
// type identifiers (web-cursor serves the same pair from its provider registry). They come from
// the harness itself: for cline that is the bridge's provider catalogue, for codex and cursor the
// harness has exactly one vendor to name.
func TestTheVendorAndModelCataloguesComeFromTheHarness(t *testing.T) {
	installFakeClineClient(t) // the process-wide cline client is the fake bridge
	f := NewAgentFactory()
	runtime := &Autonomy{AgentFactory: f, Runtime: NewRuntime(f), Store: activeStore()}
	ctx := context.Background()

	vendors, err := runtime.AccountVendors(ctx, "cline", "")
	if err != nil {
		t.Fatalf("cline vendors: %v", err)
	}
	if strings.Join(vendors, ",") != "anthropic,deepseek" {
		t.Fatalf("cline vendors=%v want the bridge's providers, sorted", vendors)
	}

	models, err := runtime.AccountModels(ctx, "cline", "deepseek", "")
	if err != nil {
		t.Fatalf("cline models: %v", err)
	}
	if strings.Join(models, ",") != "deepseek-v4-pro" {
		t.Fatalf("cline models=%v want the vendor's models", models)
	}

	codexVendors, err := runtime.AccountVendors(ctx, "codex", "")
	if err != nil || strings.Join(codexVendors, ",") != "openai" {
		t.Fatalf("codex vendors=%v err=%v want openai", codexVendors, err)
	}
	if cursorVendors, err := runtime.AccountVendors(ctx, "cursor", ""); err != nil || strings.Join(cursorVendors, ",") != "cursor" {
		t.Fatalf("cursor vendors=%v err=%v want cursor", cursorVendors, err)
	}
	if _, err := runtime.AccountVendors(ctx, "gemini", ""); err == nil {
		t.Fatal("an unknown harness must be refused")
	}
}

// The two endpoints are what the page calls, so they are covered over HTTP too.
func TestTheCatalogueEndpointsAnswerOverHTTP(t *testing.T) {
	installFakeClineClient(t)
	server := NewHTTPServer(&Autonomy{Store: activeStore()})

	for _, tc := range []struct {
		path string
		want string
	}{
		{"/api/accounts/vendors?harness=cline", "\"deepseek\""},
		{"/api/accounts/models?harness=cline&vendor=deepseek", "\"deepseek-v4-pro\""},
		{"/api/accounts/vendors?harness=gemini", "error"},
	} {
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if tc.want == "error" {
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s status=%d want 400", tc.path, rec.Code)
			}
			continue
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", tc.path, rec.Code, rec.Body.String())
		}
		var body accountCatalogueResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s decode: %v", tc.path, err)
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s body=%s want %s", tc.path, rec.Body.String(), tc.want)
		}
	}
}
