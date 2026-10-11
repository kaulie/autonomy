package codex

import (
	"slices"
	"testing"
)

func TestCodexCatalogueNamesTheWellKnownModels(t *testing.T) {
	models := CodexModels()
	for _, want := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5-codex"} {
		if !slices.Contains(models, want) {
			t.Fatalf("CodexModels=%v want %q so a create-task picker has the current Codex family", models, want)
		}
	}
}
