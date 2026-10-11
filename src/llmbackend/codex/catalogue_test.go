package codex

import (
	"slices"
	"testing"
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
