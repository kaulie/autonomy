package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/kaulie/autonomy/src/llmbackend"
)

func TestCodexCatalogueNamesTheWellKnownModels(t *testing.T) {
	models := CodexModels()
	for _, want := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		if !slices.Contains(models, want) {
			t.Fatalf("CodexModels=%v want %q so a create-task picker has the ChatGPT-signed-in Codex family", models, want)
		}
	}
	for _, stale := range []string{"gpt-5-codex", "o3", "o4-mini"} {
		if slices.Contains(models, stale) {
			t.Fatalf("CodexModels must not suggest %q: ChatGPT-signed-in Codex rejects it", stale)
		}
	}
}

func TestCodexModelsPrefersTheLiveAppServerCatalogue(t *testing.T) {
	useFakeCodex(t, map[string]string{"CODEX_FAKE_MODELS": "gpt-5.6-sol,only-from-cli"})
	got, err := codexModels(context.Background(), llmbackend.Creds{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"gpt-5.6-sol", "only-from-cli"}) {
		t.Fatalf("codexModels=%v want the live list only (no snapshot merge)", got)
	}
	if slices.Contains(got, "gpt-6-sol") {
		t.Fatalf("a live catalogue must not be padded with the snapshot: %v", got)
	}
}

func TestCodexModelsFallsBackWhenAppServerIsMissing(t *testing.T) {
	t.Setenv("AUTONOMY_CODEX_BIN", filepath.Join(t.TempDir(), "missing-codex"))
	got, err := codexModels(context.Background(), llmbackend.Creds{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, CodexModels()) {
		t.Fatalf("codexModels=%v want the snapshot when the CLI is missing", got)
	}
}

func TestListLiveCodexModelsPaginates(t *testing.T) {
	useFakeCodex(t, map[string]string{
		"CODEX_FAKE_MODELS":    "gpt-5.6-sol,gpt-5.6-terra,gpt-5.6-luna",
		"CODEX_FAKE_PAGE_SIZE": "1",
	})
	got, err := listLiveCodexModels(context.Background(), llmbackend.Creds{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}) {
		t.Fatalf("paginated live list=%v", got)
	}
}

func TestListLiveCodexModelsPassesAccountCredentials(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env.json")
	useFakeCodex(t, map[string]string{"CODEX_FAKE_ENV_FILE": envFile})
	if _, err := listLiveCodexModels(context.Background(), llmbackend.Creds{
		APIKey:  "account-key",
		BaseURL: "https://example.invalid/v1",
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		APIKey  string `json:"CODEX_API_KEY"`
		OpenAI  string `json:"OPENAI_API_KEY"`
		BaseURL string `json:"OPENAI_BASE_URL"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.APIKey != "account-key" || env.OpenAI != "account-key" || env.BaseURL != "https://example.invalid/v1" {
		t.Fatalf("app-server env=%s want the account's key and base URL", raw)
	}
}

func TestCodexBinaryPrefersTheBridgeBundle(t *testing.T) {
	dir := t.TempDir()
	bundled := filepath.Join(dir, "node_modules", ".bin")
	if err := os.MkdirAll(bundled, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bundled, "codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUTONOMY_CODEX_BIN", "")
	t.Setenv("AUTONOMY_CODEX_BRIDGE_SCRIPT", filepath.Join(dir, "bridge.mjs"))
	if got := binary(); got != path {
		t.Fatalf("binary()=%q want the SDK bundle next to the bridge script", got)
	}
	t.Setenv("AUTONOMY_CODEX_BIN", "/opt/codex")
	if got := binary(); got != "/opt/codex" {
		t.Fatalf("binary()=%q want the explicit AUTONOMY_CODEX_BIN", got)
	}
}

func useFakeCodex(t *testing.T, extra map[string]string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	bin := filepath.Join(filepath.Dir(file), "..", "..", "codexsdk", "bridge", "testdata", "fake-codex.mjs")
	if _, err := os.Stat(bin); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUTONOMY_CODEX_BIN", bin)
	for key, value := range extra {
		t.Setenv(key, value)
	}
}
