package codex

import (
	"slices"
	"testing"
)

func TestCodexCatalogueNamesTheWellKnownModels(t *testing.T) {
	models := CodexModels()
	for _, want := range []string{"gpt-5-codex", "gpt-5.4", "o3"} {
		if !slices.Contains(models, want) {
			t.Fatalf("CodexModels=%v want %q so a create-task picker has something to offer", models, want)
		}
	}
}
